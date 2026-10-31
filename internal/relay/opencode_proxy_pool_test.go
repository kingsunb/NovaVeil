package relay

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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

// 同一会话不重复使用同一账号, 账号耗尽后铸造新账号, 其他会话仍可复用。
func TestOcProxyPoolPickSessionDedup(t *testing.T) {
	now := time.Now()
	pool := &ocProxyPool{accounts: map[string]*ocProxyAccount{
		"a": {account: "a", verified: true, lastSuccessAt: now},
		"b": {account: "b", verified: true, lastSuccessAt: now.Add(-time.Hour)},
	}}

	first := pool.pickLocked(now, "s1")
	second := pool.pickLocked(now, "s1")
	assert.NotEqual(t, first.account, second.account, "同会话第二次选号不应重复")
	assert.Equal(t, "a", first.account, "首次应选最近成功账号")

	assert.Equal(t, "a", pool.pickLocked(now, "s2").account, "其他会话不受去重影响")

	third := pool.pickLocked(now, "s1")
	assert.NotContains(t, []string{first.account, second.account}, third.account, "会话内账号耗尽后应铸造新账号")
	assert.Equal(t, third.account, pool.accounts[third.account].account, "新账号应入池")
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
