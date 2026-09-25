package relay

// 渠道级 RPM 限速与并发上限:
//   - 并发按渠道 ID 维护一个连续的 active 计数与动态 limit, 在
//     sendPassthrough/sendConverted 发起上游前领取、结束后归还;
//   - RPM 滑动窗口按渠道+Key 记录最近一分钟内的放行时刻, 达到上限时
//     阻塞等待最早的时间戳滑出窗口(即最近的到期时刻)再放行。
//
// 并发上限的关键性质: active 计数在配置变更(升额/降额/不限/重新限额)期间
// 始终连续累加, 绝不因 limit 变化而替换计数容器。因此降额不会"遗忘"旧在途
// 请求——旧请求自然归还使 active 回落, 在 active 降到新 limit 之前新请求
// 阻塞等待, 无法通过换代绕过限制(见 STA-13)。
//
// 全部状态由同一把互斥锁保护: 临界区只有 map 读写与短切片扫描, 无 IO,
// 单锁足以保证正确性并避免多锁顺序问题。渠道删除时经 CleanupChannelKeyState 清理。

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errChannelConcurrencyFull 渠道并发槽位已满且 1 秒等待超时。
// 属本地准入拒绝而非上游故障: 调用方(handler)在失败记账前以 ErrClassChannelBusy
// 短路, 不计入成员业务失败连击, 也不进入成员冷却或清除会话粘合。
var errChannelConcurrencyFull = errors.New("channel concurrency limit reached")

// channelRPMWindow 滑动窗口长度; 变量以便测试注入短窗口。
var channelRPMWindow = time.Minute

// channelConcurrencyState 单个渠道的连续并发计数状态。
// active 在请求生命周期内只增不减(领取 +1 / 归还 -1), limit 变化时不重置,
// 从而保证降额期间旧在途请求仍被计入, 新请求无法绕过限制。
type channelConcurrencyState struct {
	// mu 保护 active 与 wake; 与包级 channelLimitMu 相互独立, 临界区无 IO。
	mu sync.Mutex
	// active 当前持有槽位的请求数; 归还幂等保证不会低于 0。
	active int
	// wake 当前唤醒代: 归还槽位时关闭并重建, 等待中的 acquire 据此被唤醒重试。
	// 始终为非 nil 的未关闭 channel, 关闭与重建均在 st.mu 下成对进行, 不会重复关闭。
	wake chan struct{}
}

// newChannelConcurrencyState 创建初始状态: active=0, 唤醒代已就绪。
func newChannelConcurrencyState() *channelConcurrencyState {
	return &channelConcurrencyState{wake: make(chan struct{})}
}

// release 归还一个槽位: active 递减(不低于 0)并唤醒所有等待者。
// 由 acquire 返回的幂等闭包调用, 不会因重复调用而把 active 推到负值。
func (st *channelConcurrencyState) release() {
	st.mu.Lock()
	if st.active > 0 {
		st.active--
	}
	// 关闭当前唤醒代并开启新一代: 正在 select 等待的 acquire 会被唤醒重试。
	close(st.wake)
	st.wake = make(chan struct{})
	st.mu.Unlock()
}

var (
	// channelLimitMu 保护以下两张表; 所有访问必须持锁。
	channelLimitMu sync.Mutex

	// channelConcurrencyStates 按渠道保存连续并发计数状态。
	// 配置变更时只更新传入的 limit(由 acquire 即时生效), 绝不替换状态对象,
	// 因此旧在途请求的归还仍作用于同一 active 计数, 不会与在途生命周期脱节。
	// 渠道删除时整体移除条目: 已发放槽位的归还闭包持有原状态对象, 不依赖本表
	// 存活, 直接删除是安全的; 同 ID 重建会得到全新的零计数状态。
	channelConcurrencyStates = make(map[int]*channelConcurrencyState)

	// channelKeyWindows 按 渠道+Key 记录窗口内的放行时刻, 时间升序; 长度不超过 RPM 上限。
	channelKeyWindows = make(map[keyRef][]time.Time)
)

// acquireChannelConcurrency 领取指定渠道的一个并发槽位, 返回归还函数。
// limit 非正值表示不限制, 直接返回空操作。槽位满载时阻塞等待(总预算 1 秒),
// ctx 结束(客户端断开/管理端终止/成员超时)立即返回 ctx 错误, 超时返回
// errChannelConcurrencyFull。被 ctx/超时打断的请求不会占用槽位。
//
// limit 即时生效: 升额后新请求立即按更宽的 limit 放行; 降额后旧在途请求
// 不被杀死, 但在 active 回落到新 limit 之前新请求阻塞等待, 无法绕过。
// 返回的归还函数幂等: 多次调用只递减 active 一次, 避免在多个清理路径都尝试
// 释放同一份槽位时把 active 推回低于实际持有量, 导致渠道并发超额。
func acquireChannelConcurrency(ctx context.Context, channelID int, limit int) (func(), error) {
	if limit <= 0 {
		return func() {}, nil
	}
	channelLimitMu.Lock()
	st, ok := channelConcurrencyStates[channelID]
	if !ok {
		st = newChannelConcurrencyState()
		channelConcurrencyStates[channelID] = st
	}
	channelLimitMu.Unlock()

	// 总等待预算 1 秒: 跨多次唤醒重试累计不超过 1 秒, 与原单次 select 语义一致。
	deadline := time.Now().Add(time.Second)
	for {
		st.mu.Lock()
		if st.active < limit {
			st.active++
			st.mu.Unlock()
			var once sync.Once
			return func() { once.Do(st.release) }, nil
		}
		// 槽位已满: 记下当前唤醒代后释放锁, 等待归还或 ctx/超时。
		wake := st.wake
		st.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return func() {}, errChannelConcurrencyFull
		}
		timer := time.NewTimer(remaining)
		select {
		case <-wake:
			// 某请求归还了槽位(或唤醒代被重建), 回到循环重新竞争放行。
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return func() {}, ctx.Err()
		case <-timer.C:
			return func() {}, errChannelConcurrencyFull
		}
	}
}

// waitChannelRPM 渠道级单 Key RPM 门禁: 窗口内未达上限则记录当前时刻并立即返回;
// 已达上限时计算最近一次名额腾出的到期时刻, 以 timer 与 ctx 双路 select 阻塞等待后重试。
// rpm 非正值表示不限制, 同时清除遗留窗口避免条目滞留。
// stopCh 关闭时立即返回 errAdminStopped, 供调用方以取消终态定稿而非继续等待名额。
func waitChannelRPM(ctx context.Context, channelID int, keyID string, rpm int, stopCh <-chan struct{}) error {
	ref := keyRef{ChannelID: channelID, KeyID: keyID}
	if rpm <= 0 {
		channelLimitMu.Lock()
		delete(channelKeyWindows, ref)
		channelLimitMu.Unlock()
		return nil
	}
	for {
		wait, ok := tryChannelRPMPermit(ref, rpm)
		if ok {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			// 名额到期, 回到循环重新竞争放行。
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-stopCh:
			timer.Stop()
			return errAdminStopped
		}
	}
}

// tryChannelRPMPermit 尝试为 ref 记录一次放行: 成功返回 (0, true);
// 达到上限时返回 (等待时长, false), 即第 len-rpm+1 个时间戳滑出窗口的剩余时长。
// 记录前先清理已滑出窗口的旧条目。
func tryChannelRPMPermit(ref keyRef, rpm int) (time.Duration, bool) {
	now := time.Now()
	cutoff := now.Add(-channelRPMWindow)
	channelLimitMu.Lock()
	defer channelLimitMu.Unlock()

	window := channelKeyWindows[ref]
	start := 0
	for start < len(window) && !window[start].After(cutoff) {
		start++
	}
	window = window[start:]
	if len(window) >= rpm {
		// 窗口内已有 rpm 个放行: 需等到足够多的时间戳滑出窗口腾出名额,
		// 最早的可用时刻即 window[len-rpm] 加满一个窗口周期。
		admitAt := window[len(window)-rpm].Add(channelRPMWindow)
		channelKeyWindows[ref] = window
		return admitAt.Sub(now), false
	}
	channelKeyWindows[ref] = append(window, now)
	return 0, true
}

// cleanupChannelLimits 清除指定渠道的并发计数状态与全部 Key 的 RPM 窗口。
// 由 CleanupChannelKeyState 在渠道删除时调用, 已发放槽位的归还闭包持有原状态对象,
// 不依赖本表的存活, 因此直接删除条目是安全的。
// 返回被清理的 RPM 窗口条目数, 供 ClearChannelKeyStateForUI 汇总统计使用。
func cleanupChannelLimits(channelID int) int {
	channelLimitMu.Lock()
	defer channelLimitMu.Unlock()
	delete(channelConcurrencyStates, channelID)
	cleared := 0
	for ref := range channelKeyWindows {
		if ref.ChannelID == channelID {
			delete(channelKeyWindows, ref)
			cleared++
		}
	}
	return cleared
}

// resetChannelLimits 清空全部限速状态; 仅供测试隔离包级全局状态。
func resetChannelLimits() {
	channelLimitMu.Lock()
	channelConcurrencyStates = make(map[int]*channelConcurrencyState)
	channelKeyWindows = make(map[keyRef][]time.Time)
	channelLimitMu.Unlock()
}
