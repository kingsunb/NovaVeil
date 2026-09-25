package relay

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// errChannelModelUnavailable 表示成员无法定位到渠道模型(引用成员或模型刚被删除), 不能发起合成探测。
var errChannelModelUnavailable = errors.New("channel model unavailable")

// runProbeSafely 执行一次合成探测并兜住 panic, 是探测 goroutine 的唯一 recover 防线:
// 探测实现(含渠道回调)抛出的 panic 一律转为错误返回并记录栈。两处调用场景都依赖"必有结论"——
// 批量探测的收集器按候选数死等 results, 缺一个结果整个恢复流程悬挂;
// 异步探测的 goroutine 无上层 recover, panic 直接击穿网关进程, 且成员会钉死在 HALF_OPEN 被选路永久跳过。
func runProbeSafely(ctx context.Context, channel model.Channel, modelName string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("probe panic: %v", r)
			log.Errorf("relay probe panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return probeChannelFunc(ctx, channel, modelName)
}

// claimHalfOpenLocked 探测占用的唯一判定入口: 仅当成员处于 OPEN 且冷却已到期且未被任何路径占用时,
// 才原子将其置入 HalfOpens。请求触发异步探测、后台定时探测与恢复流程全部经由此处判定,
// 保证同一成员任意时刻只会被一路探测。调用方必须持有 routeMu, 返回是否完成本次占用。
func claimHalfOpenLocked(route *RouteState, itemID int, now int64) bool {
	// A recovered candidate must complete its real business confirmation before another
	// member enters HALF_OPEN. Otherwise a late request can start a second recovery batch.
	if route.ProbeItemID != 0 {
		return false
	}
	deadline, cooling := route.Cooldowns[itemID]
	if !cooling || deadline > now || route.HalfOpens[itemID] > 0 {
		return false
	}
	route.HalfOpens[itemID] = now
	return true
}

// ensureProbeLocked 异步非阻塞探测的唯一入口: 占用判定通过后在临界区内完成 HALF_OPEN 切换并立即返回,
// 合成测试在独立 goroutine 中执行, 结论由 runAsyncProbe 写回全局状态。
// 调用方必须持有 routeMu, 状态变更后由调用方统一发布路由流; 返回是否真正发起了新探测。
func ensureProbeLocked(route *RouteState, group model.Group, item model.GroupItem, now int64) bool {
	if !claimHalfOpenLocked(route, item.ID, now) {
		return false
	}
	go runAsyncProbe(group, item)
	return true
}

// ProbeExpiredItems 后台定时探测入口: 仅对启用后台探测的故障转移分组生效, 把处于 OPEN 且冷却已到期
// 且未被占用的成员逐个交由 ensureProbeLocked 发起异步合成探测, 立即返回不阻塞调用方。
// 分组配置以调用方读到的最新值为准, 运行期变更在下次检查时自然生效。
func ProbeExpiredItems(group model.Group) {
	if group.Mode != model.GroupModeFailover || !group.RelayConfig.BackgroundProbeEnabled {
		return
	}
	now := time.Now().UnixMilli()
	started := false
	routeMu.Lock()
	route := groupRouteLocked(group)
	for _, item := range group.Items {
		if ensureProbeLocked(route, group, item, now) {
			started = true
		}
	}
	if started {
		publishRouteLocked(route)
	}
	routeMu.Unlock()
}

// runAsyncProbe 执行单成员合成测试并把结论写回全局状态: 成功恢复 CLOSED 并清除全部熔断痕迹,
// 失败重新 OPEN 且等级加一、冷却按倍数指数退避。占用已被业务路径处理时本结论作废。
func runAsyncProbe(group model.Group, item model.GroupItem) {
	timeout := group.RelayConfig.MemberNonStreamResponseTimeoutSeconds
	if timeout < 1 {
		timeout = model.DefaultGroupRelayConfig().MemberNonStreamResponseTimeoutSeconds
	}
	channelModel := item.ChannelModel
	var channel model.Channel
	var err error
	if channelModel == nil {
		// 引用成员或渠道模型刚被删除: 无从探测, 直接按失败结论写回状态。
		err = errChannelModelUnavailable
	} else {
		channel, err = channelLookupFunc(channelModel.ChannelID)
		if err == nil {
			channel, _, _, err = effectiveProbeChannel(channel)
		}
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		err = runProbeSafely(ctx, channel, channelModel.Name)
		cancel()
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil || route.HalfOpens[item.ID] == 0 {
		return
	}
	if err != nil {
		reopenItemLocked(route, group.RelayConfig, item.ID, time.Now().UnixMilli())
	} else {
		restoreItemLocked(route, item.ID)
	}
	publishRouteLocked(route)
}

// restoreItemLocked 恢复 CLOSED 的统一落账: 解除冷却等级与探测占用。调用方必须持有 routeMu。
func restoreItemLocked(route *RouteState, itemID int) {
	delete(route.Cooldowns, itemID)
	delete(route.Levels, itemID)
	delete(route.HalfOpens, itemID)
}

// reopenItemLocked 探测失败重新 OPEN 的统一落账: 等级加一, 冷却按倍数退避且不超过上限。
// 调用方必须持有 routeMu。
func reopenItemLocked(route *RouteState, config model.GroupRelayConfig, itemID int, now int64) {
	level := route.Levels[itemID] + 1
	route.Levels[itemID] = level
	route.Cooldowns[itemID] = now + cooldownMillis(config, level)
	delete(route.HalfOpens, itemID)
}
