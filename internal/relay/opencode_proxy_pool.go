package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

const (
	// ocProxyPerAccountLimit 是单个代理账号的并发上限。
	ocProxyPerAccountLimit = 5
	// ocProxySuccessTTL 内成功过的账号优先复用。
	ocProxySuccessTTL = 7 * 24 * time.Hour
	// 403 后的账号冷却时长。
	ocProxyCooldown = 12 * time.Hour
	// 429 后的账号冷却时长。
	ocProxyRateLimitedCooldown = 24 * time.Hour
	// ocProxyMaxAccounts 限制每渠道内存与库内账号数。
	ocProxyMaxAccounts = 200
	// 每成员每请求最多换号三次。
	ocProxyMaxRetries = 3
)

// ocProxyAccount 是一个代理账号的进程内状态。
type ocProxyAccount struct {
	account       string
	lastSuccessAt time.Time
	cooldownUntil time.Time
	active        int
	verified      bool
}

// ocProxyPool 是单渠道的账号池, 首次使用时从库加载。
type ocProxyPool struct {
	mu       sync.Mutex
	loaded   bool
	accounts map[string]*ocProxyAccount
}

var ocProxyPools sync.Map

// ocProxyPoolFor 返回渠道账号池, 首次调用时懒加载库内记录。
func ocProxyPoolFor(channelID int) *ocProxyPool {
	if cached, ok := ocProxyPools.Load(channelID); ok {
		return cached.(*ocProxyPool)
	}
	pool := &ocProxyPool{accounts: make(map[string]*ocProxyAccount)}
	actual, _ := ocProxyPools.LoadOrStore(channelID, pool)
	return actual.(*ocProxyPool)
}

// loadOpencodeProxyPool 从库加载已验证账号, 每渠道只执行一次。
func loadOpencodeProxyPool(pool *ocProxyPool, channelID int) {
	if pool.loaded {
		return
	}
	pool.loaded = true
	rows, err := op.LoadOpencodeProxyAccounts(channelID)
	if err != nil {
		log.Warnf("opencode 代理账号加载失败, 降级为内存池: %v", err)
		return
	}
	for _, row := range rows {
		pool.accounts[row.Account] = &ocProxyAccount{
			account:       row.Account,
			lastSuccessAt: row.LastSuccessAt,
			cooldownUntil: row.CooldownUntil,
			verified:      true,
		}
	}
}

// proxyNeedsOpencodeAccount 报告渠道是否需要按 IP 轮换代理账号。
func proxyNeedsOpencodeAccount(channel model.Channel) bool {
	if !isOpencodeFreeLane(channel) || channel.ChannelProxy == nil {
		return false
	}
	return strings.Contains(*channel.ChannelProxy, model.AccountPlaceholder)
}

// pickOpencodeProxyAccount 选出本轮使用的账号并占用一个并发槽位。
func pickOpencodeProxyAccount(ctx context.Context, channelID int) string {
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	loadOpencodeProxyPool(pool, channelID)
	now := time.Now()
	var picked *ocProxyAccount
	for _, candidate := range pool.accounts {
		if !ocProxyUsable(candidate, now) {
			continue
		}
		if picked == nil || candidate.verified && (!picked.verified || candidate.lastSuccessAt.After(picked.lastSuccessAt)) {
			picked = candidate
		}
	}
	if picked == nil {
		picked = &ocProxyAccount{account: newOpencodeProxyAccount()}
		pool.accounts[picked.account] = picked
	}
	picked.active++
	account := picked.account
	pool.mu.Unlock()
	context.AfterFunc(ctx, func() { releaseOpencodeProxyAccount(channelID, account) })
	return account
}

// ocProxyUsable 报告账号是否未冷却、未过期且并发未满。
func ocProxyUsable(account *ocProxyAccount, now time.Time) bool {
	if account.active >= ocProxyPerAccountLimit {
		return false
	}
	if now.Before(account.cooldownUntil) {
		return false
	}
	if account.verified && now.Sub(account.lastSuccessAt) > ocProxySuccessTTL {
		return false
	}
	return true
}

// releaseOpencodeProxyAccount 归还账号的并发槽位。
func releaseOpencodeProxyAccount(channelID int, account string) {
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if state, ok := pool.accounts[account]; ok && state.active > 0 {
		state.active--
	}
}

// markOpencodeProxySuccess 记录账号成功, 并异步落库。
func markOpencodeProxySuccess(channelID int, account string) {
	now := time.Now()
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	loadOpencodeProxyPool(pool, channelID)
	state, ok := pool.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		pool.accounts[account] = state
	}
	state.verified = true
	state.lastSuccessAt = now
	state.cooldownUntil = time.Time{}
	pool.mu.Unlock()
	go func() {
		if err := op.SaveOpencodeProxySuccess(channelID, account, now); err != nil {
			log.Warnf("opencode 代理账号成功记录落库失败: %v", err)
			return
		}
		if err := op.PruneOpencodeProxyAccounts(channelID, ocProxyMaxAccounts); err != nil {
			log.Warnf("opencode 代理账号清理失败: %v", err)
		}
	}()
}

// 403 后冷却账号 12 小时。
func markOpencodeProxyForbidden(channelID int, account string) {
	markOpencodeProxyCooldown(channelID, account, ocProxyCooldown)
}

// 429 后冷却账号 24 小时。
func markOpencodeProxyRateLimited(channelID int, account string) {
	markOpencodeProxyCooldown(channelID, account, ocProxyRateLimitedCooldown)
}

// 写入账号冷却并异步落库。
func markOpencodeProxyCooldown(channelID int, account string, cooldown time.Duration) {
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	state, ok := pool.accounts[account]
	if !ok || !state.verified {
		delete(pool.accounts, account)
		pool.mu.Unlock()
		return
	}
	until := time.Now().Add(cooldown)
	state.cooldownUntil = until
	pool.mu.Unlock()
	go func() {
		if err := op.SaveOpencodeProxyCooldown(channelID, account, until); err != nil {
			log.Warnf("opencode 代理账号冷却落库失败: %v", err)
		}
	}()
}

// newOpencodeProxyAccount 生成新的随机账号标识。
func newOpencodeProxyAccount() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "opencode" + hex.EncodeToString([]byte(time.Now().Format("150405")))
	}
	return "opencode" + hex.EncodeToString(buf)
}
