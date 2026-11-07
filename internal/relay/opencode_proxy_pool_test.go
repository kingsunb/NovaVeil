package relay

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
)

// TestProxyNeedsOpencodeAccountKeyed keyed 免费渠道配置 {account} 代理后仍走 IP 账号池。
func TestProxyNeedsOpencodeAccountKeyed(t *testing.T) {
	proxy := "socks5h://Default.{account}:token@resin.test:2260"
	base := model.Channel{
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/inference",
		ChannelProxy:   &proxy,
	}
	assert.True(t, proxyNeedsOpencodeAccount(base), "普通免费渠道应进入 IP 账号池")

	keyed := base
	keyed.Keyed = true
	assert.True(t, proxyNeedsOpencodeAccount(keyed), "keyed 渠道配置 {account} 后应按 Key 进入 IP 账号池")

	keyedNoProxy := keyed
	keyedNoProxy.ChannelProxy = nil
	assert.False(t, proxyNeedsOpencodeAccount(keyedNoProxy), "keyed 渠道未配置代理时不进入账号池")

	notPlaceholder := base
	plainProxy := "http://127.0.0.1:7890"
	notPlaceholder.ChannelProxy = &plainProxy
	assert.False(t, proxyNeedsOpencodeAccount(notPlaceholder), "没有 {account} 的代理不走账号池")

	notFree := base
	notFree.OpencodeCompat = false
	assert.False(t, proxyNeedsOpencodeAccount(notFree), "非免费档不走账号池")
}

// TestOcProxyPoolForKeyIsolation 验证同渠道不同 Key 的账号池互不共用。
func TestOcProxyPoolForKeyIsolation(t *testing.T) {
	const channelID = 980006
	poolA := ocProxyPoolForKey(channelID, "key-a")
	poolA.loaded = true
	poolA.accounts["key-a"] = &ocProxyAccount{account: "key-a", verified: true, lastSuccessAt: time.Now()}
	poolB := ocProxyPoolForKey(channelID, "key-b")
	poolB.loaded = true
	poolB.accounts["key-b"] = &ocProxyAccount{account: "key-b", verified: true, lastSuccessAt: time.Now()}
	keyA := ocProxyPoolKey{channelID: channelID, keyID: "key-a"}
	keyB := ocProxyPoolKey{channelID: channelID, keyID: "key-b"}
	t.Cleanup(func() {
		ocProxyKeyPools.Delete(keyA)
		ocProxyKeyPools.Delete(keyB)
	})

	ctx := context.Background()
	assert.Equal(t, "key-a", pickOpencodeProxyAccountForKey(ctx, channelID, "key-a", "s1"))
	assert.Equal(t, "key-b", pickOpencodeProxyAccountForKey(ctx, channelID, "key-b", "s2"))
	releaseOpencodeProxyAccountForKey(channelID, "key-a", "key-a", "s1")
	releaseOpencodeProxyAccountForKey(channelID, "key-b", "key-b", "s2")
}

// TestOcProxyFixedAccountSingleSession 验证 keyed 渠道只使用固定 Key.ID 账号,
// 不同活跃会话不会生成额外账号, 会话释放后同一账号可复用。
func TestOcProxyFixedAccountSingleSession(t *testing.T) {
	const channelID = 980007
	const keyID = "0123456789abcdef"
	poolKey := ocProxyPoolKey{channelID: channelID, keyID: keyID}
	t.Cleanup(func() { ocProxyKeyPools.Delete(poolKey) })
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.loaded = true

	ctx := context.Background()
	assert.Equal(t, keyID, pickOpencodeProxyAccountForKey(ctx, channelID, keyID, "s1"))
	assert.Equal(t, "", pickOpencodeProxyAccountForKey(ctx, channelID, keyID, "s2"))

	pool.mu.Lock()
	assert.Len(t, pool.accounts, 1)
	_, ok := pool.accounts[keyID]
	pool.mu.Unlock()
	assert.True(t, ok, "keyed 渠道不能生成固定账号之外的账号")

	releaseOpencodeProxyAccountForKey(channelID, keyID, keyID, "s1")
	assert.Equal(t, keyID, pickOpencodeProxyAccountForKey(ctx, channelID, keyID, "s2"))
	releaseOpencodeProxyAccountForKey(channelID, keyID, keyID, "s2")
}

// TestTrimLockedEvictsUnverifiedFirst 内存满额淘汰与库内同口径:
// 先清从未成功过的账号, 哪怕已验证号更久未活跃或未成功号仍在冷却。
func TestTrimLockedEvictsUnverifiedFirst(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{accounts: make(map[string]*ocProxyAccount, ocProxyMaxAccounts+3)}
	for i := 0; len(pool.accounts) < ocProxyMaxAccounts-1; i++ {
		account := "filler" + strconv.Itoa(i)
		pool.accounts[account] = &ocProxyAccount{account: account, verified: true, lastSuccessAt: now}
	}
	pool.accounts["stale-unverified"] = &ocProxyAccount{account: "stale-unverified", cooldownUntil: now.Add(24 * time.Hour)}
	pool.accounts["old-unverified"] = &ocProxyAccount{account: "old-unverified"}
	pool.accounts["stale-historic"] = &ocProxyAccount{account: "stale-historic", verified: true, lastSuccessAt: now.Add(-6 * 24 * time.Hour)}

	pool.trimLocked("")

	assert.Len(t, pool.accounts, ocProxyMaxAccounts)
	assert.NotContains(t, pool.accounts, "old-unverified", "未成功号按最旧先清")
	assert.NotContains(t, pool.accounts, "stale-unverified", "冷却中的未成功号仍先于历史号被清")
	assert.Contains(t, pool.accounts, "stale-historic", "历史可用号优先保留")
}

// TestOcProxyEvictFirstOrder 锁定淘汰优先级契约: 未成功先清, 同组最久未活跃先清。
func TestOcProxyEvictFirstOrder(t *testing.T) {
	now := time.Now()
	untested := &ocProxyAccount{account: "untested"}
	cooling := &ocProxyAccount{account: "cooling", cooldownUntil: now.Add(24 * time.Hour)}
	historicOld := &ocProxyAccount{account: "historic-old", verified: true, lastSuccessAt: now.Add(-6 * 24 * time.Hour)}
	historicFresh := &ocProxyAccount{account: "historic-fresh", verified: true, lastSuccessAt: now}

	assert.True(t, ocProxyEvictFirst(untested, historicOld))
	assert.True(t, ocProxyEvictFirst(cooling, historicOld))
	assert.True(t, ocProxyEvictFirst(untested, cooling))
	assert.True(t, ocProxyEvictFirst(historicOld, historicFresh))
	assert.False(t, ocProxyEvictFirst(historicOld, untested))
}

// TestOpencodeRejectCooldown 覆盖冷却时长的边界: 上游秒数优先, 缺失或不可信时
// 回落到下一个 UTC 零点(免费档配额按 UTC 天重置), 上限 24 小时。
func TestOpencodeRejectCooldown(t *testing.T) {
	now := time.Date(2026, 10, 25, 20, 16, 28, 0, time.UTC)
	toMidnight := 3*time.Hour + 43*time.Minute + 32*time.Second
	cases := []struct {
		name       string
		retryAfter string
		want       time.Duration
	}{
		{"上游秒数", "3600", time.Hour},
		{"秒数带空白", " 3600 ", time.Hour},
		{"超上限钳到 24 小时", "259200", 24 * time.Hour},
		{"HTTP 日期回落到零点", "Wed, 21 Oct 2026 07:28:00 GMT", toMidnight},
		{"空值回落到零点", "", toMidnight},
		{"零秒回落到零点", "0", toMidnight},
		{"负值回落到零点", "-1", toMidnight},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			assert.Equal(t, item.want, opencodeRejectCooldown(item.retryAfter, now))
		})
	}
}

// 同一会话粘合同一账号: 账号同时只承载一个活跃会话, 不同活跃会话不共用;
// 粘合账号失效才换号并改绑, 改绑后不回跳。
func TestOcProxyPoolPickSessionSticky(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{
		accounts: map[string]*ocProxyAccount{
			"a": {account: "a", verified: true, lastSuccessAt: now},
			"b": {account: "b", verified: true, lastSuccessAt: now.Add(-time.Hour)},
			"c": {account: "c", verified: true, lastSuccessAt: now.Add(-2 * time.Hour)},
		},
		sessionBound: make(map[string]ocSessionBinding),
	}

	first := pool.pickLocked(now, "s1")
	assert.Equal(t, "a", first.account, "首次应选最近成功账号")
	pool.accounts["a"].sessions = map[string]int{"s1": 1}
	second := pool.pickLocked(now, "s1")
	assert.Equal(t, first.account, second.account, "同会话应粘合同一账号")

	third := pool.pickLocked(now, "s2")
	assert.Equal(t, "b", third.account, "不同活跃会话应使用不同账号")
	pool.accounts["b"].sessions = map[string]int{"s2": 1}

	// 粘合账号失效: 冷却 a 后 s1 应换到空闲的 c 并改绑。
	pool.accounts["a"].cooldownUntil = now.Add(time.Hour)
	fourth := pool.pickLocked(now, "s1")
	assert.Equal(t, "c", fourth.account, "粘合账号失效应换到空闲账号")
	assert.Equal(t, "c", pool.sessionBound["s1"].account, "换号后应改绑新账号")
	pool.accounts["c"].sessions = map[string]int{"s1": 1}

	// 旧账号恢复可用也不回跳(粘合已改绑)。
	pool.accounts["a"].cooldownUntil = time.Time{}
	fifth := pool.pickLocked(now, "s1")
	assert.Equal(t, "c", fifth.account, "改绑后不回跳旧账号")
}

func TestOcProxySessionAvailable(t *testing.T) {
	active := &ocProxyAccount{sessions: map[string]int{"s1": 1}}
	assert.True(t, ocProxySessionAvailable(active, "s1"), "同会话可复用")
	assert.False(t, ocProxySessionAvailable(active, "s2"), "不同活跃会话不得共用")
	assert.True(t, ocProxySessionAvailable(active, ""), "探测空会话不参与独占")
	assert.True(t, ocProxySessionAvailable(&ocProxyAccount{}, "s2"), "空闲账号可用")
}

func TestReleaseOpencodeProxyAccountSession(t *testing.T) {
	const channelID = 980005
	pool := ocProxyPoolFor(channelID)
	pool.accounts["a"] = &ocProxyAccount{account: "a", active: 2, sessions: map[string]int{"s1": 2}}
	t.Cleanup(func() { ocProxyPools.Delete(channelID) })

	releaseOpencodeProxyAccount(channelID, "a", "s1")
	assert.Equal(t, 1, pool.accounts["a"].active)
	assert.Equal(t, 1, pool.accounts["a"].sessions["s1"])

	releaseOpencodeProxyAccount(channelID, "a", "s1")
	assert.Zero(t, pool.accounts["a"].active)
	assert.Empty(t, pool.accounts["a"].sessions)
}

// 两个不同会话在 24 小时窗口内报 429 才冷却, 重复会话不计票, 过期票作废。
func TestOcProxyRateLimitedQuorum(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{accounts: map[string]*ocProxyAccount{}}

	assert.False(t, pool.recordRateLimitedLocked("s1", now), "单会话不应冷却")
	assert.False(t, pool.recordRateLimitedLocked("s1", now), "重复会话不计票")
	assert.True(t, pool.recordRateLimitedLocked("s2", now), "两个不同会话应触发冷却")
	assert.Empty(t, pool.rateLimited, "触发后计票应清零")

	fresh := &ocProxyPool{accounts: map[string]*ocProxyAccount{}}
	fresh.recordRateLimitedLocked("s1", now)
	assert.False(t, fresh.recordRateLimitedLocked("s2", now.Add(ocProxy429VoteWindow+time.Minute)), "窗口过期后旧票不应累计")
}

func accountAt(name string, failures int, verified bool, success time.Time) *ocProxyAccount {
	return &ocProxyAccount{
		account:             name,
		consecutiveFailures: failures,
		verified:            verified,
		lastSuccessAt:       success,
	}
}

// 连续失败少者优先, 同级已验证优先, 再按最近成功时间新者优先。
func TestOcProxyCandidatePrefersFewerFailures(t *testing.T) {
	now := time.Now()
	fresh := accountAt("fresh", 0, false, time.Time{})
	failed := accountAt("failed", 1, true, now)
	older := accountAt("older", 0, true, now.Add(-time.Hour))
	newer := accountAt("newer", 0, true, now)
	unverified := accountAt("unverified", 0, false, time.Time{})

	assert.True(t, ocProxyCandidateBetter(fresh, failed), "失败一次应让位于零失败号")
	assert.True(t, ocProxyCandidateBetter(newer, older), "同级已验证号选最近成功")
	assert.True(t, ocProxyCandidateBetter(older, unverified), "零失败时已验证仍优先于未验证")
	assert.False(t, ocProxyCandidateBetter(failed, fresh))
}

// 选号时失败过的已验证号让位于零失败的未验证新号。
func TestOcProxyPickPrefersZeroFailure(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{accounts: map[string]*ocProxyAccount{
		"proven": accountAt("proven", 1, true, now),
		"new":    accountAt("new", 0, false, time.Time{}),
	}}
	assert.Equal(t, "new", pool.pickLocked(now, "").account)
}

// 真实转发成功与体检成功都清连续失败, 都不清窗口。
func TestMarkSuccessClearsConsecutiveNotWindow(t *testing.T) {
	for _, probe := range []bool{false, true} {
		pool := &ocProxyPool{loaded: true, accounts: map[string]*ocProxyAccount{
			"a": {
				account:             "a",
				consecutiveFailures: 3,
				failureWindowStart:  time.Now().Add(-time.Hour),
				failureWindowCount:  2,
				cooldownUntil:       time.Now().Add(time.Hour),
			},
		}}
		ocProxyPools.Store(980001, pool)
		t.Cleanup(func() { ocProxyPools.Delete(980001) })

		markOpencodeProxySuccess(980001, "a", probe)

		state := pool.accounts["a"]
		assert.Zero(t, state.consecutiveFailures)
		assert.Equal(t, 2, state.failureWindowCount, "成功不清窗口")
		assert.True(t, state.cooldownUntil.IsZero())
		assert.True(t, state.verified)
	}
}

// 窗口计满 6 次才冷却, 并清零连续失败与窗口; 已在冷却中的失败不改截止时间。
func TestFailureWindowTriggersCooldownOnce(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{loaded: true, accounts: map[string]*ocProxyAccount{
		"a": {account: "a", failureWindowStart: now.Add(-time.Hour), failureWindowCount: 5},
	}}
	ocProxyPools.Store(980002, pool)
	t.Cleanup(func() { ocProxyPools.Delete(980002) })

	markOpencodeProxyRealFailure(980002, "a")

	state := pool.accounts["a"]
	assert.False(t, state.cooldownUntil.IsZero(), "第 6 次应进入冷却")
	assert.Zero(t, state.consecutiveFailures)
	assert.Zero(t, state.failureWindowCount)
	assert.True(t, state.failureWindowStart.IsZero())
	until := state.cooldownUntil

	markOpencodeProxyRealFailure(980002, "a")
	assert.Equal(t, until, pool.accounts["a"].cooldownUntil, "冷却中再失败不延长截止")
	assert.Zero(t, pool.accounts["a"].failureWindowCount, "冷却中不推进窗口")
}

// 距窗口起点超过 24h 后从 1 重计, 不沿用旧计数。
func TestFailureWindowResetsAfter24h(t *testing.T) {
	pool := &ocProxyPool{loaded: true, accounts: map[string]*ocProxyAccount{
		"a": {
			account:            "a",
			failureWindowStart: time.Now().Add(-ocProxyFailureWindow - time.Minute),
			failureWindowCount: 5,
		},
	}}
	ocProxyPools.Store(980003, pool)
	t.Cleanup(func() { ocProxyPools.Delete(980003) })

	markOpencodeProxyRealFailure(980003, "a")

	state := pool.accounts["a"]
	assert.Equal(t, 1, state.failureWindowCount)
	assert.True(t, state.cooldownUntil.IsZero(), "跨窗重置不应触发冷却")
	assert.Equal(t, 1, state.consecutiveFailures)
}

// 任何冷却路径都清零连续失败与窗口。
func TestCooldownClearsFailureState(t *testing.T) {
	pool := &ocProxyPool{loaded: true, accounts: map[string]*ocProxyAccount{
		"a": {
			account:             "a",
			consecutiveFailures: 4,
			failureWindowStart:  time.Now().Add(-time.Hour),
			failureWindowCount:  3,
		},
	}}
	ocProxyPools.Store(980004, pool)
	t.Cleanup(func() { ocProxyPools.Delete(980004) })

	markOpencodeProxyCooldown(980004, "a", time.Hour)

	state := pool.accounts["a"]
	assert.Zero(t, state.consecutiveFailures)
	assert.Zero(t, state.failureWindowCount)
	assert.True(t, state.failureWindowStart.IsZero())
	assert.False(t, state.cooldownUntil.IsZero())
}
