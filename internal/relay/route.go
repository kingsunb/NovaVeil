package relay

import (
	"context"
	"maps"
	"math"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

// RouteState 是一个分组的进程内路由状态, 同时作为路由流的消息形状; 跨该分组的全部请求共享。
type RouteState struct {
	GroupID       int           `json:"group_id"`        // 状态所属的分组 ID。
	CurrentItemID int           `json:"current_item_id"` // 当前承载请求的成员 ID, 0 表示尚未建立路由。
	ProbeItemID   int           `json:"probe_item_id"`   // 当前等待业务二次确认的候选成员 ID, 0 表示无候选。
	AffinityUntil int64         `json:"affinity_until"`  // 当前路由的亲和截止 Unix 毫秒时间, 0 表示无亲和。
	Cooldowns     map[int]int64 `json:"cooldowns"`       // 失败成员 ID 对应的冷却截止 Unix 毫秒时间, 已到期的条目由前端按当前时间忽略。
	Levels        map[int]int   `json:"levels"`          // 成员冷却等级, 半开探测失败逐级退避, 业务确认成功后清零。
	HalfOpens     map[int]int64 `json:"half_opens"`      // 处于 HALF_OPEN 探测中的成员 ID 到进入半开的 Unix 毫秒时间。

	PostCommitStrikes map[int]int `json:"post_commit_strikes"` // 成员提交后失败(流中断/异常终止原因)的跨请求连击计数, 达到阈值进入冷却; 成功即清零。

	EmergencyItemID int `json:"emergency_item_id"` // 紧急兜底模式当前承载请求的成员 ID, 0 表示未进入紧急模式; 与 EmergencyActive 一同供前端感知。
	EmergencyActive int `json:"emergency_active"`  // 当前以紧急兜底模式承载的进行中请求数。

	emergencyCounts map[int]int   // 各成员正在承载的紧急请求数, 与 exported 汇总字段配合支持配置热切换; 仅进程内使用。
	emergencyBlocks map[int]int64 // 紧急兜底业务失败的成员封锁截止 Unix 毫秒时间: 常规冷却不拦紧急放行, 由该标记独立拦停; 仅进程内使用。

	affinityArmed bool // 当前路由下一次成功后是否开始亲和, 仅故障切换后为真。
}

// emergencyMaxConcurrent 是紧急兜底成员同时承载的进行中请求上限:
// pickGroupItem 无法区分请求归属, 故以进程内信号量做全局节流式放行, 超出的请求继续等待走原逻辑。
const emergencyMaxConcurrent = 3

const routeStreamBuffer = 16 // 单个路由流连接的非阻塞消息缓冲容量。

var (
	routeMu      sync.Mutex                           // routeMu 保护全部分组路由状态。
	routes       = make(map[int]*RouteState)          // routes 按分组 ID 保存路由状态。
	routeStreams = make(map[chan RouteState]struct{}) // 全部路由 SSE 连接。
)

// channelLookupFunc 抽出成员渠道查询以便单元测试注入假实现。
// 使用不含模型列表的 ChannelGetCore: 选路/探测路径只需要 Enabled/Type 等核心字段,
// channelSnapshot 每次调用都要重建并排序全量渠道模型列表, 在 routeMu 临界区内是主要开销。
var channelLookupFunc = op.ChannelGetCore

// groupLookupFunc 抽出引用目标分组查询以便单元测试注入假实现。
var groupLookupFunc = op.GroupGetByName

// probeChannelFunc 抽出合成探测实现以便单元测试注入假实现。
var probeChannelFunc = probeChannel

// channelDisabledForRouting 返回成员指向的渠道当前是否被管理端禁用。
// 引用成员(无渠道关联)或渠道刚删除时无从判定, 保持既有语义交由派发层处理, 这里按未禁用返回。
func channelDisabledForRouting(item model.GroupItem) bool {
	if item.ChannelModel == nil {
		return false
	}
	channel, err := channelLookupFunc(item.ChannelModel.ChannelID)
	if err != nil {
		return false
	}
	return !channel.Enabled
}

// pickGroupItem 按分组模式选择本轮目标成员, 没有可用成员时返回零值; group.Items 已按 Priority 升序排列。
// exclude 为本请求已放弃的成员 ID, 重扫时跳过。被禁用渠道的成员在手动/亲和/常规扫描与紧急兜底
// 各入口一律跳过, 派发层另有兜底检查覆盖粘合等残余旁路; 缺少密钥不在此判断, 由调用方作为一轮失败上报。
// 可选参数 clientFormat 为客户端协议格式: 分组开启 PreferPassthrough 且携带非空格式时,
// 渠道协议与客户端一致的成员整体排到候选最前(各自内部仍按优先级升序), 同协议无健康成员时自然回退跨协议成员;
// 免费渠道分类(IsFree)在协议重排后再次稳定前移: 免费渠道整体优先于付费渠道, 同分类内保持原有扫描顺序,
// 从而让免费渠道在故障转移中优先被选择。开关关闭或未携带格式时协议重排不做, 免费渠道依然前移。
// 排列只改变扫描次序, 冷却跳过/到期待探测/CLOSED 首选等判定逻辑复用同一遍扫描, 熔断、半开探测、粘合与紧急兜底语义不变。
// 同组仍存在 CLOSED 成员时, 扫描中遇到的冷却到期成员经 ensureProbeLocked 异步探测后立即返回 CLOSED 成员,
// 业务请求不被阻塞。全部成员冷却且存在到期成员时就地进入恢复流程: 整批原子切换 HALF_OPEN 后并行探测,
// 返回最先成功的候选成员交由调用方发起业务二次确认; 该调用阻塞至探测有结论, 探测上下文独立于请求生命周期。
// 配置了紧急兜底成员且常规规则一无所获(整批探测失败, 或其余成员全部冷却中被排除)时,
// 在并发上限内节流式放行紧急成员作为本轮目标, 由 recordRouteSuccess/recordRouteFailure/releaseRouteProbe 归还占用。
func pickGroupItem(group model.Group, exclude int, clientFormat ...llm.APIFormat) model.GroupItem {
	if group.Mode == model.GroupModeManual {
		for _, item := range group.Items {
			if item.ID == group.ActiveItemID {
				// 指定成员的渠道被禁用时按无目标处理: 请求进入等待, 重新启用或人工换渠道后继续。
				if channelDisabledForRouting(item) {
					return model.GroupItem{}
				}
				return item
			}
		}
		return model.GroupItem{}
	}

	// 候选重排在加锁前完成: 只读分组快照与渠道缓存, 不与路由状态竞争。
	// 先按「优先透传」做协议重排, 再对结果做「免费渠道优先」的稳定划分:
	// 免费渠道整体排在付费渠道前, 各自内部保持上一步的扫描顺序。
	items := freePriorityOrderedItems(passthroughOrderedItems(group, clientFormat))

	routeMu.Lock()
	route := groupRouteLocked(group)
	now := time.Now().UnixMilli()
	if route.AffinityUntil <= now {
		route.AffinityUntil = 0
	}

	// A synthetic probe has selected one recovered member and the selecting request is
	// performing the real business confirmation. Late requests must wait until that
	// candidate is confirmed, rejected, or released instead of starting another batch.
	if route.ProbeItemID != 0 {
		routeMu.Unlock()
		return model.GroupItem{}
	}

	// 亲和期内沿用当前成员, 不提前探测已恢复的高优先级成员;
	// 当前成员渠道已被禁用时不再延续亲和, 落到下方常规扫描重新选路。
	if route.CurrentItemID != 0 && route.CurrentItemID != exclude && route.AffinityUntil > now {
		item := itemOf(group, route.CurrentItemID)
		if item.ID != 0 && !channelDisabledForRouting(item) {
			routeMu.Unlock()
			return item
		}
	}

	// CLOSED 成员优先: 渠道禁用的成员直接跳过; 冷却未到期或已在探测中的成员跳过,
	// 冷却到期成员先记录待定。
	var expired []model.GroupItem
	var selected model.GroupItem
	for _, item := range items {
		if item.ID == exclude {
			continue
		}
		if channelDisabledForRouting(item) {
			continue
		}
		deadline, cooling := route.Cooldowns[item.ID]
		if cooling {
			if deadline > now || route.HalfOpens[item.ID] > 0 {
				continue
			}
			expired = append(expired, item)
			continue
		}
		selected = item
		break
	}

	// 存在 CLOSED 成员时对到期的 OPEN 成员做异步非阻塞探测: 临界区内原子占用后立即返回当前成员,
	// 合成测试在后台进行, 本请求照常继续服务。
	if selected.ID != 0 {
		for _, item := range expired {
			ensureProbeLocked(route, group, item, now)
		}
		route.CurrentItemID = selected.ID
		publishRouteLocked(route)
		routeMu.Unlock()
		return selected
	}

	// 扫描不到 CLOSED 成员且本批存在到期成员: 全部原子切换 HALF_OPEN 后并行探测。
	// 切换和写入在同一临界区内完成, 其余请求看到探测占用即让路, 避免重复探测。
	if len(expired) > 0 {
		for _, item := range expired {
			claimHalfOpenLocked(route, item.ID, now)
		}
		publishRouteLocked(route)
		routeMu.Unlock()
		if item := recoverExpiredItems(group, expired); item.ID != 0 {
			return item
		}
		// 整批探测失败, 常规规则确认一无所获, 落入紧急兜底判定。
		return claimEmergencyItem(group, exclude)
	}

	routeMu.Unlock()
	return claimEmergencyItem(group, exclude)
}

// passthroughOrderedItems 返回故障转移模式的候选扫描顺序: 分组开启 PreferPassthrough 且调用方携带
// 非空客户端协议时, 渠道协议与客户端一致的成员整体前移, 其余成员保持原有优先级顺序跟在后面;
// 同协议成员不存在、全部成员同协议或开关未生效时原样返回, 不产生任何重排开销。
// 渠道类型取自进程内缓存(op.ChannelGetCore), 引用成员与渠道刚被删除的成员无从判定协议, 按不一致处理留在原位。
// 返回的切片为独立副本, 绝不改动分组快照本身的元素顺序。
func passthroughOrderedItems(group model.Group, clientFormat []llm.APIFormat) []model.GroupItem {
	if !group.RelayConfig.PreferPassthrough || len(clientFormat) == 0 || clientFormat[0] == "" {
		return group.Items
	}
	native := make([]model.GroupItem, 0, len(group.Items))
	other := make([]model.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		if itemMatchesClientFormat(item, clientFormat[0]) {
			native = append(native, item)
		} else {
			other = append(other, item)
		}
	}
	if len(native) == 0 || len(other) == 0 {
		return group.Items
	}
	return append(native, other...)
}

// freePriorityOrderedItems 返回故障转移模式的候选扫描顺序: 免费渠道(IsFree=true)整体前移,
// 付费渠道跟在后面, 各自内部保持传入顺序不变(已含原有优先级/协议重排)。全部同分类、
// 无渠道关联或渠道查询失败时原样返回, 不产生任何重排开销。
func freePriorityOrderedItems(items []model.GroupItem) []model.GroupItem {
	free := make([]model.GroupItem, 0, len(items))
	paid := make([]model.GroupItem, 0, len(items))
	for _, item := range items {
		if itemIsFreeChannel(item) {
			free = append(free, item)
		} else {
			paid = append(paid, item)
		}
	}
	if len(free) == 0 || len(paid) == 0 {
		return items
	}
	return append(free, paid...)
}

// itemIsFreeChannel 报告成员是否指向已标记为免费的渠道。引用成员或渠道查询失败时
// 无法判定免费属性, 按非免费处理以保持保守行为。
func itemIsFreeChannel(item model.GroupItem) bool {
	if item.ChannelModel == nil {
		return false
	}
	channel, err := channelLookupFunc(item.ChannelModel.ChannelID)
	if err != nil {
		return false
	}
	return channel.IsFree
}

// itemMatchesClientFormat 报告成员渠道的原生协议是否与客户端一致(即可整包透传):
// supportsNativeFormat 为真表示同协议。引用成员或渠道查询失败时无法判定, 一律按不一致处理。
func itemMatchesClientFormat(item model.GroupItem, format llm.APIFormat) bool {
	if item.ChannelModel == nil {
		return false
	}
	channel, err := channelLookupFunc(item.ChannelModel.ChannelID)
	if err != nil {
		return false
	}
	return supportsNativeFormat(channel, format)
}

// claimEmergencyItem 紧急兜底放行的加锁入口: 常规选路一无所获时把配置的紧急成员交给本轮请求。
func claimEmergencyItem(group model.Group, exclude int) model.GroupItem {
	routeMu.Lock()
	defer routeMu.Unlock()

	route := groupRouteLocked(group)
	return claimEmergencyLocked(route, group, exclude, time.Now().UnixMilli())
}

// claimEmergencyLocked 判定并占用一个紧急兜底并发额度: 配置关闭、成员不存在或已删除、
// 本请求已放弃该成员、该成员处于探测占用或紧急封锁、并发额度耗尽时均不放行并返回零值。
// 紧急放行是全部常规成员不可用时的最后防线, 不因成员自身的常规冷却记录却步;
// 紧急业务失败写入的 emergencyBlocks 封锁标记才拦停后续放行, 避免冷却中的故障兜底成员被持续击打。
// 放行即递增进程内信号量并在路由状态上标记紧急模式, 供 SSE 快照与成败上报区分来源; 调用方必须持有锁。
func claimEmergencyLocked(route *RouteState, group model.Group, exclude int, now int64) model.GroupItem {
	itemID := group.RelayConfig.EmergencyItemID
	if itemID <= 0 || itemID == exclude || route.EmergencyActive >= emergencyMaxConcurrent {
		return model.GroupItem{}
	}
	item := itemOf(group, itemID)
	if item.ID == 0 {
		return model.GroupItem{}
	}
	// 紧急兜底是常规成员全部不可用时的最后防线, 但不推翻管理端的禁用开关:
	// 被禁用成员即使配置为紧急兜底也不放行。
	if channelDisabledForRouting(item) {
		return model.GroupItem{}
	}
	if route.HalfOpens[itemID] > 0 {
		return model.GroupItem{}
	}
	if block, blocked := route.emergencyBlocks[itemID]; blocked && block > now {
		return model.GroupItem{}
	}

	if route.emergencyCounts == nil {
		route.emergencyCounts = make(map[int]int)
	}
	route.emergencyCounts[itemID]++
	syncEmergencySummaryLocked(route)
	publishRouteLocked(route)
	return item
}

// emergencyHeldLocked 返回指定成员当前是否承载着未结束的紧急请求; 调用方必须持有锁。
func emergencyHeldLocked(route *RouteState, itemID int) bool {
	return route.emergencyCounts[itemID] > 0
}

// releaseEmergencyLocked 归还指定成员的一个紧急并发额度, 额度归零后清除紧急模式标记。
// 返回路由状态是否发生变化, 由调用方决定是否发布; 调用方必须持有锁。
func releaseEmergencyLocked(route *RouteState, itemID int) bool {
	if route.emergencyCounts[itemID] <= 0 {
		return false
	}
	route.emergencyCounts[itemID]--
	if route.emergencyCounts[itemID] <= 0 {
		delete(route.emergencyCounts, itemID)
	}
	syncEmergencySummaryLocked(route)
	return true
}

// syncEmergencySummaryLocked 同步紧急模式的对外汇总字段: 进行中请求数取各成员计数之和,
// 全部归还后清空成员标记, 使前端快照准确反映紧急模式的进出。调用方必须持有锁。
func syncEmergencySummaryLocked(route *RouteState) {
	total := 0
	for _, count := range route.emergencyCounts {
		total += count
	}
	if total <= 0 {
		route.EmergencyItemID = 0
		route.EmergencyActive = 0
		return
	}
	if route.EmergencyItemID == 0 || route.emergencyCounts[route.EmergencyItemID] <= 0 {
		for itemID := range route.emergencyCounts {
			route.EmergencyItemID = itemID
			break
		}
	}
	route.EmergencyActive = total
}

// recoverExpiredItems 对已切换为 HALF_OPEN 的到期成员并行发送合成测试请求, 取最先成功者作为候选返回。
// 任一成员成功即取消其余探测, 被取消成员不出结论并保持原冷却等待下一批; 整批失败时各成员等级加一,
// 冷却按倍数指数退避且不超过上限后重新 OPEN。探测与请求生命周期解耦, 请求取消不影响全局状态收敛。
func recoverExpiredItems(group model.Group, candidates []model.GroupItem) model.GroupItem {
	timeout := group.RelayConfig.MemberNonStreamResponseTimeoutSeconds
	if timeout < 1 {
		timeout = model.DefaultGroupRelayConfig().MemberNonStreamResponseTimeoutSeconds
	}

	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()

	type probeResult struct {
		item model.GroupItem
		err  error
	}
	results := make(chan probeResult, len(candidates))
	var probeWG sync.WaitGroup
	for _, item := range candidates {
		// 引用成员不直接指向渠道模型(关联为空), 与渠道模型刚被删除的成员一样
		// 无法发起合成探测, 按失败结论处理使成员保持原冷却等待下一批。
		if item.ChannelModel == nil {
			results <- probeResult{item: item, err: errChannelModelUnavailable}
			continue
		}
		channel, err := channelLookupFunc(item.ChannelModel.ChannelID)
		if err == nil {
			channel, _, _, err = effectiveProbeChannel(channel)
		}
		if err != nil {
			results <- probeResult{item: item, err: err}
			continue
		}
		probeWG.Add(1)
		go func(item model.GroupItem, channel model.Channel) {
			defer probeWG.Done()
			probeCtx, cancelProbe := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancelProbe()
			results <- probeResult{item: item, err: probeChannelFunc(probeCtx, channel, item.ChannelModel.Name)}
		}(item, channel)
	}

	var winner model.GroupItem
	for remaining := len(candidates); remaining > 0; remaining-- {
		result := <-results
		if result.err != nil || winner.ID != 0 {
			// 失败结果与已有成功者之后的无结论结果都只消耗计数。
			continue
		}
		winner = result.item
		cancelAll()
	}
	probeWG.Wait()

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return model.GroupItem{}
	}
	if winner.ID != 0 {
		// 其余被取消的成员不出结论, 保持原冷却等待下一批到期。
		for _, item := range candidates {
			if item.ID != winner.ID {
				delete(route.HalfOpens, item.ID)
			}
		}
		// 最先成功者成为候选, 等待当前真实业务请求二次确认后再决定是否恢复 CLOSED。
		route.ProbeItemID = winner.ID
		publishRouteLocked(route)
		return winner
	}
	now := time.Now().UnixMilli()
	for _, item := range candidates {
		reopenItemLocked(route, group.RelayConfig, item.ID, now)
	}
	publishRouteLocked(route)
	return model.GroupItem{}
}

// recordRouteSuccess 上报一轮成功: 候选成员业务确认通过即真正恢复, 解除冷却探测与等级;
// 并在故障切换后按配置开始亲和。紧急来源的成功只归还并发额度, 不迁移常规路由当前项也不建立亲和。
func recordRouteSuccess(group model.Group, itemID int) {
	if group.Mode == model.GroupModeManual {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return
	}
	now := time.Now().UnixMilli()
	changed := false
	emergencyHeld := emergencyHeldLocked(route, itemID)

	// 业务成功清零提交后失败连击计数。
	if route.PostCommitStrikes[itemID] > 0 {
		delete(route.PostCommitStrikes, itemID)
		changed = true
	}

	// 候选业务确认成功说明该成员真正恢复, 清零等级并解除全部熔断痕迹与紧急封锁。
	if route.ProbeItemID == itemID || route.HalfOpens[itemID] > 0 {
		restoreItemLocked(route, itemID)
		route.ProbeItemID = 0
		delete(route.emergencyBlocks, itemID)
		if route.CurrentItemID == 0 || route.AffinityUntil <= now {
			route.CurrentItemID = itemID
			route.AffinityUntil = 0
		}
		changed = true
	}
	// 紧急来源的成功只释放占用; 非紧急来源才按亲和配置开始亲和, 使请求稳定留在备用成员上。
	if emergencyHeld {
		if releaseEmergencyLocked(route, itemID) {
			changed = true
		}
	} else if route.CurrentItemID == itemID && route.affinityArmed {
		route.affinityArmed = false
		if group.RelayConfig.MemberAffinitySeconds > 0 {
			route.AffinityUntil = now + int64(group.RelayConfig.MemberAffinitySeconds)*1000
			changed = true
		}
	}
	if changed {
		publishRouteLocked(route)
	}
}

// recordPostCommitFailure 上报一次"已提交给客户端之后才发现失败"的轮次(流中断、异常终止原因被抑制等)。
// 此类失败无法在本请求内重试, 但必须跨请求累积成员健康度: 连续达到 MemberMaxAttempts 的成员进入常规冷却,
// 让后续新请求自动故障转移, 而不是反复命中同一个坏渠道。业务成功(含探测确认)会清零该计数。
// 返回是否因达到阈值而进入了冷却, 调用方据此清除指向该成员的会话粘合。
func recordPostCommitFailure(group model.Group, itemID int) bool {
	if group.Mode == model.GroupModeManual {
		return false
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := groupRouteLocked(group)
	if _, cooling := route.Cooldowns[itemID]; cooling {
		return true
	}
	if route.PostCommitStrikes == nil {
		route.PostCommitStrikes = make(map[int]int)
	}
	route.PostCommitStrikes[itemID]++
	if route.PostCommitStrikes[itemID] < max(group.RelayConfig.MemberMaxAttempts, 1) {
		publishRouteLocked(route)
		return false
	}
	delete(route.PostCommitStrikes, itemID)
	route.Cooldowns[itemID] = time.Now().UnixMilli() + cooldownMillis(group.RelayConfig, 1)
	publishRouteLocked(route)
	return true
}

// recordRouteFailure 上报一轮业务失败, 返回是否应当立即重新选路; false 表示等待后重试同一成员。
// failures 为该成员在本请求内包含首次请求的连续业务失败次数, 由调用方累计。
// 紧急来源的失败照常计入冷却, 并同时归还紧急并发额度。
func recordRouteFailure(group model.Group, itemID, failures int) bool {
	return recordRouteFailureWithLimit(group, itemID, failures, max(group.RelayConfig.MemberMaxAttempts, 1))
}

// recordRouteFailureWithLimit 是业务失败与基础设施失败共用的路由冷却核心。
// threshold 由错误类型的独立配置决定: 业务失败取 MemberMaxAttempts, 基础设施失败取
// MemberInfraMaxRetries(0 时回退 MemberMaxAttempts)。冷却判断只使用传入阈值, 防止 infra
// 达到自身阈值后又被 MemberMaxAttempts 二次拦截, 导致第 N 次错误不能正常冷却。
func recordRouteFailureWithLimit(group model.Group, itemID, failures, threshold int) bool {
	if group.Mode == model.GroupModeManual {
		return false
	}
	threshold = max(threshold, 1)

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return false
	}
	config := group.RelayConfig

	// 候选成员的失败不再重试同一成员: 未达对应错误类型阈值保持 CLOSED 仅本请求换渠道,
	// 达到阈值则重新 OPEN 且冷却按等级加倍退避。
	if route.ProbeItemID == itemID || route.HalfOpens[itemID] > 0 {
		delete(route.HalfOpens, itemID)
		route.ProbeItemID = 0
		now := time.Now().UnixMilli()
		if threshold <= failures {
			level := route.Levels[itemID] + 1
			route.Levels[itemID] = level
			route.Cooldowns[itemID] = now + cooldownMillis(config, level)
		} else {
			delete(route.Cooldowns, itemID)
		}
		if route.CurrentItemID == itemID {
			route.CurrentItemID = 0
			route.AffinityUntil = 0
			route.affinityArmed = true
		}
		releaseEmergencyLocked(route, itemID)
		publishRouteLocked(route)
		return true
	}

	// 常规成员达到传入阈值后进入冷却, 冷却时长沿用当前等级的退避结果;
	// 紧急成员同样走此落账: 达到阈值即进入常规冷却并写入紧急封锁标记, 后续请求不再放行。
	if failures < threshold {
		if releaseEmergencyLocked(route, itemID) {
			publishRouteLocked(route)
		}
		return false
	}

	now := time.Now().UnixMilli()
	route.Cooldowns[itemID] = now + cooldownMillis(config, route.Levels[itemID])
	if emergencyHeldLocked(route, itemID) {
		if route.emergencyBlocks == nil {
			route.emergencyBlocks = make(map[int]int64)
		}
		route.emergencyBlocks[itemID] = now + cooldownMillis(config, route.Levels[itemID])
	}
	if route.CurrentItemID == itemID {
		route.CurrentItemID = 0
		route.AffinityUntil = 0
		route.affinityArmed = true
	}
	releaseEmergencyLocked(route, itemID)
	publishRouteLocked(route)
	return true
}

// memberFailureCounts 维护单个成员在本请求内的两套独立失败计数。
// 各成员的计数互不继承也互不清零: A/B 交替失败时各自累计并先后达到阈值进入冷却。
// 旧实现在成员切换时整体清零, 两个交替失败的坏成员计数永远到不了阈值,
// 导致无限轮换不熔断(实测两渠道 422/429 交替空转 33 轮的案例)。
// 同一成员在错误类型切换时只递增对应计数, 防止基础设施错误与业务失败互相串线。
type memberFailureCounts struct {
	business int
	infra    int
}

// recordRouteFailureIfReal 对本次失败做唯一一次计数并按错误类型选择独立阈值。
// 基础设施错误(SOCKS/DNS/TLS/连接重置等)累计 infra, 达到 MemberInfraMaxRetries 时
// 在第 N 次错误上立即进入正常冷却; 业务错误累计 business 并由 MemberMaxAttempts 控制。
// MemberInfraMaxRetries == 0 时退化为 MemberMaxAttempts; 小于 0 已由 Normalize 回填默认值。
func recordRouteFailureIfReal(group model.Group, itemID int, counts map[int]*memberFailureCounts, err error) bool {
	if counts[itemID] == nil {
		counts[itemID] = &memberFailureCounts{}
	}
	member := counts[itemID]
	if !isInfrastructureError(err) {
		member.business++
		return recordRouteFailure(group, itemID, member.business)
	}

	member.infra++
	limit := group.RelayConfig.MemberInfraMaxRetries
	if limit <= 0 {
		limit = group.RelayConfig.MemberMaxAttempts
	}
	return recordRouteFailureWithLimit(group, itemID, member.infra, limit)
}

// releaseRouteProbe 归还未产生成败结论的候选占用, 用于请求被人工中止或客户端断开;
// 该成员保持原冷却记录回到到期待探测状态, 由下一轮恢复流程继续处理。
// 若该成员承载着未结束的紧急请求, 则一并归还紧急并发额度。
func releaseRouteProbe(group model.Group, itemID int) {
	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return
	}
	if route.ProbeItemID == itemID || route.HalfOpens[itemID] > 0 {
		delete(route.HalfOpens, itemID)
		if route.ProbeItemID == itemID {
			route.ProbeItemID = 0
		}
		publishRouteLocked(route)
		return
	}
	if releaseEmergencyLocked(route, itemID) {
		publishRouteLocked(route)
	}
}

// refHop 是引用链上的一跳: 在 group 分组内选中了 item, item 可能仍是引用成员或最终叶子成员。
type refHop struct {
	group model.Group     // 本层分组。
	item  model.GroupItem // 本层选出的成员; 链尾必为叶子成员(非引用)。
}

// pickRefChainHop 在单个分组内完成一层选路: 会话粘合优先, 其后按模式选择。
// 各分组的路由状态按 group.ID 独立, 目标分组内部因此享有完整的三态熔断/冷却/半开语义。
// clientFormat 透传给本层 pickGroupItem, 使引用目标分组同样获得同协议优先的候选排序。
func pickRefChainHop(group model.Group, sessionKey string, exclude int, clientFormat ...llm.APIFormat) model.GroupItem {
	item := model.GroupItem{}
	if sessionStickyEnabled(group, sessionKey) {
		item = pickSessionSticky(group, sessionKey)
	}
	if item.ID == 0 {
		item = pickGroupItem(group, exclude, clientFormat...)
	}
	return item
}

// resolveGroupRefChain 从顶层分组已选出的成员出发逐层解析引用成员, 返回自顶向下的完整链路,
// 链尾即最终叶子成员及其所在分组。failedIdx 返回 -1 表示解析成功;
// 否则为链路上解析失败跳的下标: 该跳成员是引用成员但目标分组无可选成员、目标不存在、
// 出现环或超出 model.MaxGroupRefDepth 深度上限。这类失败属于结构性不可用, 调用方对齐 OmniRoute
// skipped_before_dispatch 的思想按本次请求内跳过处理: 整链(含失败跳)只做无结论的探测占用归还,
// 不把失败记到任何跳的成员身上也不上冷却, 立即改试顶层下一优先级。
// 运行期以 visited 集合加深度上限双保险防环, 即使存量数据绕过了配置校验也不会死循环。
// clientFormat 原样透传给沿途每一跳的 pickRefChainHop。
func resolveGroupRefChain(top model.Group, first model.GroupItem, sessionKey string, exclude int, clientFormat ...llm.APIFormat) (hops []refHop, failedIdx int) {
	hops = append(hops, refHop{group: top, item: first})
	visited := map[int]bool{top.ID: true}
	item := first
	for item.IsGroupRef() {
		fail := len(hops) - 1
		// 顶层自身不占深度预算, 与配置校验口径一致: 自身之下最多再经 model.MaxGroupRefDepth 个分组。
		if len(visited)-1 >= model.MaxGroupRefDepth {
			return hops, fail
		}
		next, err := groupLookupFunc(item.RefGroupName)
		if err != nil || visited[next.ID] {
			return hops, fail
		}
		visited[next.ID] = true
		nextItem := pickRefChainHop(next, sessionKey, exclude, clientFormat...)
		if nextItem.ID == 0 {
			return hops, fail
		}
		hops = append(hops, refHop{group: next, item: nextItem})
		item = nextItem
	}
	return hops, -1
}

// releaseRefChainHops 在无结论路径上按层归还整条链路的探测占用:
// 引入引用解析后每一跳都可能在所属分组持有半开候选或紧急额度, 任一失败路径都必须整链释放以免泄漏。
func releaseRefChainHops(hops []refHop) {
	for _, hop := range hops {
		releaseRouteProbe(hop.group, hop.item.ID)
	}
}

// releaseRefChainProbeHolds panic 兜底专用: 按层幂等归还引用链上的探测候选占用与半开标记,
// 供 Forward 的 recover 兜底在请求异常终止时防止占用泄漏把整组钉死。已定论的跳为无操作。
// 刻意不归还紧急并发额度, 两点权衡: ① 定论路径已归还过的额度若被兜底重复归还会多扣在飞额度;
// ② 定论前 panic 的场景下额度因此泄漏, emergencyMaxConcurrent 次后紧急兜底失效到重启——
// 这是 should-never-happen 路径上的有界损失, 换取兜底对全部持有状态严格无副作用。
// 探测占用与半开标记均为单请求持有的幂等标记, 重复归并无副作用。
func releaseRefChainProbeHolds(hops []refHop) {
	routeMu.Lock()
	defer routeMu.Unlock()
	for _, hop := range hops {
		route := routes[hop.group.ID]
		if route == nil {
			continue
		}
		if route.ProbeItemID != hop.item.ID && route.HalfOpens[hop.item.ID] == 0 {
			continue
		}
		delete(route.HalfOpens, hop.item.ID)
		if route.ProbeItemID == hop.item.ID {
			route.ProbeItemID = 0
		}
		publishRouteLocked(route)
	}
}

// cooldownMillis 计算指定冷却等级对应的冷却毫秒数: 基础冷却乘倍数的等级次方, 不超过配置上限。
func cooldownMillis(config model.GroupRelayConfig, level int) int64 {
	seconds := float64(max(config.MemberCooldownSeconds, 1))
	if level > 0 {
		multiplier := max(config.CooldownBackoffMultiplier, 1)
		seconds *= math.Pow(multiplier, float64(level))
	}
	if ceiling := float64(max(config.CooldownMaxSeconds, 0)); ceiling >= 1 && seconds > ceiling {
		seconds = ceiling
	}
	return int64(seconds * float64(time.Second/time.Millisecond))
}

// allMembersInCooldown 报告分组是否至少有一个非禁用成员, 且所有非禁用成员当前都处于未过期的冷却中。
// 用于 failover 循环在 pickGroupItem 返回空值时区分"全部冷却"与"根本没有成员"两种情况:
// 前者可自动清除冷却后立即重试, 后者只能等待人工补齐成员或冷却到期。
// 手动模式不使用冷却语义, 一律返回 false。
func allMembersInCooldown(group model.Group) bool {
	if group.Mode == model.GroupModeManual || len(group.Items) == 0 {
		return false
	}
	routeMu.Lock()
	defer routeMu.Unlock()
	route := routes[group.ID]
	if route == nil {
		return false
	}
	now := time.Now().UnixMilli()
	hasCandidate := false
	for _, item := range group.Items {
		if channelDisabledForRouting(item) {
			continue
		}
		hasCandidate = true
		deadline, ok := route.Cooldowns[item.ID]
		if !ok || deadline <= now {
			return false // 发现一个未冷却的可用成员
		}
	}
	return hasCandidate
}

// ResetGroupCooldown 清空指定分组下所有成员的冷却与相关运行时抑制状态, 并通过 SSE 立即推送更新。
// 不影响路由粘合(AffinityUntil / CurrentItemID / affinityArmed)与紧急兜底计数, 也不取消正在进行的半开探测:
// HalfOpens 保留以便在飞探测自然走完; 业务请求路径上的冷却判断会立即看到清空效果。
// 返回被清理的成员条目数(Cooldowns 与 Levels 任一非零的成员计 1), 用于前端展示反馈。
// 找到对应 groupID 时返回 (n, true); 路由状态尚未初始化时返回 (0, true) 作为无操作成功。
func ResetGroupCooldown(groupID int) (cleared int, ok bool) {
	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[groupID]
	if route == nil {
		return 0, true
	}
	// 计数: 任何一个相关条目非零即计 1 个被清理成员, 用于反馈。
	dirty := make(map[int]struct{})
	for itemID, deadline := range route.Cooldowns {
		if deadline > 0 {
			dirty[itemID] = struct{}{}
		}
	}
	for itemID, level := range route.Levels {
		if level > 0 {
			dirty[itemID] = struct{}{}
		}
	}
	for itemID, strikes := range route.PostCommitStrikes {
		if strikes > 0 {
			dirty[itemID] = struct{}{}
		}
	}
	for itemID, blockUntil := range route.emergencyBlocks {
		if blockUntil > 0 {
			dirty[itemID] = struct{}{}
		}
	}
	cleared = len(dirty)
	// 直接清空 map: 旧条目删除后即视为"无冷却", 前端按缺失键渲染;
	// 保留 map 实例, 避免 nil map 写入 panic。
	clear(route.Cooldowns)
	clear(route.Levels)
	clear(route.PostCommitStrikes)
	clear(route.emergencyBlocks)
	syncEmergencySummaryLocked(route)
	publishRouteLocked(route)
	return cleared, true
}

// RemoveGroupRoute 回收已删除分组的路由状态与会话粘合记录。
// groupRouteLocked 只清理分组仍存在但成员被删除的残留; 分组本身被删除后其
// *RouteState 会永久滞留 routes 表(组号被新建分组复用前一直占内存并出现在路由流快照)。
// 由管理端删除分组的 handler 调用。
func RemoveGroupRoute(groupID int) {
	routeMu.Lock()
	delete(routes, groupID)
	routeMu.Unlock()
	clearSessionStickyByGroup(groupID)
}

// groupRouteLocked 取出分组路由状态并清理已删除成员的残留; 调用方必须持有锁。
func groupRouteLocked(group model.Group) *RouteState {
	route := routes[group.ID]
	if route == nil {
		route = &RouteState{
			GroupID:           group.ID,
			Cooldowns:         make(map[int]int64),
			Levels:            make(map[int]int),
			HalfOpens:         make(map[int]int64),
			PostCommitStrikes: make(map[int]int),
			emergencyCounts:   make(map[int]int),
			emergencyBlocks:   make(map[int]int64),
		}
		routes[group.ID] = route
	}
	items := make(map[int]bool, len(group.Items))
	for _, item := range group.Items {
		items[item.ID] = true
	}
	for itemID := range route.Cooldowns {
		if !items[itemID] {
			delete(route.Cooldowns, itemID)
		}
	}
	for itemID := range route.Levels {
		if !items[itemID] {
			delete(route.Levels, itemID)
		}
	}
	for itemID := range route.HalfOpens {
		if !items[itemID] {
			delete(route.HalfOpens, itemID)
		}
	}
	for itemID := range route.PostCommitStrikes {
		if !items[itemID] {
			delete(route.PostCommitStrikes, itemID)
		}
	}
	for itemID := range route.emergencyCounts {
		if !items[itemID] {
			delete(route.emergencyCounts, itemID)
		}
	}
	for itemID := range route.emergencyBlocks {
		if !items[itemID] {
			delete(route.emergencyBlocks, itemID)
		}
	}
	syncEmergencySummaryLocked(route)
	if route.ProbeItemID != 0 && !items[route.ProbeItemID] {
		route.ProbeItemID = 0
	}
	if route.CurrentItemID != 0 && !items[route.CurrentItemID] {
		route.CurrentItemID = 0
		route.AffinityUntil = 0
		route.affinityArmed = false
	}
	return route
}

// itemOf 返回分组内指定 ID 的成员, 不存在时返回零值。
func itemOf(group model.Group, itemID int) model.GroupItem {
	for _, item := range group.Items {
		if item.ID == itemID {
			return item
		}
	}
	return model.GroupItem{}
}

// cloneRouteState 复制路由状态快照, 映射字段按值克隆以免前端读到后续变更。
// PostCommitStrikes 同样必须克隆: 发布的消息在锁外被 SSE 消费者序列化,
// 共享底层 map 会与锁内连击写入构成并发迭代/写入冲突直接 fatal。
func cloneRouteState(route *RouteState) RouteState {
	message := *route
	message.Cooldowns = maps.Clone(route.Cooldowns)
	message.Levels = maps.Clone(route.Levels)
	message.HalfOpens = maps.Clone(route.HalfOpens)
	message.PostCommitStrikes = maps.Clone(route.PostCommitStrikes)
	return message
}

// publishRouteLocked 非阻塞发布路由状态, 连接拥塞时关闭它并交给客户端重连获取全量快照; 调用方必须持有锁。
func publishRouteLocked(route *RouteState) {
	message := cloneRouteState(route)
	for stream := range routeStreams {
		select {
		case stream <- message:
		default:
			delete(routeStreams, stream)
			close(stream)
		}
	}
}

// OpenRouteStream 注册路由流连接, 返回全部分组的当前状态快照和后续增量通道。
func OpenRouteStream() ([]RouteState, chan RouteState) {
	routeMu.Lock()
	defer routeMu.Unlock()

	stream := make(chan RouteState, routeStreamBuffer)
	routeStreams[stream] = struct{}{}

	snapshot := make([]RouteState, 0, len(routes))
	for _, route := range routes {
		snapshot = append(snapshot, cloneRouteState(route))
	}
	return snapshot, stream
}

// CloseRouteStream 注销并关闭指定路由流连接。
func CloseRouteStream(stream chan RouteState) {
	routeMu.Lock()
	defer routeMu.Unlock()

	if _, exists := routeStreams[stream]; exists {
		delete(routeStreams, stream)
		close(stream)
	}
}
