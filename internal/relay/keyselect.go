package relay

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// keyRef 定位一个渠道 Key 的冷却记录; 渠道 ID 与 Key ID 组成进程内唯一引用。
type keyRef struct {
	ChannelID int
	KeyID     string
}

var (
	// keyCursors 按渠道保存轮询游标, 进程重启归零无害: 仅影响起始位置不影响均匀性。
	keyCursors  = make(map[int]*atomic.Uint64)
	keyCursorMu sync.Mutex

	// keyCooldowns 记录 401/403 后进入冷却的 Key 及其到期时刻, RWMutex 保护。
	keyCooldownsMu sync.RWMutex
	keyCooldowns   = make(map[keyRef]time.Time)
)

// channelKeyCursor 返回指定渠道的轮询游标, 首次访问时惰性创建。
func channelKeyCursor(channelID int) *atomic.Uint64 {
	keyCursorMu.Lock()
	defer keyCursorMu.Unlock()
	if cursor, ok := keyCursors[channelID]; ok {
		return cursor
	}
	cursor := &atomic.Uint64{}
	keyCursors[channelID] = cursor
	return cursor
}

// markChannelKeyCooldown 把指定渠道的当前 Key 标记冷却 seconds 秒;
// seconds 非法时回退默认分组配置的成员冷却时长。
func markChannelKeyCooldown(channelID int, keyID string, seconds int) {
	if seconds < 1 {
		seconds = model.DefaultGroupRelayConfig().MemberCooldownSeconds
	}
	keyCooldownsMu.Lock()
	keyCooldowns[keyRef{ChannelID: channelID, KeyID: keyID}] = time.Now().Add(time.Duration(seconds) * time.Second)
	keyCooldownsMu.Unlock()
}

// channelKeyCooling 返回指定 Key 当前是否仍在冷却中; 到期条目顺带清理。
func channelKeyCooling(channelID int, keyID string) bool {
	ref := keyRef{ChannelID: channelID, KeyID: keyID}
	keyCooldownsMu.RLock()
	deadline, cooling := keyCooldowns[ref]
	keyCooldownsMu.RUnlock()
	if !cooling {
		return false
	}
	if time.Now().Before(deadline) {
		return true
	}
	// 到期后在写锁下重新检查, 避免 TOCTOU: 另一个 goroutine 可能在我们
	// RLock→Lock 之间通过 markChannelKeyCooldown 写入了新的未来 deadline。
	keyCooldownsMu.Lock()
	if d, ok := keyCooldowns[ref]; ok && !time.Now().Before(d) {
		delete(keyCooldowns, ref)
	}
	keyCooldownsMu.Unlock()
	return false
}

// selectChannelKey 返回本轮应使用的 Key 下标与生效 Key 条目; 全部不可用时返回 false。
// 渠道未配置多 Key 时构造临时条目回退旧 Channel.Key 字段, 下游走同一路径;
// 轮询以原子游标领取票号保证并发下各请求尽量错开, 冷却中的 Key 按环序跳过。
func selectChannelKey(channel model.Channel) (index int, effective model.ChannelKey, ok bool) {
	if len(channel.Keys) == 0 {
		return 0, model.ChannelKey{ID: "", Key: channel.Key}, true
	}
	// 先原子领取唯一票号再按环序扫描, 并发请求不会固定命中同一下标。
	ticket := channelKeyCursor(channel.ID).Add(1)
	start := int((ticket - 1) % uint64(len(channel.Keys)))
	for offset := 0; offset < len(channel.Keys); offset++ {
		idx := (start + offset) % len(channel.Keys)
		candidate := channel.Keys[idx]
		if channelKeyCooling(channel.ID, candidate.ID) {
			continue
		}
		return idx, candidate, true
	}
	return 0, model.ChannelKey{}, false
}

// selectProbeChannelKey 为辅助路径(半开/后台探测/TestChannel)固定选第一把健康 Key。
// 与业务轮询不同, 探测无需分散负载; 旧式单 Key 仍回退 Channel.Key。
func selectProbeChannelKey(channel model.Channel) (index int, effective model.ChannelKey, ok bool) {
	if len(channel.Keys) == 0 {
		return 0, model.ChannelKey{ID: "", Key: channel.Key}, true
	}
	for idx, candidate := range channel.Keys {
		if channelKeyCooling(channel.ID, candidate.ID) {
			continue
		}
		return idx, candidate, true
	}
	return 0, model.ChannelKey{}, false
}

var (
	errAllChannelKeysCooling = errors.New("all keys cooling")
	errProxyTemplateInvalid  = errors.New("proxy template invalid")
	errChannelDisabled       = errors.New("channel disabled")
	errWarpProbeUnsupported  = errors.New("warp proxy probe unsupported")
)

// effectiveChannelForKey 构造单次上游调用使用的渠道副本: 写入所选 Key, 并用「渠道 ID + Key ID」
// 确定性派生的别名解析 ChannelProxy 中的 {account}, 同一渠道同一 Key 每次请求别名一致。
// Remark 仅用于展示, 从不参与代理替换。
// WARP 渠道保留占位符交给选槽。
// 代理解析失败只返回固定哨兵, 不透出模板原文或底层 URL 错误, 防止 userinfo 中的凭据泄漏。
func effectiveChannelForKey(channel model.Channel, selected model.ChannelKey) (model.Channel, error) {
	effective := channel
	effective.Key = selected.Key
	// Runtime 生效副本只暴露本次选中的单一凭据: buildOutbound/conversionMiddleware 都通过
	// PrimaryKey() 读取认证, 若保留原 Keys 列表会永远优先拿 Keys[0], 使轮询选中的 Key 失效。
	effective.Keys = nil
	if effective.ChannelProxy == nil || !model.HasAccountPlaceholder(*effective.ChannelProxy) {
		return effective, nil
	}
	if isWarpProxy(channel) {
		// WARP 占位符交由 relay 选槽。
		return effective, nil
	}
	resolvedProxy, err := helper.ResolveProxyTemplate(*effective.ChannelProxy, helper.AccountAliasFor(channel.ID, selected.ID))
	if err != nil {
		return model.Channel{}, errProxyTemplateInvalid
	}
	resolvedAddr := resolvedProxy.String()
	effective.ChannelProxy = &resolvedAddr
	return effective, nil
}

// effectiveProbeChannel 构造半开/后台探测/TestChannel 共用的生效渠道副本。
// 辅助路径固定第一把健康 Key, 但代理账号必须使用同一条 Key 的 Account/ID, 不得使用 Remark。
// 停用渠道的探测与 TestChannel 同样应被阻止, 否则后台探测会无限消耗
// 冷却 / 并发 / RPM 信号量, 拉低整链路健康度。仅对真实 ID 的渠道检查, 避免
// 影响单元测试里直接构造的 ID=0 占位 channel。
// WARP 槽位返回哨兵错误。
func effectiveProbeChannel(channel model.Channel) (model.Channel, int, model.ChannelKey, error) {
	if isWarpProxy(channel) {
		return model.Channel{}, 0, model.ChannelKey{}, errWarpProbeUnsupported
	}
	if channel.ID > 0 && !channel.Enabled {
		return model.Channel{}, 0, model.ChannelKey{}, errChannelDisabled
	}
	index, selected, ok := selectProbeChannelKey(channel)
	if !ok {
		return model.Channel{}, 0, model.ChannelKey{}, errAllChannelKeysCooling
	}
	effective, err := effectiveChannelForKey(channel, selected)
	if err != nil {
		return model.Channel{}, index, selected, err
	}
	return effective, index, selected, nil
}

// channelKeyLabel 返回面板展示用的 Key 标签: "#"+序号+"("+显示名+")"。
// 显示名优先级: Remark(用户备注, 展示友好) > ID(稳定标识);
// 旧式单 Key 回退条目无序号与别名, 返回空串。
func channelKeyLabel(index int, key model.ChannelKey) string {
	if key.ID == "" && key.Remark == "" {
		return ""
	}
	display := key.Remark
	if display == "" {
		display = key.ID
	}
	return "#" + strconv.Itoa(index+1) + "(" + display + ")"
}

// upstreamErrorStatusCode 从错误中提取上游 HTTP 状态码:
// 先取结构化错误(httpclient.Error), 取不到时用 state.go 的状态码正则与短语反查从错误文本提炼。
func upstreamErrorStatusCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if code, ok := UpstreamStatusCode(err); ok {
		return code, true
	}
	return 0, false
}

// isAuthRejectionError 判断错误是否为上游对当前凭据的认证拒绝(401/403)。
func isAuthRejectionError(err error) bool {
	code, ok := upstreamErrorStatusCode(err)
	return ok && (code == http.StatusUnauthorized || code == http.StatusForbidden)
}

// isKeyRateLimitError 判断错误是否为上游对当前凭据的限速/配额拒绝(429):
// 月度/周度用量限额等对单把 Key 是持续性不可用, 冷却几分钟解冻后仍会立刻再撞,
// 多 Key 渠道应与认证拒绝一样轮换到下一把 Key, 而不是反复重试同一把。
func isKeyRateLimitError(err error) bool {
	code, ok := upstreamErrorStatusCode(err)
	return ok && code == http.StatusTooManyRequests
}

// PruneEphemeralState 回收进程内过期的 Key 冷却、会话粘合与脱敏映射。
func PruneEphemeralState() {
	PruneExpiredKeyCooldowns()
	PruneExpiredSessionStickies()
	pruneMaskSessions()
}

// PruneExpiredKeyCooldowns 删除已到期的 Key 冷却条目, 避免只读惰性清理漏掉不再被选中的 Key。
func PruneExpiredKeyCooldowns() int {
	now := time.Now()
	keyCooldownsMu.Lock()
	defer keyCooldownsMu.Unlock()
	removed := 0
	for ref, deadline := range keyCooldowns {
		if !now.Before(deadline) {
			delete(keyCooldowns, ref)
			removed++
		}
	}
	return removed
}

// resetChannelKeyHealth 清空轮询游标与冷却记录; 仅供测试隔离包级全局状态。
func resetChannelKeyHealth() {
	keyCursorMu.Lock()
	keyCursors = make(map[int]*atomic.Uint64)
	keyCursorMu.Unlock()
	keyCooldownsMu.Lock()
	keyCooldowns = make(map[keyRef]time.Time)
	keyCooldownsMu.Unlock()
}

// ClearChannelKeyStateForUI 清空指定渠道的全部 Key 冷却记录与限速窗口, 并返回清理条数, 供分组清理冷却接口使用。
// 行为与 CleanupChannelKeyState 一致, 但同时上报两类状态各自的条目数, 便于前端展示。
func ClearChannelKeyStateForUI(channelID int) (cooldowns int, rateWindows int) {
	keyCooldownsMu.Lock()
	for ref := range keyCooldowns {
		if ref.ChannelID == channelID {
			delete(keyCooldowns, ref)
			cooldowns++
		}
	}
	keyCooldownsMu.Unlock()

	rateWindows = cleanupChannelLimits(channelID)
	return cooldowns, rateWindows
}

// CleanupChannelKeyState 清除指定渠道的全部轮询游标、Key 冷却记录与限速状态
// (并发信号量及 RPM 滑动窗口, 见 channellimit.go)。
// 渠道删除时调用, 防止反复增删渠道导致条目永久累积; 同 ID 重建渠道也不会
// 被残留的冷却窗口误伤。
func CleanupChannelKeyState(channelID int) {
	keyCursorMu.Lock()
	delete(keyCursors, channelID)
	keyCursorMu.Unlock()

	keyCooldownsMu.Lock()
	for ref := range keyCooldowns {
		if ref.ChannelID == channelID {
			delete(keyCooldowns, ref)
		}
	}
	keyCooldownsMu.Unlock()

	cleanupChannelLimits(channelID)
}
