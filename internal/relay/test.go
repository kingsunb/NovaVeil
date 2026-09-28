package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ChannelTestResult 单次渠道模型测试的结果。
type ChannelTestResult struct {
	Model            string `json:"model"`             // 测试的模型名称。
	Content          string `json:"content"`           // 模型回复文本。
	ElapsedMS        int64  `json:"elapsed_ms"`        // 端到端耗时毫秒。
	PromptTokens     int64  `json:"prompt_tokens"`     // 输入 token 数。
	CompletionTokens int64  `json:"completion_tokens"` // 输出 token 数。
}

// ChannelKeyTestResult 渠道单把密钥的可用性测试结果。
type ChannelKeyTestResult struct {
	KeyID     string `json:"key_id"`            // 密钥稳定标识; 旧式单 Key 渠道为空串。
	Label     string `json:"label"`             // 面板展示标签: "#序号(备注/ID)"。
	OK        bool   `json:"ok"`                // 上游是否成功返回回复。
	Content   string `json:"content,omitempty"` // 成功时的回复摘要。
	Error     string `json:"error,omitempty"`   // 失败原因(上游错误原文/超时等)。
	ElapsedMS int64  `json:"elapsed_ms"`        // 端到端耗时毫秒。
}

// KEY_TEST_CONCURRENCY 逐密钥测试的并发上限, 与面板批量模型测试的并发保持一致。
const KEY_TEST_CONCURRENCY = 4

// keyTestRequestTimeout 单把密钥/分组成员测试的独立超时。
const keyTestRequestTimeout = 60 * time.Second

// testProbeClientIP 测试探针在日志流里的客户端标识: 探针没有真实来源 IP,
// 用固定中文标记与业务流量区分。因为不走 newRequestState, 该标记不会计入
// 客户端调用统计(client_stats), 仅作面板展示。
const testProbeClientIP = "面板测试"

// diagInfraRetryInterval 诊断/评估探针对基础设施错误重试的等待间隔, 对齐分组路由
// 默认重试间隔(MemberRetryIntervalSeconds)。定义为变量仅为让测试缩短等待。
var diagInfraRetryInterval = 2 * time.Second

// diagInfraRetryLimit 诊断/评估探针的网络错误容忍次数, 取分组路由 MemberInfraMaxRetries
// 的出厂默认值; 计数语义与路由层一致(阈值含首次失败, 达到即止), 即最多发起 limit 次上游调用。
// 探针不绑定单一分组(渠道可属多个分组, 评估按渠道发起), 故不读具体分组配置而统一用默认值。
func diagInfraRetryLimit() int {
	return max(model.DefaultGroupRelayConfig().MemberInfraMaxRetries, 1)
}

// sendDiagnosticUpstream 以诊断/评估语义发起一次非流式上游调用: 基础设施层错误
// (代理/DNS/TLS/连接重置/连接提前中断等, isInfrastructureError 判定)按网络错误重试策略
// 最多尝试 diagInfraRetryLimit() 次, 每次间隔 diagInfraRetryInterval, 期间尊重 ctx 取消;
// 业务错误(4xx/5xx/限流/响应校验失败)与上下文取消/超时不重试, 原样返回——重试不可能
// 改变结果, 只会烧计费请求。面板测试、模型评估与半开探测此前均为单发路径, 瞬时网络
// 抖动(如 unexpected EOF)会把渠道/密钥直接判死、把恢复中的成员重新打入更长冷却,
// 与分组路由对真实流量按网络错误重试的容错口径不一致。
func sendDiagnosticUpstream(ctx context.Context, send func() (*upstreamResponse, error)) (*upstreamResponse, error) {
	limit := diagInfraRetryLimit()
	for attempt := 1; ; attempt++ {
		resp, err := send()
		if err == nil || attempt >= limit || !isRetryableDiagnosticError(err) {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(diagInfraRetryInterval):
		}
	}
}

// isRetryableDiagnosticError 判定诊断探针的失败是否值得重试: 基础设施层错误可重试;
// 上下文取消/超时表示调用方已放弃或时间预算耗尽, 重试只会把等待拉长 N 倍, 不重试。
func isRetryableDiagnosticError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return isInfrastructureError(err)
}

// startTestRequest 在准备/发送探针前发布运行态。探针复用请求状态与轮次轨迹,
// 但不计入业务客户端统计、用量分桶与对话留存。
func startTestRequest(clientModel string, rawBody []byte, target RoundTarget, cancel context.CancelFunc) *RequestState {
	mu.Lock()
	defer mu.Unlock()
	request := &RequestState{
		ID:            idSeq.Add(1),
		Status:        StatusRunning,
		StartedAt:     time.Now(),
		Model:         clientModel,
		ClientIP:      testProbeClientIP,
		TargetChannel: target.ChannelName,
		TargetModel:   target.Model,
		KeyLabel:      target.KeyLabel,
		ClientFormat:  target.ClientFormat,
		UpstreamType:  target.UpstreamType,
		RelayMode:     "converted",
		ProxyAddr:     target.ProxyAddr,
		body:          truncatePreview(string(rawBody)),
		cancel:        cancel,
		stopCh:        make(chan struct{}),
	}
	if target.Passthrough {
		request.RelayMode = "passthrough"
	}
	requests[request.ID] = request
	publishRequestLocked(request)
	return request
}

// finishTestRequest 更新同一探针的终态并加入有界历史, 不额外创建日志行。
func finishTestRequest(request *RequestState, responseBody string, usage *llm.Usage, reqErr error) {
	mu.Lock()
	defer mu.Unlock()
	request.Status = StatusSuccess
	request.Class = ""
	request.Error = ""
	if reqErr != nil {
		request.Status = StatusFailed
		request.Class = ClassifyError(reqErr)
		request.Error = truncateErrBrief(reqErr.Error())
		if errors.Is(reqErr, context.Canceled) || request.stopRequested {
			request.Status = StatusCanceled
			request.Class = ErrClassClientCancel
			if request.stopRequested {
				request.Class = ErrClassAdminAbort
			}
		} else if errors.Is(reqErr, context.DeadlineExceeded) {
			request.Class = ErrClassTimeout
		}
	}
	request.Duration = time.Since(request.StartedAt)
	request.Sending = false
	request.cancel = nil
	request.responseBody = truncatePreview(responseBody)
	if usage != nil {
		request.Usage = *usage
	}
	if request.Status == StatusFailed {
		appendFailureLocked(FailureSummary{
			ID:            request.ID,
			FinishedAt:    time.Now(),
			Model:         request.Model,
			TargetChannel: request.TargetChannel,
			TargetModel:   request.TargetModel,
			ErrClass:      request.Class,
			ErrBrief:      request.Error,
		})
	}
	publishRequestLocked(request)
	finishedRequestQueue = append(finishedRequestQueue, request.ID)
	trimFinishedRequestsLocked()
}

// TestChannel 按渠道的生效协议模拟下游请求, 向单个模型发送测试消息并返回回复摘要。
// 测试请求走与真实转发一致的转换 pipeline, 渠道参数覆盖与模型限制同样生效。
// keyID 为空时固定使用第一把健康 Key(与半开/后台探测一致);
// 非空时强制使用该把密钥并绕过冷却——管理端显式验证某把 Key, 冷却跳过会静默换 Key 使结果失真。
// 每次测试从运行态到终态复用同一条日志记录(客户端标记为 面板测试)。
func TestChannel(ctx context.Context, channelID int, modelName string, message string, keyID string) (*ChannelTestResult, error) {
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	if message == "" {
		message = "ping"
	}

	effective, keyIndex, key, err := effectiveTestChannel(channel, keyID)
	if err != nil {
		recordTestSetupFailure(channel, modelName, modelName, message, channelKeyLabel(keyIndex, key), err)
		return nil, err
	}
	return sendChannelTestRequest(ctx, effective, modelName, message, channelKeyLabel(keyIndex, key), testPanelMaxTokens)
}

// evalKeyAttemptLimit 单渠道单模型评估时最多尝试的密钥把数。密钥极多的渠道若逐把
// 失败再换下一把, 单次评估会把大量计费请求烧在坏密钥上并拖很久(每把最长 10 分钟),
// 因此评估只顺序探测前 N 把, 首把可用即短路, 全部失败即止步于该上限。
const evalKeyAttemptLimit = 10

// TestChannelKeyFailover 依次尝试渠道的密钥, 任一成功即返回该 Key 的结果;
// 全部失败时返回聚合错误。供模型评估使用, 与面板逐密钥诊断(TestChannelKeys 并发全测)不同:
// 评估只需确认渠道可服务该模型, 顺序探测避免对同一上游产生并发压力, 且首个可用 Key 即可短路。
// 不检查冷却: 评估是主动诊断, 不受被业务流量的冷却状态遮蔽。
// 单渠道单模型最多尝试前 evalKeyAttemptLimit 把密钥, 避免密钥极多的渠道在评估时逐把烧计费请求。
func TestChannelKeyFailover(ctx context.Context, channelID int, modelName string, message string) (*ChannelTestResult, error) {
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	if message == "" {
		message = "ping"
	}
	candidates := channelKeyTestCandidates(channel)
	if len(candidates) == 0 {
		err := fmt.Errorf("渠道未配置任何密钥")
		recordTestSetupFailure(channel, modelName, modelName, message, "", err)
		return nil, err
	}
	tried := candidates
	if len(tried) > evalKeyAttemptLimit {
		tried = tried[:evalKeyAttemptLimit]
	}
	var errs []string
	for index, key := range tried {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		effective, err := effectiveChannelForKey(channel, key)
		if err != nil {
			recordTestSetupFailure(channel, modelName, modelName, message, channelKeyLabel(index, key), err)
			errs = append(errs, fmt.Sprintf("#%d(%s): %v", index+1, key.ID, err))
			continue
		}
		// 评估路径需要完整的长 HTML+SVG 产物, 沿用 testMaxTokens(100k) 上限。
		// 面板单模型/逐密钥/分组诊断路径使用 testPanelMaxTokens(4k) 控制计费。
		result, err := sendChannelTestRequest(ctx, effective, modelName, message, channelKeyLabel(index, key), testMaxTokens)
		if err == nil {
			return result, nil
		}
		errs = append(errs, fmt.Sprintf("#%d(%s): %v", index+1, key.ID, err))
	}
	if len(tried) < len(candidates) {
		return nil, fmt.Errorf("前 %d 把密钥测试均失败(渠道共 %d 把密钥, 已达单次评估尝试上限): %s", len(tried), len(candidates), strings.Join(errs, "; "))
	}
	return nil, fmt.Errorf("全部密钥测试失败: %s", strings.Join(errs, "; "))
}

// TestChannelKeys 对渠道配置的每一把密钥各发送一条测试消息, 按配置顺序返回逐 Key 结果,
// 供管理端一键核验全部密钥有效性。诊断入口不写冷却记录, 每把密钥独立判定互不影响。
func TestChannelKeys(ctx context.Context, channelID int, modelName string, message string) ([]ChannelKeyTestResult, error) {
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}
	return testChannelKeysWithChannel(ctx, channel, modelName, message)
}

// testChannelKeysWithChannel 以固定并发池跑完渠道全部候选密钥, 结果按候选顺序回填。
func testChannelKeysWithChannel(ctx context.Context, channel model.Channel, modelName string, message string) ([]ChannelKeyTestResult, error) {
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	if message == "" {
		message = "ping"
	}
	candidates := channelKeyTestCandidates(channel)
	if len(candidates) == 0 {
		err := fmt.Errorf("渠道未配置任何密钥")
		recordTestSetupFailure(channel, modelName, modelName, message, "", err)
		return nil, err
	}

	results := make([]ChannelKeyTestResult, len(candidates))
	var cursor atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < min(KEY_TEST_CONCURRENCY, len(candidates)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(cursor.Add(1)) - 1
				if index >= len(candidates) {
					return
				}
				results[index] = testChannelKeyOnce(ctx, channel, index, candidates[index], modelName, message)
			}
		}()
	}
	wg.Wait()
	return results, nil
}

// channelKeyTestCandidates 汇总待测密钥候选: 多 Key 渠道按配置顺序全部纳入(跳过明文为空
// 的残缺条目), 旧式单 Key 渠道(含无密钥渠道)回退为单个无 ID 候选, 与单模型测试的探测路径同口径。
func channelKeyTestCandidates(channel model.Channel) []model.ChannelKey {
	if len(channel.Keys) == 0 {
		return []model.ChannelKey{{Key: channel.Key}}
	}
	candidates := make([]model.ChannelKey, 0, len(channel.Keys))
	for _, key := range channel.Keys {
		if key.Key == "" {
			continue
		}
		candidates = append(candidates, key)
	}
	return candidates
}

// testChannelKeyOnce 对单把密钥执行一次测试并填充结果; 失败只记录原因, 不影响其余密钥。
func testChannelKeyOnce(ctx context.Context, channel model.Channel, index int, key model.ChannelKey, modelName string, message string) ChannelKeyTestResult {
	result := ChannelKeyTestResult{KeyID: key.ID, Label: channelKeyLabel(index, key)}
	effective, err := effectiveChannelForKey(channel, key)
	if err == nil {
		// 诊断测试不与业务流量抢并发槽位: 清零副本上的 max_concurrent,
		// 避免业务请求占满信号量时测试在等槽位中烧掉自己的超时。
		effective.MaxConcurrent = 0
		err = sendKeyTestRequest(ctx, effective, modelName, message, &result)
	} else {
		recordTestSetupFailure(channel, modelName, modelName, message, result.Label, err)
	}
	switch {
	case err == nil:
		result.OK = true
	case errors.Is(err, errProxyTemplateInvalid):
		result.Error = "代理地址模板无效"
	case errors.Is(err, context.DeadlineExceeded):
		result.Error = fmt.Sprintf("上游响应超时(%s)", keyTestRequestTimeout)
	default:
		result.Error = err.Error()
	}
	return result
}

// sendKeyTestRequest 发送单把密钥的测试请求并把回复摘要与耗时写入 result。
// 与单模型测试共用协议选择、日志生命周期和请求头注入。
func sendKeyTestRequest(ctx context.Context, effective model.Channel, modelName string, message string, result *ChannelKeyTestResult) error {
	keyCtx, cancel := context.WithTimeout(ctx, keyTestRequestTimeout)
	defer cancel()

	startedAt := time.Now()
	response, err := sendChannelTestRequest(keyCtx, effective, modelName, message, result.Label, testPanelMaxTokens)
	result.ElapsedMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		return err
	}
	result.Content = response.Content
	return nil
}

// effectiveTestChannel 解析 TestChannel 的生效渠道: keyID 非空时按 ID 定位该把密钥并绕过冷却,
// 找不到时返回中文错误(该错误会原样展示在面板); 为空时回退探测路径的第一把健康 Key。
func effectiveTestChannel(channel model.Channel, keyID string) (model.Channel, int, model.ChannelKey, error) {
	if keyID == "" {
		return effectiveProbeChannel(channel)
	}
	for index, candidate := range channel.Keys {
		if candidate.ID == keyID {
			effective, err := effectiveChannelForKey(channel, candidate)
			if err != nil {
				return model.Channel{}, index, candidate, err
			}
			return effective, index, candidate, nil
		}
	}
	return model.Channel{}, 0, model.ChannelKey{}, fmt.Errorf("渠道上不存在指定的密钥")
}

// testMaxTokens 模型评估路径的输出 token 上限: 评估页要求模型生成完整 HTML+SVG
// 动画, 产物动辄数万 token; 早期硬编码 1024 会截断 Anthropic 渠道的输出导致渲染
// 残缺, 统一放宽到 100k 与各协议上限留出余量。面板诊断路径不使用该值。
const testMaxTokens = 100000

// testPanelMaxTokens 面板/分组诊断测试请求的输出 token 上限。
// 诊断只需确认渠道与该模型可服务, 不需要数万 token 的完整产物; 4k 足以覆盖回复
// 摘要与错误信息, 并把管理后台单次误测/恶意触发的计费上限压在低位。
const testPanelMaxTokens = 4096

// newTestRequest 按 format 构造一条非流式测试请求。maxTokens 指定输出上限:
// 评估路径传 testMaxTokens, 面板/分组诊断路径传 testPanelMaxTokens, 传 0 时回退
// testPanelMaxTokens(诊断默认)。Path 与正文一起按客户端协议构造, 完全透传
// 同样携带真实的入站路径。未提供原生入站的渠道通过 Chat 转换到上游协议。
//   - OpenAI Chat / Anthropic Messages: messages 数组; Anthropic 另需 max_tokens。
//   - OpenAI Responses: input 字段。
//
// 三种协议均显式注入输出上限, 避免上游默认值(部分渠道仅 1024)截断诊断回复;
// 个别模型不接受该字段时会以错误返回, 由调用方按测试失败处理。
func newTestRequest(format llm.APIFormat, modelName, message string, maxTokens int) (*httpclient.Request, error) {
	if maxTokens <= 0 {
		maxTokens = testPanelMaxTokens
	}
	body := []byte("{}")
	var err error
	switch format {
	case llm.APIFormatAnthropicMessage:
		if body, err = sjson.SetBytes(body, "model", modelName); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "messages", []map[string]string{{"role": "user", "content": message}}); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "max_tokens", maxTokens); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "stream", false); err != nil {
			return nil, err
		}
	case llm.APIFormatOpenAIResponse:
		if body, err = sjson.SetBytes(body, "model", modelName); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "input", message); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "max_output_tokens", maxTokens); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "stream", false); err != nil {
			return nil, err
		}
	default: // llm.APIFormatOpenAIChatCompletion
		if body, err = sjson.SetBytes(body, "model", modelName); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "messages", []map[string]string{{"role": "user", "content": message}}); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "max_tokens", maxTokens); err != nil {
			return nil, err
		}
		if body, err = sjson.SetBytes(body, "stream", false); err != nil {
			return nil, err
		}
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if format == llm.APIFormatAnthropicMessage {
		headers.Set("Anthropic-Version", "2023-06-01")
	}
	return &httpclient.Request{
		Method:    http.MethodPost,
		Path:      "/v1" + upstreamPath(format),
		Headers:   headers,
		Body:      body,
		APIFormat: format.String(),
	}, nil
}

// newTestChatRequest 构造一条 OpenAI Chat 非流式测试请求; 保留为旧调用点兼容入口。
func newTestChatRequest(modelName string, message string) (*httpclient.Request, error) {
	return newTestRequest(llm.APIFormatOpenAIChatCompletion, modelName, message, testPanelMaxTokens)
}

// sendChannelTestRequest 发送单模型测试请求并聚合回复摘要、耗时与 token 用量。
// maxTokens 由调用方按场景传入: 面板诊断用 testPanelMaxTokens, 模型评估用 testMaxTokens。
// 客户端协议取渠道/模型的生效协议; keyLabel 标识所用密钥。
func sendChannelTestRequest(ctx context.Context, channel model.Channel, modelName string, message string, keyLabel string, maxTokens int) (*ChannelTestResult, error) {
	return sendTestRequest(ctx, channel, modelName, modelName, message, keyLabel, maxTokens)
}

// testRequestTarget 与真实转发共用生效协议规则, 包括 OpenCode 的模型协议覆盖。
func testRequestTarget(channel model.Channel, modelName, keyLabel string) (llm.APIFormat, *model.ChannelModel, RoundTarget) {
	var channelModel *model.ChannelModel
	for i := range channel.Models {
		if channel.Models[i].Name == modelName {
			channelModel = &channel.Models[i]
			break
		}
	}
	format := channelNativeFormat(outboundProvider(channel, channelModel))
	return format, channelModel, RoundTarget{
		ChannelID:    channel.ID,
		ChannelName:  channel.Name,
		Model:        modelName,
		KeyLabel:     keyLabel,
		ClientFormat: clientFormatLabel(format),
		UpstreamType: upstreamTypeLabel(channel, channelModel),
		Passthrough:  supportsNativeFormat(channel, channelModel, format),
		ProxyAddr:    roundProxyLabel(channel, channel),
	}
}

// recordTestSetupFailure 记录渠道已识别但密钥/代理等准备失败的诊断结果。
func recordTestSetupFailure(channel model.Channel, clientModel, modelName, message, keyLabel string, reqErr error) {
	format, _, target := testRequestTarget(channel, modelName, keyLabel)
	var body []byte
	if raw, err := newTestRequest(format, modelName, message, testPanelMaxTokens); err == nil {
		body = raw.Body
	} // 构造失败时保留原始准备错误, 请求体为空。
	request := startTestRequest(clientModel, body, target, nil)
	finishTestRequest(request, "", nil, reqErr)
}

// sendTestRequest 统一单模型、逐密钥、分组与评估探针的出站和可观察生命周期。
// clientModel 仅用于日志展示(分组测试为分组名), modelName 是实际上游模型。
func sendTestRequest(ctx context.Context, channel model.Channel, clientModel, modelName, message, keyLabel string, maxTokens int) (*ChannelTestResult, error) {
	format, channelModel, target := testRequestTarget(channel, modelName, keyLabel)
	testCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	raw, err := newTestRequest(format, modelName, message, maxTokens)
	if err != nil {
		recordTestSetupFailure(channel, clientModel, modelName, message, keyLabel, err)
		return nil, err
	}
	request := startTestRequest(clientModel, raw.Body, target, cancel)
	outbound, passthrough, err := buildOutbound(channel, channelModel, format)
	if err != nil {
		finishTestRequest(request, "", nil, err)
		return nil, err
	}
	target.Passthrough = passthrough
	// 生成随机值以触发 injectRandomHeaders 注入动态头(含 opencode 兼容头),
	// 与真实转发路径保持一致; 空串会让 injectRandomHeaders 提前返回而漏注这些头。
	randomValue := uuid.NewString()
	result, err := sendDiagnosticUpstream(testCtx, func() (*upstreamResponse, error) {
		if err := testCtx.Err(); err != nil {
			return nil, err
		}
		request.startRound(cancel, target)
		var response *upstreamResponse
		var sendErr error
		if passthrough {
			response, sendErr = sendPassthrough(testCtx, format, raw, channel, outbound, false, randomValue)
		} else {
			response, sendErr = sendConverted(testCtx, format, raw, channel, outbound, false, randomValue)
		}
		if sendErr == nil {
			request.finishRound(AttemptSuccess, "", "")
		} else {
			outcome, class := AttemptFailed, ClassifyError(sendErr)
			if errors.Is(sendErr, context.Canceled) {
				outcome, class = AttemptCanceled, ErrClassClientCancel
				if request.IsStopRequested() {
					class = ErrClassAdminAbort
				}
			} else if errors.Is(sendErr, context.DeadlineExceeded) {
				class = ErrClassTimeout
			}
			request.finishRound(outcome, class, truncateErrBrief(sendErr.Error()))
		}
		return response, sendErr
	})
	if err != nil {
		// HTTP 错误体供日志详情按需读取, 不把原文塞进状态流。
		var failure *httpclient.Error
		responseBody := ""
		if errors.As(err, &failure) {
			responseBody = string(failure.Body)
		}
		// 重试等待期间取消/超时以请求上下文为准, 避免仍显示上一轮网络错误。
		if contextErr := testCtx.Err(); contextErr != nil {
			err = contextErr
		}
		finishTestRequest(request, responseBody, nil, err)
		return nil, err
	}
	defer result.Close()

	usage := llm.Usage{}
	if result.usage != nil {
		usage = *result.usage
	}
	finishTestRequest(request, string(result.body), &usage, nil)
	return &ChannelTestResult{
		Model:            modelName,
		Content:          extractMessageContent(result.body),
		ElapsedMS:        request.Duration.Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
	}, nil
}

// channelNativeFormat 返回网关支持的对应入站格式。Gemini/Volcengine/Custom
// 无独立原生入站, 使用 OpenAI Chat 并交给现有转换器适配上游。
func channelNativeFormat(channelType model.ChannelProvider) llm.APIFormat {
	switch channelType {
	case model.ChannelProviderOpenAI:
		return llm.APIFormatOpenAIChatCompletion
	case model.ChannelProviderOpenAIResponses:
		return llm.APIFormatOpenAIResponse
	case model.ChannelProviderAnthropic:
		return llm.APIFormatAnthropicMessage
	default:
		return llm.APIFormatOpenAIChatCompletion
	}
}

// extractMessageContent 从上游响应中提取回复文本, 跳过推理与工具块并拼接文本块:
//   - OpenAI Chat: choices.0.message.content(字符串或分片数组)。
//   - Anthropic Messages: content 中的 text 块。
//   - OpenAI Responses: output 中所有 message 的 content 文本块。
//
// 逐路径尝试, 命中即返回; 均不命中返回空串。
func extractMessageContent(responseBody []byte) string {
	// OpenAI Chat: choices.0.message.content, 兼容字符串与分片数组两种内容形态。
	if field := gjson.GetBytes(responseBody, "choices.0.message.content"); field.Exists() {
		if field.Type == gjson.String {
			return field.String()
		}
		if field.IsArray() {
			texts := make([]string, 0, len(field.Array()))
			for _, part := range field.Array() {
				if text := part.Get("text"); text.Exists() {
					texts = append(texts, text.String())
				}
			}
			if len(texts) > 0 {
				return strings.Join(texts, "")
			}
		}
	}
	var texts []string
	for _, part := range gjson.GetBytes(responseBody, "content").Array() {
		if part.Get("type").String() == "text" && part.Get("text").Type == gjson.String {
			texts = append(texts, part.Get("text").String())
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "")
	}
	for _, item := range gjson.GetBytes(responseBody, "output").Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" && part.Get("text").Type == gjson.String {
				texts = append(texts, part.Get("text").String())
			}
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "")
	}
	return ""
}

// GroupTestResult 分组测试单成员的结果条目。
type GroupTestResult struct {
	ChannelName string `json:"channel_name"`         // 成员所属渠道名。
	Model       string `json:"model"`                // 上游模型名。
	Status      string `json:"status"`               // "ok" 或 "fail"。
	Content     string `json:"content,omitempty"`    // 成功时的回复摘要。
	LatencyMS   int64  `json:"latency_ms"`           // 端到端耗时毫秒。
	Error       string `json:"error,omitempty"`      // 失败原因。
	RelayMode   string `json:"relay_mode,omitempty"` // "passthrough" 或 "converted"。
}

// groupTestConcurrency 分组测试的并发上限, 与面板批量模型测试保持一致,
// 避免一次性向分组内全部渠道打出过多计费请求触发上游 rate limit。
const groupTestConcurrency = 3

// TestGroup 对分组全体成员(含引用成员递归展开)各发起一次连通性测试,
// 按成员顺序返回逐条结果, 供管理端一键核验分组可用性。
// 引用成员按真实路由语义递归解析到目标分组的直接渠道成员; 每个直接成员按其渠道
// 的原生协议测试(同协议透传, 异协议转换)。message 为空时默认 "ping"。
// 分组不存在时返回错误; 单个成员的渠道/模型不存在时记一条 "fail" 结果并继续。
func TestGroup(ctx context.Context, groupID int, message string) ([]GroupTestResult, error) {
	var group model.Group
	found := false
	for _, g := range op.GroupList() {
		if g.ID == groupID {
			group = g
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("group not found")
	}
	if message == "" {
		message = "ping"
	}

	members := collectGroupTestMembers(group, 0)
	results := make([]GroupTestResult, len(members))
	if len(members) == 0 {
		return results, nil
	}

	// 固定并发池跑完全部直接成员, 结果按展开顺序回填; 与逐密钥测试的游标模式一致。
	var cursor atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < min(groupTestConcurrency, len(members)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(cursor.Add(1)) - 1
				if index >= len(members) {
					return
				}
				results[index] = testGroupMember(ctx, members[index], group.Name, message)
			}
		}()
	}
	wg.Wait()
	return results, nil
}

// collectGroupTestMembers 递归展开分组的直接渠道成员(引用成员按真实路由语义解析),
// 返回渠道模型 ID 刕表; 超过最大引用深度(MaxGroupRefDepth)或引用目标不存在时截断,
// 避免无限递归。重复出现的同一渠道模型会重复测试, 与故障转移时多次命中的语义一致。
func collectGroupTestMembers(group model.Group, depth int) []int {
	if depth > model.MaxGroupRefDepth {
		return nil
	}
	var members []int
	for _, item := range group.Items {
		if item.IsGroupRef() {
			ref, err := op.GroupGetByName(item.RefGroupName)
			if err != nil {
				continue
			}
			members = append(members, collectGroupTestMembers(ref, depth+1)...)
		} else if item.ChannelModelID != 0 {
			members = append(members, item.ChannelModelID)
		}
	}
	return members
}

// testGroupMember 对单个渠道模型发起一次测试, 返回分组测试结果条目。
// groupName 为客户端模型名(分组名), 写入日志流的 Model 字段; 上游模型名取自渠道模型。
// 渠道/模型不存在时记 "fail" 结果并返回, 不影响其余成员。
func testGroupMember(ctx context.Context, channelModelID int, groupName, message string) GroupTestResult {
	cm, err := op.ChannelModelGet(channelModelID)
	if err != nil {
		return GroupTestResult{Status: "fail", Error: fmt.Sprintf("渠道模型不存在: %v", err)}
	}
	channel, err := op.ChannelGet(cm.ChannelID)
	if err != nil {
		return GroupTestResult{Model: cm.Name, Status: "fail", Error: fmt.Sprintf("渠道不存在: %v", err)}
	}
	// 选第一把健康 Key(与单模型测试探测路径一致); 找不到健康 Key 时记失败。
	effective, keyIndex, key, err := effectiveProbeChannel(channel)
	if err != nil {
		recordTestSetupFailure(channel, groupName, cm.Name, message, channelKeyLabel(keyIndex, key), err)
		return GroupTestResult{ChannelName: channel.Name, Model: cm.Name, Status: "fail", Error: err.Error()}
	}
	// 诊断测试不与业务流量抢并发槽位: 清零副本上的 max_concurrent。
	effective.MaxConcurrent = 0

	_, _, target := testRequestTarget(effective, cm.Name, channelKeyLabel(keyIndex, key))
	relayMode := "converted"
	if target.Passthrough {
		relayMode = "passthrough"
	}
	// 单成员独立超时, 避免一个慢上游拖垮整组测试。
	testCtx, cancel := context.WithTimeout(ctx, keyTestRequestTimeout)
	defer cancel()

	startedAt := time.Now()
	resp, err := sendTestRequest(testCtx, effective, groupName, cm.Name, message, target.KeyLabel, testPanelMaxTokens)
	elapsed := time.Since(startedAt)
	if err != nil {
		return GroupTestResult{ChannelName: channel.Name, Model: cm.Name, Status: "fail", Error: err.Error(), LatencyMS: elapsed.Milliseconds(), RelayMode: relayMode}
	}
	return GroupTestResult{ChannelName: channel.Name, Model: cm.Name, Status: "ok", Content: resp.Content, LatencyMS: resp.ElapsedMS, RelayMode: relayMode}
}
