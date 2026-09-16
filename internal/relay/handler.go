package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/client"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Forward 按客户端协议承载一个请求的完整转发过程: 解析请求, 定位分组, 循环选目标请求上游, 直至提交响应或请求结束。
func Forward(format llm.APIFormat) gin.HandlerFunc {
	var inbound transformer.Inbound
	switch format {
	case llm.APIFormatOpenAIResponse:
		inbound = responses.NewInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		inbound = anthropic.NewInboundTransformer()
	default:
		inbound = openai.NewInboundTransformer()
	}

	return func(c *gin.Context) {
		// 全局停止: 任何新请求立即 503, 不消耗转发协程与请求状态; 在途请求由后台单独终止。
		if IsAllStopped() {
			rejectRequest(c, inbound, errAllRequestsStopped)
			return
		}
		// 完整读取客户端请求, 正文先登记到请求状态, 后续每轮直接改写为当前目标请求。
		raw, err := readLimitedHTTPRequest(c.Request)
		if err != nil {
			if errors.Is(err, ErrRelayBodyTooLarge) {
				resp.Error(c, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			rejectRequest(c, inbound, err)
			return
		}

		// 协议自动检测: 部分客户端(如 ZCode)将原生 Anthropic Messages 格式请求
		// 发往 /v1/chat/completions 端点。OpenAI Chat 解析器会静默丢弃顶层 system
		// 字段并破坏 input_schema 工具定义, 导致上游收到空壳请求返回空响应。
		// 检测到 Anthropic 格式标记时切换为 Anthropic 入站转换器, 避免数据丢失。
		format := format
		inbound := inbound
		if format == llm.APIFormatOpenAIChatCompletion && looksLikeAnthropicMessages(raw.Body) {
			format = llm.APIFormatAnthropicMessage
			inbound = anthropic.NewInboundTransformer()
		}

		// 此处只读取选组和分流所需字段; 完整协议校验由同协议上游或跨协议 pipeline 完成。
		// gjson 单字段提取: 完整 json.Unmarshal 会为每个请求构建整棵泛型解析树,
		// 对多 MB 长上下文请求体是纯粹的每请求浪费。
		metadataModel := gjson.GetBytes(raw.Body, "model").String()
		// multipart/form-data 请求(图片编辑/变体、语音转写/翻译): JSON 解析取不到 model,
		// 从 Content-Type 中的 boundary 构造 multipart.Reader 提取表单字段。
		if metadataModel == "" {
			metadataModel = multipartFormField(raw.Headers.Get("Content-Type"), raw.Body, "model")
		}
		metadataStreaming := gjson.GetBytes(raw.Body, "stream").Bool()

		// API Key 限定了模型范围时只放行范围内的模型, 为空表示不限制。
		if allowed := c.GetString("supported_models"); allowed != "" && !slices.Contains(strings.Split(allowed, ","), metadataModel) {
			rejectRequest(c, inbound, errors.New("model not supported by this api key"))
			return
		}

		// 客户端请求的模型名称即分组名称; 分组不存在说明模型名错误, 等待也不会出现该分组。
		if _, err := op.GroupGetByName(metadataModel); err != nil {
			rejectRequest(c, inbound, errors.New("model not found"))
			return
		}

		// 登记进程内请求状态, 返回的记录是后续全部状态写入和前端可视化推送的入口。
		// 请求体以安全拷贝移交: string(raw.Body) 复制一份独立副本, 避免后续 sjson 改写
		// raw.Body 时影响已存入 RequestState.body 的字符串(违反 Go 字符串不可变契约)。
		requestBodyString := string(raw.Body)
		apiKeyRaw := c.GetString("api_key_raw")
		apiKeyName := c.GetString("api_key_name")
		request := newRequestState(metadataModel, requestBodyString, c.ClientIP(), apiKeyRaw, apiKeyName)
		op.TrackClientStat(c.ClientIP())
		ctx := c.Request.Context()
		failureCounts := make(map[int]*memberFailureCounts) // 各成员的业务/基础设施失败独立计数, 成员间互不继承互不清零。
		// 会话标识优先取 X-Session-Id, 缺省时回退读取 opencode 兼容头 x-opencode-session,
		// 两者均为空表示客户端未启用会话粘合与稳定随机头。
		sessionKey := c.GetHeader("X-Session-Id")
		if sessionKey == "" {
			sessionKey = c.GetHeader(opencodeSessionHeader)
		}
		// 脱敏: 全局开关 + 分组开关均开时对请求体执行一次脱敏, 映射表供响应还原复用。
		// 每轮重试复用同一脱敏结果, 不重复扫描(文档 01 §二)。fail-closed: 脱敏失败拒绝放行明文。
		// 分组暂不可得时跳过脱敏(循环内会等待分组出现), 开关任一关时零开销短路(文档 04 §1.4)。
		var maskMapping *mask.Mapping
		var streamRestorer *mask.StreamRestorer
		if g, gErr := op.GroupGetByName(metadataModel); gErr == nil {
			masked, mapping, matches, mErr := applyRequestMask(raw.Body, sessionKey, g.RelayConfig.MaskEnabled)
			if mErr != nil {
				rejectRequest(c, inbound, fmt.Errorf("脱敏失败, 拒绝放行明文: %w", mErr))
				return
			}
			raw.Body = masked
			maskMapping = mapping
			if maskMapping != nil {
				streamRestorer = mask.NewStreamRestorer(maskMapping)
				request.Masked = true
			}
			// 用脱敏后的请求体替换状态中的原始明文, 同时记录命中明细并发布状态,
			// 使日志/审计/对话留存只记录脱敏后内容, 日志详情实时收到命中信息(文档 07 §3.1)。
			request.applyMaskResult(string(masked), toMaskMatches(matches))
		}
		// 有会话键的映射跨请求保留(多轮同一占位符), 由 SessionStore TTL 回收;
		// 无会话键时 Apply 使用请求级 Mapping, 不入表, 请求结束即释放。
		// 请求级随机头值: 同一请求的所有动态头与所有重试复用同一值, 仅在首次真正发起上游前解析一次。
		// 解析按 (sessionKey, 渠道, Key) 命名空间, 命名空间隔离不同上游的会话; 空会话生成请求级独立 UUID。
		var requestRandomValue string
		requestRandomValueResolved := false
		exclude := 0                                   // 本请求已放弃的成员 ID, 重扫时跳过以免再次选中。
		refSkips := 0                                  // 本请求内结构性跳过的引用计数, 超过成员总数说明全部引用均不可用。
		nonRetryable := make(map[int]bool)             // 出现过确定性 4xx(400/404/422)的成员集合, 全部成员都出现时终止请求。
		rounds := 0                                    // 本请求已消耗的尝试轮次, 含引用链结构性跳过等一切循环路径。
		startedAt := time.Now()                        // 请求进入转发循环的时刻, 用于整体安全截止时间判定。
		relayConfig := model.DefaultGroupRelayConfig() // 最近一次成功读取的分组 Relay 配置, 分组暂不可得时以默认值兜底。
		earlyEofRetried := make(map[int]bool)          // 已享受过提前 EOF 免费重试的成员 ID: 每个成员每请求仅免记账重试一次。
		sanitizeRetried := make(map[int]bool)          // 已享受过 400 清洗重试的成员 ID: 每个成员每请求仅一次。
		allCooldownClears := 0                         // 全冷却自动清除并重试的累计次数, 用于线性退避间隔计算。
		var hops []refHop                              // 当轮引用链: 提升到循环外供 panic 兜底读取当轮占用, 每轮选路成功后重新赋值。
		var failedIdx int                              // 引用链解析失败跳下标(仅当轮有效), 与 hops 一起提升以便用普通赋值接收。
		var lifecycle *roundLifecycle                  // 当轮生命周期: 提升到循环外供 panic 兜底释放上游响应与并发槽位。

		// panic 兜底: gin 会 recover 该请求, 但当轮引用链若已持有探测候选占用或半开标记而不归还,
		// pickGroupItem 会因候选占用永久返回空、claimHalfOpenLocked 拒绝新半开, 整组钉死到重启。
		// 只做幂等的占用归还(不动紧急并发计数), 已定论轮次为无操作。
		defer func() {
			if r := recover(); r != nil {
				lifecycle.Stop()
				releaseRefChainProbeHolds(hops)
				panic(r)
			}
		}()

		for {
			if ctx.Err() != nil {
				request.markCanceled(ctx.Err(), "", nil)
				return
			}
			// 管理端 per-request 终止: stopRequested 在轮间等待中被 stopCh 唤醒后,
			// 循环回到此处立即以取消终态收尾, 不再发起新一轮尝试。
			if request.IsStopRequested() {
				request.markCanceled(errAdminStopped, "", nil)
				return
			}
			// 全局停止下, 在途请求立即以 503 失败收尾, 不再尝试任何成员也不消耗更多时间。
			if IsAllStopped() {
				request.markFailed(errAllRequestsStopped, "", nil)
				recordErrorLog(request)
				rejectRequest(c, inbound, errAllRequestsStopped)
				return
			}

			// 每请求全局尝试上限与整体安全截止时间: 引用链跳过、成员全冷却等待等所有循环路径
			// 都计入轮次并接受截止检查, 防止配置异常或上游持续脏数据把单个请求钉成无限循环。
			rounds++
			if limit := maxRequestRounds(relayConfig); rounds > limit {
				err := errors.New("请求尝试轮次超限")
				request.markFailed(err, "", nil)
				recordErrorLog(request)
				rejectRequest(c, inbound, errNoAvailableChannels)
				return
			}
			if requestDeadlineExceeded(relayConfig, startedAt) {
				err := errors.New("请求总时长超限")
				request.markFailed(err, "", nil)
				recordErrorLog(request)
				rejectRequest(c, inbound, errNoAvailableChannels)
				return
			}

			// 分组配置和成员随时可改, 故每轮重新读取; 分组被删除时等待它重新出现。
			group, err := op.GroupGetByName(metadataModel)
			if err != nil {
				if !request.wait(ctx, model.DefaultGroupRelayConfig().MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			relayConfig = group.RelayConfig

			// 会话粘合仅在故障转移模式生效, 分组配置随时可改故每轮重新判断; 先在顶层选出本层成员。
			item := model.GroupItem{}
			if sessionStickyEnabled(group, sessionKey) {
				// 粘合有效时本轮直接使用粘合成员, 失效则按优先级正常选路。
				item = pickSessionSticky(group, sessionKey)
			}
			if item.ID == 0 {
				// 手动模式取人工指定的成员, 故障转移模式按优先级选择未禁用且不在冷却中的成员;
				// 选路层会跳过被禁用渠道的成员, 派发处另有兜底检查覆盖粘合等旁路。
				// 没有目标时等待重新选择, 期间人工切换渠道, 补齐成员或成员冷却到期即可让请求继续。
				item = pickGroupItem(group, exclude, format)
			}
			if item.ID == 0 {
				// 全冷却自动清除: 分组配置了 AllCooldownRetryBaseSeconds 且所有非禁用成员都在冷却中时,
				// 清除全部冷却让 failover 依次重试每个成员, 而非空转到冷却自然到期。
				// 退避间隔线性递增(base, 2*base, 3*base, …), 上限 AllCooldownRetryMaxSeconds,
				// 防止上游持续故障时过于激进地清除重试。
				if base := group.RelayConfig.AllCooldownRetryBaseSeconds; base > 0 && allMembersInCooldown(group) {
					allCooldownClears++
					ResetGroupCooldown(group.ID)
					interval := base * allCooldownClears
					if max := group.RelayConfig.AllCooldownRetryMaxSeconds; max > 0 && interval > max {
						interval = max
					}
					if !request.wait(ctx, interval) {
						return
					}
					continue
				}
				// 本请求已无可选成员: 先清除引用跳过标记再等待, 让等待结束后的重扫
				// 能重新评估此前被结构性跳过的引用, 目标分组恢复后即可自动回流。
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 引用成员(ChannelID 为 0)逐层解析到最终叶子成员与其所在分组:
			// 每一层复用既有粘合/选路函数, 目标分组内部享受完整的三态熔断/冷却/半开语义。
			// 目标分组无可选成员、目标不存在或链路成环超限属于结构性不可用, 几秒的等待窗口内不会自愈:
			// 对齐 OmniRoute skipped_before_dispatch 的思想按本次请求内跳过处理, 不计失败连击、
			// 不给引用成员上冷却也不等待, 立即排除该引用改试顶层下一优先级。
			hops, failedIdx = resolveGroupRefChain(group, item, sessionKey, exclude)
			if failedIdx >= 0 {
				// 防热旋: 单个 exclude 变量记不住多个损坏的兄弟引用, 全部引用都结构性失效时
				// 会交替重选形成紧循环。跳过次数超过成员总数即视为整组不可用, 退避一轮后
				// 清空排除重新评估——等待窗口内目标分组的冷却可能到期恢复。
				refSkips++
				if refSkips > len(group.Items) {
					refSkips = 0
					exclude = 0
					// 超限退避前同样整链无结论归还: 失败链沿途可能持有紧急额度或探测占用,
					// 泄漏会累积耗尽紧急并发并虚增 SSE 汇总, 与下方非超限分支的释放语义一致。
					releaseRefChainHops(hops)
					if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
						return
					}
					continue
				}
				broken := hops[failedIdx]
				// 整链按无结论归还沿途探测占用与紧急额度, 含失败跳自身可能持有的占用
				// (原先由 recordRouteFailure 落账时归还, 现在不再走失败记账, 须在此一并释放)。
				releaseRefChainHops(hops)
				// 清除指向失效引用的会话粘合, 避免粘合绕过排除标记反复命中同一条死链;
				// 深层失效时顶层引用与失败跳不是同一成员, 其粘合一并清除才能推进到下一优先级。
				clearSessionStickyByItem(broken.group.ID, broken.item.ID)
				if broken.item.ID != item.ID {
					clearSessionStickyByItem(group.ID, item.ID)
				}
				// 排除本轮顶层引用成员(failedIdx 为 0 时即失败跳自身), 重扫直接落到顶层下一优先级;
				// 不留任何持久化惩罚, 下一个新请求会重新评估该引用, 目标分组恢复后自动回流。
				exclude = item.ID
				continue
			}
			// 解析成功: 路由记账切到叶子分组, 统计仍记最终叶子渠道与成员。
			group = hops[len(hops)-1].group
			item = hops[len(hops)-1].item
			refSkips = 0

			// 叶子成员经关联取得渠道模型; 关联缺失说明渠道或其模型刚被删除(缓存快照悬空),
			// 与成员指向已删渠道同等对待: 整链归还探测占用并清除粘合后等待重扫。
			channelModel := item.ChannelModel
			if channelModel == nil {
				releaseRefChainHops(hops)
				clearSessionStickyByItem(group.ID, item.ID)
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 成员指向的渠道已被删除时同样等待, 该成员可能很快被改回可用渠道;
			// 若本轮沿途持有探测占用, 先整链归还再等待。
			// 粘合成员的渠道被删除时其粘合仍然有效, 会把会话钉死到失效成员直至 TTL:
			// 清除指向该成员的全部粘合, 让后续请求重新选路到健康成员。
			// ChannelGetCore: 派发路径只需要渠道核心配置, 不必重建全量渠道模型列表。
			channel, err := op.ChannelGetCore(channelModel.ChannelID)
			if err != nil {
				releaseRefChainHops(hops)
				clearSessionStickyByItem(group.ID, item.ID)
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			// 渠道停用时整链视为不可用, 等待重扫而非继续消耗 token / 配额;
			// 停用渠道上的粘合与并发信号量应已被 CleanupChannelKeyState 释放。
			if !channel.Enabled {
				releaseRefChainHops(hops)
				clearSessionStickyByItem(group.ID, item.ID)
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 将分组成员配置的真实模型写入本轮上游请求。
			raw.Body, err = sjson.SetBytes(raw.Body, "model", channelModel.Name)
			if err != nil {
				releaseRefChainHops(hops)
				request.markFailed(err, "", nil)
				recordErrorLog(request)
				rejectRequest(c, inbound, err)
				return
			}
			// OpenAI Chat 流式响应需显式要求上游在末尾附带用量。
			if metadataStreaming && format == llm.APIFormatOpenAIChatCompletion {
				raw.Body, err = sjson.SetBytes(raw.Body, "stream_options.include_usage", true)
				if err != nil {
					releaseRefChainHops(hops)
					request.markFailed(err, "", nil)
					recordErrorLog(request)
					rejectRequest(c, inbound, err)
					return
				}
			}
			// 多轮 reasoning 回传: 客户端剥离历史 reasoning_content 时按缓存补齐,
			// 仅对 OpenAI Chat 协议与 OpenAI 系渠道生效; 必须在真实模型名写入后、发起上游前完成。
			raw.Body = injectMissingReasonings(raw.Body, format, channel.Type)

			// 单渠道多 Key: 发起上游前选出本轮使用的凭据并装配生效渠道副本。
			// 全部 Key 都在冷却中, 或代理模板按当前别名解析失败时, 按一轮真实失败记账,
			// 不发起上游请求; 渠道未配置多 Key 时回退旧 Key 字段走同一路径。
			keyIndex, selectedKey, keyOK := selectChannelKey(channel)
			effective := channel
			var dispatchErr error // 尚未发起上游调用即已确定的本轮失败原因。
			if !keyOK {
				dispatchErr = errAllChannelKeysCooling
			} else {
				// 业务路径轮询选 Key, 生效渠道构造与 probe/TestChannel 共用:
				// 写入该 Key 的明文并使用其 Account/ID 解析代理模板; Remark 仅展示不参与。
				effective, dispatchErr = effectiveChannelForKey(channel, selectedKey)
			}

			if dispatchErr != nil {
				// 客户端已取消时不再落账失败, 直接以取消终态结束请求。
				if ctx.Err() != nil {
					releaseRefChainHops(hops)
					request.markCanceled(ctx.Err(), "", nil)
					return
				}
				roundCtx, cancelRound := context.WithCancelCause(ctx)
				lifecycle = newRoundLifecycle(cancelRound)
				request.startRound(lifecycle.Stop, RoundTarget{
					MemberID:     item.ID,
					ChannelID:    channel.ID,
					ChannelName:  channel.Name,
					Model:        channelModel.Name,
					KeyLabel:     channelKeyLabel(keyIndex, selectedKey),
					ClientFormat: clientFormatLabel(format),
					UpstreamType: upstreamTypeLabel(channel.Type),
					ProxyAddr:    roundProxyLabel(channel, effective),
				})
				request.finishRound(AttemptFailed, classifyRound(dispatchErr, ctx, roundCtx), dispatchErr.Error())
				request.releaseRoundLifecycle()
				// 叶子成员照常计入失败; 祖先引用跳不因下游成员失败背锅, 无结论整链归还探测占用。
				releaseRefChainHops(hops[:len(hops)-1])
				// 失败计数的唯一入口: 业务与基础设施错误由 memberFailureCounts 独立累计,
				// 每个错误只计一次, 成员改变时同时重置两类计数。
				if recordRouteFailureIfReal(group, item.ID, failureCounts, dispatchErr) {
					clearSessionStickyByItem(group.ID, item.ID)
					exclude = item.ID
					continue
				}
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 渠道级单 Key RPM 门禁: 选定 Key 之后、开始本轮之前按滑动窗口放行;
			// 尚未 startRound 故这段等待不计入尝试轨迹(预期行为)。回退旧 Key 字段时
			// selectedKey.ID 为空串, 作为该渠道单 Key 的稳定引用。等待期间客户端断开
			// 或管理端终止请求时以取消终态定稿, 不发起上游请求。
			if err := waitChannelRPM(ctx, channel.ID, selectedKey.ID, effective.RateLimitRPM, request.stopCh); err != nil {
				// 等待期间取消同样按无结论整链归还占用: 叶子可能是半开恢复候选或紧急成员,
				// 不归还则 ProbeItemID 滞留, pickGroupItem 永久返回空, 整组钉死到重启。
				releaseRefChainHops(hops)
				if request.IsStopRequested() {
					request.markCanceled(errAdminStopped, "", nil)
				} else {
					request.markCanceled(ctx.Err(), "", nil)
				}
				return
			}

			// 为本轮上游调用建立独立取消入口并登记当前目标。
			// 带原因的取消用于区分成员级响应超时与人工中止: 人工中止原因为 context.Canceled, 超时为 errMemberResponseTimeout。
			roundCtx, cancelRound := context.WithCancelCause(ctx)
			lifecycle = newRoundLifecycle(cancelRound)
			modelLimit, _ := lookupModelLimit(channel.ModelLimits, channelModel.Name)
			request.startRound(lifecycle.Stop, RoundTarget{
				MemberID:      item.ID,
				ChannelID:     channel.ID,
				ChannelName:   channel.Name,
				Model:         channelModel.Name,
				KeyLabel:      channelKeyLabel(keyIndex, selectedKey),
				ThinkingLevel: modelLimit.ThinkingLevel,
				ClientFormat:  clientFormatLabel(format),
				UpstreamType:  upstreamTypeLabel(channel.Type),
				Passthrough:   supportsNativeFormat(channel, format),
				ProxyAddr:     roundProxyLabel(channel, effective),
			})
			// 成员级响应超时: 流式只约束等待首个有效事件的阶段, 非流式约束等待完整响应的阶段。
			timeoutSeconds := memberTimeoutSeconds(group.RelayConfig, metadataStreaming)
			stopRoundTimeout := armRoundTimeout(cancelRound, timeoutSeconds)

			// 按渠道协议构造出站转换器并确定是否可以直接透传。
			outbound, passthrough, err := buildOutbound(effective, format)

			// 请求上游并等待首个有效响应: 非流式等待完整响应, 流式等待首个事件。
			// 同协议渠道原样直通, 跨协议渠道经转换后请求; 此时尚未写给客户端, 失败仍可换目标重试。
			// 请求级随机头值在首次发起上游前一次性解析, 之后所有重试复用同一值, 不在每次重试重新生成。
			if !requestRandomValueResolved {
				requestRandomValue = resolveRequestRandomValue(sessionKey, channel.ID, selectedKey.ID)
				requestRandomValueResolved = true
			}
			var result *upstreamResponse
			if err == nil {
				// 客户端与渠道协议一致时直接透传, 其余组合通过 pipeline 转换。
				if passthrough {
					result, err = sendPassthrough(roundCtx, format, raw, effective, outbound, metadataStreaming, requestRandomValue)
				} else {
					result, err = sendConverted(roundCtx, format, raw, effective, outbound, metadataStreaming, requestRandomValue)
				}
			}
			if result != nil {
				lifecycle.Attach(result)
			}
			// 首个有效响应已经取得(或本轮已失败), 立即停止超时计时器, 后续长流转发不再受本轮超时约束。
			stopRoundTimeout()
			// 计时器与首事件到达存在竞态: 已取得流式首事件但本轮刚被超时中止时按超时失败处理,
			// 避免在已取消的上下文上续流导致转发立即中断且错误被误判。
			// 此时 result.events 已经打开, 必须先关闭再按失败处理, 否则上游连接随轮次泄漏。
			if err == nil && metadataStreaming && roundCtx.Err() != nil {
				result.Close()
				err = memberTimeoutError(metadataStreaming, timeoutSeconds)
			}

			if err != nil {
				// 本地计时器触发的超时会以上下文取消的形式浮出, 还原为明确的超时失败再进入统一处理。
				if errors.Is(context.Cause(roundCtx), errMemberResponseTimeout) {
					err = memberTimeoutError(metadataStreaming, timeoutSeconds)
				}
				// 归类本轮结束原因并回填尝试轨迹: 客户端取消与人工中止不计为轮次失败, 也不向面板展示为错误。
				outcome := AttemptFailed
				switch {
				case ctx.Err() != nil:
					outcome = AttemptCanceled
				case roundCtx.Err() != nil && !errors.Is(context.Cause(roundCtx), errMemberResponseTimeout):
					outcome = AttemptCanceled
				}
				brief := ""
				if outcome == AttemptFailed {
					brief = err.Error()
				}
				// 多 Key 认证/限额拒绝轮换: 上游明确拒绝当前凭据(401/403)或标记其限速配额(429)
				// 且该渠道配置了多把 Key 时, 仅把本轮使用的 Key 标记冷却(时长取分组
				// MemberCooldownSeconds), 不计渠道失败、不等待, 立即重选同一成员的下一把 Key。
				// 429 的用量限额对单把 Key 是持续性不可用, 不轮换会反复重试同一把耗尽的 Key。
				// 旧式单 Key 与仅剩一把的渠道不走此路径, 照常进入下方失败计数与冷却处理。
				if outcome == AttemptFailed && len(channel.Keys) > 1 && (isAuthRejectionError(err) || isKeyRateLimitError(err)) {
					markChannelKeyCooldown(channel.ID, selectedKey.ID, group.RelayConfig.MemberCooldownSeconds)
					request.finishRound(AttemptFailed, classifyUpstreamStatus(err), brief)
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops)
					continue
				}
				request.finishRound(outcome, classifyRound(err, ctx, roundCtx), brief)
				// 先按上游返回时的原始上下文状态分类，再释放本轮生命周期。
				// releaseRoundLifecycle 会主动 cancel roundCtx；若在分类前调用，会把所有真实业务失败误判成人工中止。
				// 管理端整体终止: 立即以取消终态收尾, 不再重选目标。
				if request.IsStopRequested() {
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops)
					request.markCanceled(errAdminStopped, "", nil)
					return
				}
				// 父上下文结束说明客户端已经取消, 整链归还探测占用并以取消终态结束请求。
				if ctx.Err() != nil {
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops)
					request.markCanceled(ctx.Err(), "", nil)
					return
				}
				// 仅本轮上下文被人工中止时不计失败也不等待, 立即重新选择目标;
				// 成员级响应超时属于真实上游故障, 不在此列, 继续按失败计数与冷却处理。
				if roundCtx.Err() != nil && !errors.Is(context.Cause(roundCtx), errMemberResponseTimeout) {
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops)
					continue
				}
				// 上游在任何内容到达前提前关闭流(提前 EOF): 多为瞬时抖动, 首次发生时对同成员
				// 给予一次免记账的立即重试——旁路 failures 计数, 不记路由失败也不计渠道故障;
				// 第二次仍提前 EOF 才落入下方正常失败处理。成员每请求仅享受一次免费重试。
				if errors.Is(err, errStreamEarlyEof) && !earlyEofRetried[item.ID] {
					earlyEofRetried[item.ID] = true
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops)
					continue
				}
				// 上游 400: 可能因请求体包含上游不兼容的非标准字段(如 stop 字符串、developer 角色)。
				// 每个成员每请求享受一次清洗重试, 清洗后仍失败则正常走失败路径。
				if code, ok := UpstreamStatusCode(err); ok && code == http.StatusBadRequest && !sanitizeRetried[item.ID] {
					sanitizeRetried[item.ID] = true
					sanitizeRequestBody(raw)
					request.releaseRoundLifecycle()
					// 整链无结论释放(含叶子): 清洗重试不走失败记账, 叶子若是探测恢复候选,
					// 其 ProbeItemID/HalfOpen 占用若不释放, pickGroupItem 会因候选占用永久返回空,
					// 整组请求钉死到重启。与提前 EOF 免费重试路径的释放语义保持一致。
					releaseRefChainHops(hops)
					continue
				}
				request.releaseRoundLifecycle()
				// 确定性请求错误(400/404/422): 重试同一成员或等待冷却都不可能改变结果。
				// 该成员照常计入失败并武装冷却, 立即换下一优先级; 当全部启用成员都各失败
				// 一次后终止请求, 把明确的错误交还下游——避免无意义的冷却-探测循环烧掉
				// 轮次与时长预算(实测 Codex developer 角色被上游拒绝后空转 30 轮的案例)。
				if code, ok := UpstreamStatusCode(err); ok && (code == http.StatusBadRequest || code == http.StatusNotFound || code == http.StatusUnprocessableEntity) {
					request.releaseRoundLifecycle()
					releaseRefChainHops(hops[:len(hops)-1])
					// 同一成员反复出现确定性 4xx 只记一次, 集合按成员去重后与成员总数比较。
					recordRouteFailureIfReal(group, item.ID, failureCounts, err)
					clearSessionStickyByItem(group.ID, item.ID)
					nonRetryable[item.ID] = true
					if len(nonRetryable) >= len(group.Items) {
						request.markFailed(err, "", nil)
						recordErrorLog(request)
						rejectRequest(c, inbound, errNoAvailableChannels)
						return
					}
					exclude = item.ID
					continue
				}
				request.releaseRoundLifecycle()
				// 叶子成员照常计入失败; 祖先引用跳不因下游成员失败背锅, 无结论整链归还探测占用。
				releaseRefChainHops(hops[:len(hops)-1])
				// 失败计数的唯一入口: 每次错误只累计一次, 业务/基础设施类型及成员切换互不串线。
				// 达到各自阈值或候选确认失败时立即重新选路, 否则等待后重试当前成员。
				if recordRouteFailureIfReal(group, item.ID, failureCounts, err) {
					// 粘合成员已进入冷却, 清除指向它的全部会话粘合; 本请求重扫时不再复用该成员。
					clearSessionStickyByItem(group.ID, item.ID)
					exclude = item.ID
					continue
				}
				exclude = 0
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			// 记录本轮已经取得可提交的上游响应。
			request.finishRound(AttemptSuccess, "", "")
			// 上游成功后沿途各层解除冷却与探测占用, 并按各自路由配置处理亲和:
			// 引用跳被业务确认说明它确能产出可用下游, 叶子层独立完成自己的候选确认与恢复。
			for _, hop := range hops {
				recordRouteSuccess(hop.group, hop.item.ID)
			}
			// 业务成功后沿途每层建立或滑动续期会话粘合(顶层粘到引用项, 叶子组粘到叶子项),
			// 同一会话在链路任一分组的粘合有效期内都稳定命中同一条完整链路。
			for _, hop := range hops {
				if sessionStickyEnabled(hop.group, sessionKey) {
					bindSessionSticky(hop.group, sessionKey, hop.item.ID)
				}
			}
			// 同协议透传时原样返回上游响应头; 跨协议响应没有需要透传的响应头。
			// 流式分支须剔除 Content-Length 类定长分帧头: 上游 SSE 若误带定长声明,
			// 原样复制会与逐事件写出(以及污染流追加合成终止帧)的分帧冲突, 破坏客户端解析;
			// Connection 等其余逐跳头由 HTTP 库层处理, 这里只补长度类剔除。
			copyUpstreamHeaders(c.Writer.Header(), result.header, metadataStreaming)

			// 非流式响应已经完整取得, 提交后一次写给客户端。
			if !metadataStreaming {
				result.Close()
				request.releaseRoundLifecycle()
				if c.Writer.Header().Get("Content-Type") == "" {
					c.Header("Content-Type", "application/json")
				}
				// 上游已成功产出响应, 从中采集 tool_call.id -> reasoning_content 映射供多轮回放;
				// 解析失败静默忽略, 不影响本次交付。
				if format == llm.APIFormatOpenAIChatCompletion {
					collectReasonings(result.body)
				}
				request.markCommitted()
				// 脱敏还原: 非流式响应占位符→原文(文档 01 §3.1)。
				result.body = restoreNonStream(result.body, maskMapping)
				n, err := c.Writer.Write(result.body)
				if err == nil && n != len(result.body) {
					err = io.ErrShortWrite
				}
				if err != nil {
					if ctx.Err() != nil {
						request.markCanceled(ctx.Err(), string(result.body), result.usage)
					} else {
						request.markFailed(err, string(result.body), result.usage)
						recordErrorLog(request)
					}
					return
				}
				request.markSucceeded(string(result.body), result.usage)
				return
			}

			// 首帧提交后无法再整轮重试, 转发期逐事件校验终止原因白名单:
			// 异常终止块被抑制并以干净终止帧收尾(见下方污染处理), 其余事件直接转发至上游结束。
			if c.Writer.Header().Get("Content-Type") == "" {
				c.Header("Content-Type", "text/event-stream")
			}
			var encoded bytes.Buffer
			var chunks []*httpclient.StreamEvent
			committed := false                // 已向客户端写出至少一个事件。
			terminalSeen := result.terminated // 有效性窗口内已出现协议终止事件。
			clientGone := false               // 因客户端写失败退出转发, 客户端已不可达, 无需补发终止帧。
			polluted := false                 // 上游下发了携带异常终止原因的块: 该块被抑制, 流以合成终止帧收尾并按失败记账。
			var polluteErr error              // 首个污染块的异常原因, 用于请求终态留档。
			noAnswerStop := false             // 思考-only 自然终止已命中: 终止块及其后尾帧不再写给客户端。
			roundAnswered := false            // 整轮是否出现过最终回答信号(文本增量/工具调用)。
			roundReasoned := false            // 整轮是否出现过推理内容信号(reasoning_content)。
			roundFinished := false            // 整轮是否出现过白名单内的非空终止原因(OpenAI Chat 的 finish_reason / Anthropic 的 message_delta.stop_reason)。
			window := result.window
			// 初始化必须为 false: 窗口内出现终止事件时(无内容流), 窗口可能携带多条事件
			// (结构帧 + 终止帧 + usage 尾帧), 若以 terminated 起步会在写完第一帧后提前 break,
			// 丢弃剩余全部帧; 循环内的 analyzeStreamEvent 会在真正终止帧上把 last 置真并收尾。
			last := false // 已转发的最后一个事件是否按客户端协议结束了整个响应流。
			event := (*httpclient.StreamEvent)(nil)
			// firstErr 保存转发期首个真实失败(流语义错误/编码错误/写错误/读错误/空闲超时/预算超限),
			// 不被后续 sse.Encode 的 nil 返回或写成功覆盖。原实现把 inspectErr 写入复用的 err,
			// 紧随其后的 err = sse.Encode(...) 会把流内错误帧的 inspectErr 覆盖为 nil, 导致已交付
			// 错误帧的流被误判为成功。firstErr 与外层 err(此处必为 nil)分离, 终态判定只看 firstErr。
			var firstErr error
			// 流空闲超时: 首有效内容后逐事件读取阶段, 相邻事件间隔超过配置即以明确终态终止。
			// 计时器只覆盖阻塞的 result.events.Next(), 事件到达即重置; 不设全局 WriteTimeout,
			// 只要事件持续到达就不会触发, 正常长 SSE 不受影响。
			idleSeconds := streamIdleSeconds(relayConfig)
			var idleTimer *time.Timer
			armIdle := func() {
				if idleSeconds <= 0 {
					return
				}
				if idleTimer != nil {
					idleTimer.Stop()
				}
				idleTimer = time.AfterFunc(time.Duration(idleSeconds)*time.Second, func() {
					cancelRound(errStreamIdleTimeout)
				})
			}
			disarmIdle := func() {
				if idleTimer != nil {
					idleTimer.Stop()
					idleTimer = nil
				}
			}
			// 累计事件/字节预算: 防止异常上游用无限事件流耗尽 relay 内存。0 表示不限。
			maxStreamBytes := streamMaxBytes(relayConfig)
			maxStreamEvents := streamMaxEvents(relayConfig)
			var totalStreamBytes int
			var totalStreamEvents int
			for {
				// 先写出有效性窗口内缓冲的事件, 再实时读取剩余事件。
				if len(window) > 0 {
					event, window = window[0], window[1:]
				} else {
					if last {
						break
					}
					// 阻塞读上游下一事件前武装空闲超时, 读到立即解除; 计时器触发会取消
					// roundCtx 使 Next 返回 false, 循环退出后由下方还原为 errStreamIdleTimeout。
					armIdle()
					hasNext := result.events.Next()
					disarmIdle()
					if !hasNext {
						break
					}
					event = result.events.Current()
				}
				if event == nil {
					continue
				}
				// 累计预算检查: 超限即以明确终态终止, 已交付内容不重发, 静默截断收尾。
				totalStreamEvents++
				totalStreamBytes += len(event.Data)
				if (maxStreamBytes > 0 && totalStreamBytes > maxStreamBytes) || (maxStreamEvents > 0 && totalStreamEvents > maxStreamEvents) {
					if firstErr == nil {
						firstErr = fmt.Errorf("%w: %d events / %d bytes", errStreamBudgetExceeded, totalStreamEvents, totalStreamBytes)
					}
					break
				}
				// 流已被污染: 后续事件不再写给客户端, 仅保留在聚合序列中以留存已产生的用量计量。
				if polluted {
					chunks = append(chunks, event)
					continue
				}
				// 已提交的响应不能再换目标重试: 协议终态事件原样转发后立即结束转发,
				// 不再阻塞等待上游关闭连接(部分网关发完 [DONE]/message_stop 仍不关体,
				// 迟到的 context.Canceled 会把已完整交付的响应误判为取消, 见上游 #349);
				// 结束事件自身携带的失败也原样交付并作为本请求终态。
				// 单次解码取得全部判定(终止/错误/异常终止原因/回答与推理信号), 长流下避免同一事件反复解析。
				verdict := analyzeStreamEvent(format, event)
				roundAnswered = roundAnswered || verdict.hasAnswer
				roundReasoned = roundReasoned || verdict.hasReasoning
				roundFinished = roundFinished || verdict.finishSeen
				if verdict.inspectErr != nil || verdict.terminal {
					last = true
					terminalSeen = true
					// 流语义错误(坏 JSON/error 帧/类型错乱)记入 firstErr 且不被后续编码/写出的 nil 覆盖;
					// 事件本身仍原样转发(错误帧应到达下游), last 已置真使循环在写完该帧后退出。
					if firstErr == nil {
						firstErr = verdict.inspectErr
					}
				}
				// 携带异常终止原因的块(如 "finish_reason":"network_error")不得到达下游:
				// 抑制该块并置流污染标志, 流结束后由下方合成的干净协议终止帧规范收尾。
				if verdict.abnormalErr != nil {
					polluted = true
					polluteErr = verdict.abnormalErr
					chunks = append(chunks, event)
					continue
				}
				// 思考-only 自然终止: 上游以 stop 收尾但整轮只有推理内容、没有任何最终回答
				// (文本/工具调用)。思考型模型的推理与回答共享输出预算, 思考阶段耗尽预算后
				// 部分上游按正常 stop 交付这类轮次; 客户端拿到"成功结束的空回答"只能整轮判失败。
				// 终止块与其后的尾帧(usage/[DONE])不再写给客户端, 仍保留在聚合序列中留存用量;
				// 流结束后由 frameFailure 分支合成协议内错误帧, 下游 SDK 感知失败并自动重试。
				// 仅 OpenAI Chat 事件会置 stopFinish, 其余客户端协议不受影响。
				if !polluted && verdict.stopFinish && roundReasoned && !roundAnswered {
					noAnswerStop = true
				}
				if noAnswerStop {
					chunks = append(chunks, event)
					if last {
						break
					}
					continue
				}
				// 脱敏还原: SSE 事件增量文本占位符→原文, StreamRestorer 跨 chunk 缓冲拼接(文档 01 §3.2、03)。
				if streamRestorer != nil {
					event.Data = restoreStreamEvent(event.Data, format, streamRestorer)
				}
				chunks = append(chunks, event)
				encoded.Reset()
				// 编码错误独立判定, 不覆盖 firstErr(流语义错误优先); 编码失败属硬错误, 直接退出。
				if encErr := sse.Encode(&encoded, sse.Event{Id: event.LastEventID, Event: event.Type, Data: event.Data}); encErr != nil {
					if firstErr == nil {
						firstErr = encErr
					}
					break
				}
				if !committed {
					request.markCommitted()
					committed = true
				}
				n, writeErr := c.Writer.Write(encoded.Bytes())
				if writeErr == nil && n != encoded.Len() {
					writeErr = io.ErrShortWrite
				}
				if writeErr != nil {
					// 客户端写失败: 客户端已不可达, 保留首个真实失败, 不再补发终止帧。
					if firstErr == nil {
						firstErr = writeErr
					}
					clientGone = true
					break
				}
				c.Writer.Flush()
				// 协议终态已交付: 立即结束转发, 不再阻塞等待上游关闭连接。
				if last {
					break
				}
			}
			disarmIdle()
			// 脱敏流式还原: 流结束后 Flush 残留 pending, 作为最终 delta 事件写给客户端。
			if streamRestorer != nil {
				if flushData := flushStreamRestorer(streamRestorer, format); flushData != nil {
					encoded.Reset()
					if sse.Encode(&encoded, sse.Event{Data: flushData}) == nil {
						c.Writer.Write(encoded.Bytes())
						c.Writer.Flush()
					}
				}
			}
			// 流空闲超时以 roundCtx 取消形式浮出: 优先还原为明确错误, 不被误判为客户端取消或吞成成功。
			if firstErr == nil && roundCtx.Err() != nil && errors.Is(context.Cause(roundCtx), errStreamIdleTimeout) {
				firstErr = errStreamIdleTimeout
			}
			// 流读取器错误作为兜底: 仅当尚无更早的真实失败时采纳, 保留首个失败语义。
			if firstErr == nil {
				if streamErr := result.events.Err(); streamErr != nil {
					firstErr = streamErr
				}
			}
			result.Close()
			request.releaseRoundLifecycle()
			// 使用客户端协议转换器聚合已转发事件, 统一取得最终响应正文和用量。
			responseBody, meta, aggregateErr := inbound.AggregateStreamChunks(context.WithoutCancel(ctx), chunks)
			if aggregateErr == nil {
				result.usage = meta.Usage
			}
			// 流式成功交付时同样采集 reasoning 回放缓存, 与非流式共用同一解析入口;
			// 聚合失败或污染流的失败轮次不采集。
			if firstErr == nil && !polluted && aggregateErr == nil && format == llm.APIFormatOpenAIChatCompletion {
				collectReasonings(responseBody)
			}
			// 失败哨兵仅用于服务端终态记账/错误日志分类, 不再转化为发给客户端的错误帧。
			frameFailure := firstErr
			if polluted {
				frameFailure = nil
			} else if noAnswerStop && firstErr == nil {
				// 思考-only 自然终止: 客户端已收到推理增量却没有终止帧, 按流失败收尾,
				// 而不是把空回答当作正常完成交付。
				frameFailure = errNoAnswerStop
			} else if firstErr == nil && !terminalSeen {
				if roundFinished {
					// 上游已发白名单内的非空终止原因(OpenAI Chat 的 finish_reason /
					// Anthropic 的 message_delta.stop_reason)但不发终止哨兵([DONE] /
					// message_stop)即关流(如 MiniMax / 部分 Anthropic 兼容第三方代理):
					// 生成已完整结束, 不判截断; terminalSeen 保持 false, 由下方补发
					// 合成的正常终止帧让客户端 SDK 规范收尾, 按成功定稿。
				} else {
					// 上游在协议终止帧之前干净关闭连接(EOF 无错误): 与读错误型中断同样按失败定稿,
					// 否则截断的答案会以正常 stop/end_turn 收尾, 客户端 SDK 把残缺内容当作完成。
					frameFailure = errStreamEarlyClose
				}
			}
			// 聚合失败纳入终态判定: 已交付的流不构成合法完整响应时按失败定稿, 但不重发任何内容
			// (流已 committed, 静默截断收尾)。仅在流表面正常(无 firstErr/无污染/非 noAnswerStop)时采纳,
			// 避免与更早的真实失败重复记账。原实现未把 aggregateErr 纳入失败条件, 聚合失败的流被误判成功。
			if frameFailure == nil && !polluted && !noAnswerStop && aggregateErr != nil {
				frameFailure = fmt.Errorf("aggregate stream chunks: %w", aggregateErr)
			}
			// 提交后的流失败一律静默截断: 不补发任何终止帧, 直接结束响应让 SSE 流缺协议终止事件。
			// 原方案合成协议内错误帧, 但实测 HTTP 层自动重试只覆盖首字节之前, Codex/opencode 等
			// Agent CLI 对 200 + 已开始流式之后的流内 error 帧只做异常展示、不会自动重试整个请求;
			// 而对「SSE 流缺终止事件即断开」它们有自动重连重试(stream disconnected before
			// completion), 静默截断恰好命中该路径, 客户端感知失败后整体重试。服务端仍按失败
			// 终态记账、落错误日志、累计成员连击, 可观测性不受影响。
			// 污染流与缺 [DONE] 哨兵的完整流(frameFailure == nil)不受影响, 仍合成正常终止帧规范收尾。
			if committed && !clientGone && ctx.Err() == nil && frameFailure == nil && (!terminalSeen || polluted) {
				for _, frame := range terminalStreamFrames(format) {
					encoded.Reset()
					if sse.Encode(&encoded, sse.Event{Id: frame.LastEventID, Event: frame.Type, Data: frame.Data}) != nil {
						break
					}
					if _, werr := c.Writer.Write(encoded.Bytes()); werr != nil {
						break
					}
					c.Writer.Flush()
				}
			}
			if firstErr != nil || polluted || frameFailure != nil {
				// 管理端整体终止与客户端断开同等豁免: 不计失败/连击/渠道故障。
				if request.IsStopRequested() {
					releaseRefChainHops(hops)
					request.markCanceled(errAdminStopped, string(responseBody), result.usage)
					return
				}
				// 污染流沿用 markFailed 分支语义定稿: 客户端已取消仍记取消, 否则记失败并保留聚合出的计量。
				// 提前干净关闭(frameFailure 哨兵)同样按失败定稿。
				finalErr := firstErr
				if finalErr == nil && polluted {
					finalErr = polluteErr
				}
				if finalErr == nil {
					finalErr = frameFailure
				}
				// 提交后的失败无法在本请求内重试, 但要跨请求累积成员健康度:
				// 连败达阈值的成员进入冷却, 后续请求自动故障转移, 不再反复命中同一坏渠道。
				// 进入冷却时清除指向它的会话粘合, 与提交前失败路径的语义保持一致。
				// 客户端断开(写失败)与客户端取消同等豁免: 成员并无过错, 不计提交后连击。
				if ctx.Err() == nil && !clientGone && recordPostCommitFailure(group, item.ID) {
					clearSessionStickyByItem(group.ID, item.ID)
				}
				if ctx.Err() != nil {
					request.markCanceled(ctx.Err(), string(responseBody), result.usage)
				} else {
					request.markFailed(finalErr, string(responseBody), result.usage)
					recordErrorLog(request)
				}
				return
			}
			request.markSucceeded(string(responseBody), result.usage)
			return
		}
	}
}

// errMemberResponseTimeout 是本轮上下文被成员级响应超时计时器中止时的取消原因,
// 用于把超时与客户端取消(context.Canceled)及人工中止区分开。
var errMemberResponseTimeout = errors.New("member response timeout")

// maxRequestRounds 返回单个请求允许消耗的最大尝试轮次, 配置缺失或非法时回退默认配置,
// 与 NormalizeGroupRelayConfig 形成双保险。
func maxRequestRounds(config model.GroupRelayConfig) int {
	if config.MaxRequestRounds > 0 {
		return config.MaxRequestRounds
	}
	return model.DefaultGroupRelayConfig().MaxRequestRounds
}

// requestDeadlineExceeded 判定请求是否已超过配置的整体时长上限:
// 0 表示不限时, 负值视为配置非法回退默认上限。
func requestDeadlineExceeded(config model.GroupRelayConfig, startedAt time.Time) bool {
	seconds := config.MaxRequestSeconds
	if seconds == 0 {
		return false
	}
	if seconds < 0 {
		seconds = model.DefaultGroupRelayConfig().MaxRequestSeconds
	}
	return time.Since(startedAt) >= time.Duration(seconds)*time.Second
}

// memberTimeoutSeconds 按请求类型返回成员级响应超时秒数: 流式取首个事件超时, 非流式取完整响应超时。
// 配置缺失或非法时回退默认配置, 与 NormalizeGroupRelayConfig 形成双保险。
func memberTimeoutSeconds(config model.GroupRelayConfig, streaming bool) int {
	if streaming {
		if config.MemberStreamFirstEventTimeoutSeconds > 0 {
			return config.MemberStreamFirstEventTimeoutSeconds
		}
		return model.DefaultGroupRelayConfig().MemberStreamFirstEventTimeoutSeconds
	}
	if config.MemberNonStreamResponseTimeoutSeconds > 0 {
		return config.MemberNonStreamResponseTimeoutSeconds
	}
	return model.DefaultGroupRelayConfig().MemberNonStreamResponseTimeoutSeconds
}

// streamIdleSeconds 返回流式转发期相邻事件间的空闲超时秒数, 0 表示不限时(维持既有长流语义)。
// 负值视为配置非法回退默认配置, 与 NormalizeGroupRelayConfig 形成双保险。
// 与首事件超时(MemberStreamFirstEventTimeoutSeconds)相互独立: 首事件超时只覆盖首个事件到达,
// 空闲超时覆盖首有效内容后逐事件转发阶段的相邻事件间隔, 两者分别武装/解除, 互不干扰。
func streamIdleSeconds(config model.GroupRelayConfig) int {
	if config.MemberStreamIdleTimeoutSeconds > 0 {
		return config.MemberStreamIdleTimeoutSeconds
	}
	if config.MemberStreamIdleTimeoutSeconds == 0 {
		return 0
	}
	return model.DefaultGroupRelayConfig().MemberStreamIdleTimeoutSeconds
}

// streamMaxBytes 返回单次流式转发累计事件字节数上限, 0 表示不限。
// 负值视为配置非法回退默认配置, 与 NormalizeGroupRelayConfig 形成双保险。
func streamMaxBytes(config model.GroupRelayConfig) int {
	if config.MemberStreamMaxBytes >= 0 {
		return config.MemberStreamMaxBytes
	}
	return model.DefaultGroupRelayConfig().MemberStreamMaxBytes
}

// streamMaxEvents 返回单次流式转发累计事件数上限, 0 表示不限。
// 负值视为配置非法回退默认配置, 与 NormalizeGroupRelayConfig 形成双保险。
func streamMaxEvents(config model.GroupRelayConfig) int {
	if config.MemberStreamMaxEvents >= 0 {
		return config.MemberStreamMaxEvents
	}
	return model.DefaultGroupRelayConfig().MemberStreamMaxEvents
}

// memberTimeoutError 构造面向状态展示的超时失败原因。
func memberTimeoutError(streaming bool, seconds int) error {
	if streaming {
		return fmt.Errorf("等待成员流式首个事件超时(%d 秒)", seconds)
	}
	return fmt.Errorf("等待成员非流式完整响应超时(%d 秒)", seconds)
}

// armRoundTimeout 为本轮启动成员级响应超时计时器, 到点以 errMemberResponseTimeout 为原因中止本轮上游调用。
// 流式响应的转发阶段绑定在同一上下文上且无法在取得首事件后更换,
// 故采用定时取消而非带截止时间的派生上下文: 调用方在取得首个有效响应后必须立即调用返回的停止函数,
// 计时器停止后本轮上下文恢复为纯取消语义, 长流不会被中途掐断。
func armRoundTimeout(cancelRound context.CancelCauseFunc, seconds int) (stop func()) {
	timer := time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		cancelRound(errMemberResponseTimeout)
	})
	// Stop 返回 false 说明计时器已触发但回调尚未(或刚刚)执行完毕:
	// 显式补写超时原因, 保证"响应到达与超时到期"竞态下取消原因不会丢失。
	return func() {
		if !timer.Stop() {
			cancelRound(errMemberResponseTimeout)
		}
	}
}

// copyUpstreamHeaders 复制上游响应头到客户端响应。
// 流式分支剔除 Content-Length 类定长分帧头(大小写不敏感), 防止上游误带的定长声明
// 破坏逐事件分帧与合成终止帧追加; 非流式分支原样保留全部响应头。
func copyUpstreamHeaders(dst, src http.Header, streaming bool) {
	for key, values := range src {
		if streaming && http.CanonicalHeaderKey(key) == "Content-Length" {
			continue
		}
		dst[key] = values
	}
}

// errNoAvailableChannels 面向下游的统一终态错误消息:
// 全部成员都不可用而终止的请求, 客户端只看到该文案; 详细原因仅保留在内部状态与日志。
var errNoAvailableChannels = errors.New("暂无可用渠道")

// errAllRequestsStopped 在管理端「一键停止所有请求」置位期间作为入口与在途的统一终止原因;
// 使用与 errNoAvailableChannels 相同的 rejectRequest 路径返回 503, 避免对客户端协议层的额外协议变更。
var errAllRequestsStopped = errors.New("服务器过载，请稍候")

// roundProxyLabel 返回本轮出口代理地址的展示形式(经 client.MaskProxySecret 打码密码段,
// 保留用户名中 {account} 解析出的别名): 优先取解析后的生效代理, 模板解析失败时回退原始
// 模板(打码函数对其显示占位符, 恰好提示地址非法)。
// 渠道未启用代理返回空串(直连无代理可展示); 启用但未配置渠道专属地址时出站走系统代理,
// 此处标注系统代理地址, 否则这类请求在面板上完全看不出是否走了代理、走的哪一个。
func roundProxyLabel(channel, effective model.Channel) string {
	if !channel.Proxy {
		return ""
	}
	addr := effective.ChannelProxy
	if addr == nil {
		addr = channel.ChannelProxy
	}
	if addr == nil || strings.TrimSpace(*addr) == "" {
		systemProxy := systemProxyLookup()
		if systemProxy == "" {
			return systemProxyUnsetLabel
		}
		return systemProxyLabelPrefix + client.MaskProxySecret(systemProxy)
	}
	return client.MaskProxySecret(*addr)
}

// systemProxyLabelPrefix 标记该轮走的是系统代理而非渠道专属代理。
const systemProxyLabelPrefix = "系统代理 "

// systemProxyUnsetLabel 渠道开启了代理但系统代理地址为空(此时出站必然失败)的展示文案。
const systemProxyUnsetLabel = "系统代理(未配置地址)"

// systemProxyLookup 返回当前系统代理地址(已去空白), 未配置时为空串; 测试可整体替换。
var systemProxyLookup = func() string {
	systemProxy, err := op.SettingGetString(model.SettingKeyProxyURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(systemProxy)
}

// recordErrorLog 把已定稿的失败请求投递到错误日志内存队列, 由 op 层后台协程攒批落库。
// 必须在 markFailed 返回之后调用: 此处仅读取已定稿且不再变更的状态字段。
// 入队永不阻塞转发链路; 队列满载时由 op 层丢弃并计数。
// errCompleteLogCount 记录每个 err_class 已写入完整请求体的次数, 超过 3 条后仅存摘要。
var errCompleteLogCount sync.Map

// completeLogQuota 每类错误保留完整请求体的条数上限。
const completeLogQuota = 3

func recordErrorLog(request *RequestState) {
	// 业务错误计数: 不依赖日志留存/去重/丢弃, 在投递日志前即递增,
	// 即使队列满载丢弃该条, 失败事件仍被计入 TotalErrorCount。
	totalErrorCount.Add(1)

	classStr := string(request.Class)
	counter, _ := errCompleteLogCount.LoadOrStore(classStr, &atomic.Int64{})
	seq := counter.(*atomic.Int64).Add(1)

	// 每类前 3 条保留完整请求体(不截断), 后续仅存摘要不含请求体, 控制磁盘增长。
	// 命中明细的保留策略与请求体对齐: 仅在保留完整请求体的条目上附带, 控制落盘量(文档 07 §3.2)。
	var reqBody string
	var maskMatchesJSON json.RawMessage
	if seq <= completeLogQuota {
		reqBody = request.body
		if len(request.MaskMatches) > 0 {
			if data, mErr := json.Marshal(request.MaskMatches); mErr == nil {
				maskMatchesJSON = json.RawMessage(data)
			}
		}
	}

	op.RecordErrorBucket(request.Model)
	op.ErrorLogEnqueue(model.ErrorLog{
		CreatedAt:       time.Now(),
		Model:           request.Model,
		ChannelName:     request.TargetChannel,
		TargetModel:     request.TargetModel,
		ClientIP:        request.ClientIP,
		APIKeyName:      request.KeyName,
		APIKeySuffix:    request.APIKey,
		ClientFormat:    request.ClientFormat,
		UpstreamType:    request.UpstreamType,
		RelayMode:       request.RelayMode,
		ProxyAddr:       request.ProxyAddr,
		ChannelKeyLabel: request.KeyLabel,
		ErrClass:        classStr,
		ErrBrief:        request.Error,
		ErrDetail:       request.Error,
		RequestBody:     reqBody,
		MaskMatches:     maskMatchesJSON,
	})
	// 同类错误只保留最近 3 条, 避免同一问题刷屏挤掉其他类型的错误记录。
	op.ErrorLogDedupPerClass(string(request.Class), 3)
}

// rejectRequest 以客户端协议的错误格式返回请求级失败, 用于尚未登记状态因而无需定稿的请求。
// 管理端「一键停止」属于瞬态不可用, 返回 503(+Retry-After) 让下游 SDK 按可重试处理;
// 暂无可用渠道保持 400: 这是测试钉死的既有下游契约(客户端按确定性失败自行重试整个请求),
// 其余请求级错误(协议/参数问题)同为 400, 下游按不可重试的确定性失败处理。
func rejectRequest(c *gin.Context, inbound transformer.Inbound, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errAllRequestsStopped) {
		status = http.StatusServiceUnavailable
		c.Header("Retry-After", "5")
	}
	response := inbound.TransformError(c.Request.Context(), &llm.ResponseError{
		StatusCode: status,
		Detail:     llm.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"},
	})
	c.Data(status, "application/json", response.Body)
	c.Abort()
}

// sanitizeRequestBody 就地修正请求体中已知的上游不兼容字段:
// 单条 stop_sequences 字符串→数组、developer 角色→首条 system/其余 user。
// 只修改会触发 400 的字段, 不改动正常请求。
func sanitizeRequestBody(raw *httpclient.Request) {
	// stop: 字符串→单元素数组
	stop := gjson.GetBytes(raw.Body, "stop")
	if stop.Type == gjson.String {
		if updated, sErr := sjson.SetRawBytes(raw.Body, "stop", []byte("["+stop.Raw+"]")); sErr == nil {
			raw.Body = updated
		}
	}
	// temperature: 钳制到 [0,1] 范围
	temp := gjson.GetBytes(raw.Body, "temperature")
	if temp.Exists() && temp.Float() > 1.0 {
		if updated, sErr := sjson.SetBytes(raw.Body, "temperature", 1.0); sErr == nil {
			raw.Body = updated
		}
	}
	// developer 角色: 首条→system, 其余→user
	msgs := gjson.GetBytes(raw.Body, "messages")
	if !msgs.IsArray() {
		return
	}
	isFirst := true
	for i, m := range msgs.Array() {
		if m.Get("role").String() != "developer" {
			continue
		}
		role := "user"
		if isFirst {
			role = "system"
			isFirst = false
		}
		if updated, sErr := sjson.SetBytes(raw.Body, fmt.Sprintf("messages.%d.role", i), role); sErr == nil {
			raw.Body = updated
		}
	}
}

// looksLikeAnthropicMessages 报告请求体是否为 Anthropic Messages API 格式。
// 用于 /v1/chat/completions 端点的协议自动检测: 部分客户端(如 ZCode)将原生
// Anthropic 格式请求发往 OpenAI Chat 端点, OpenAI Chat 解析器无法正确处理
// 顶层 system 字段和 input_schema 工具定义。
//
// 检测标记(任一即判定为 Anthropic):
//   - 顶层 "system" 字段存在(Anthropic 将 system 放在顶层, OpenAI Chat 用 messages[].role:"system")
//   - tools 中存在 "input_schema"(Anthropic)而非 "function.parameters"(OpenAI Chat)
func looksLikeAnthropicMessages(body []byte) bool {
	// Anthropic 的顶层 system 字段: 字符串或数组。OpenAI Chat 不使用此字段。
	if gjson.GetBytes(body, "system").Exists() {
		return true
	}
	// Anthropic 工具用 input_schema; OpenAI Chat 工具用 function.parameters。
	// 检查第一个工具即可——同一请求内所有工具格式一致。
	if gjson.GetBytes(body, "tools.0.input_schema").Exists() {
		return true
	}
	return false
}
