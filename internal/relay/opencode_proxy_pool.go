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
	// 403/429 被拒账号统一冷却 24 小时。
	ocProxyRejectedCooldown = 24 * time.Hour
	// ocProxyMaxAccounts 是每渠道账号数上限, 库内按此淘汰。
	ocProxyMaxAccounts = 2000
	// 每成员每请求最多换号三次。
	ocProxyMaxRetries = 3
	// ocPruneInterval 限制淘汰查询频率, 避免每次落库都全表排序。
	ocPruneInterval = 10 * time.Minute
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
	mu          sync.Mutex
	loaded      bool
	lastPruneAt time.Time
	accounts    map[string]*ocProxyAccount
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
		// 台账里存在未探测的生成账号(探测前即落账), 不能整行视为已验证:
		// 只有真实成功过(含冷却行的历史成功)才可信, 未探测项降级为普通候选。
		pool.accounts[row.Account] = &ocProxyAccount{
			account:       row.Account,
			lastSuccessAt: row.LastSuccessAt,
			cooldownUntil: row.CooldownUntil,
			verified:      !row.LastSuccessAt.IsZero(),
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
		pool.trimLocked(picked.account)
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

// needsPrune 报告是否需要淘汰库内账号; 调用方需持锁。满额才查库, 且限频。
func (p *ocProxyPool) needsPrune() bool {
	if len(p.accounts) < ocProxyMaxAccounts || time.Since(p.lastPruneAt) <= ocPruneInterval {
		return false
	}
	p.lastPruneAt = time.Now()
	return true
}

// trimLocked 把内存账号数压回上限, 淘汰最久未成功的; 调用方需持锁。
func (p *ocProxyPool) trimLocked(keep string) {
	for len(p.accounts) > ocProxyMaxAccounts {
		var oldest *ocProxyAccount
		for _, state := range p.accounts {
			if state.account == keep {
				continue
			}
			if oldest == nil || state.lastSuccessAt.Before(oldest.lastSuccessAt) {
				oldest = state
			}
		}
		if oldest == nil {
			return
		}
		delete(p.accounts, oldest.account)
	}
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
		pool.trimLocked(account)
	}
	state.verified = true
	state.lastSuccessAt = now
	state.cooldownUntil = time.Time{}
	prune := pool.needsPrune()
	pool.mu.Unlock()
	go func() {
		if err := op.SaveOpencodeProxySuccess(channelID, account, now); err != nil {
			log.Warnf("opencode 代理账号成功记录落库失败: %v", err)
			return
		}
		if !prune {
			return
		}
		if err := op.PruneOpencodeProxyAccounts(channelID, ocProxyMaxAccounts); err != nil {
			log.Warnf("opencode 代理账号清理失败: %v", err)
		}
	}()
}

// 403/429 被拒后冷却账号 24 小时并异步落库, 不区分拒绝原因。
func markOpencodeProxyRejected(channelID int, account string) {
	markOpencodeProxyCooldown(channelID, account, ocProxyRejectedCooldown)
}

// 写入账号冷却并异步落库; 未成功过的账号同样入槽。
func markOpencodeProxyCooldown(channelID int, account string, cooldown time.Duration) {
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	// 与成功回填同口径: 先确保首载完成, 避免后到的首载用库内旧行覆盖本冷却。
	loadOpencodeProxyPool(pool, channelID)
	state, ok := pool.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		pool.accounts[account] = state
		pool.trimLocked(account)
	}
	until := time.Now().Add(cooldown)
	state.cooldownUntil = until
	prune := pool.needsPrune()
	pool.mu.Unlock()
	go func() {
		if err := op.SaveOpencodeProxyCooldown(channelID, account, until); err != nil {
			log.Warnf("opencode 代理账号冷却落库失败: %v", err)
			return
		}
		if !prune {
			return
		}
		if err := op.PruneOpencodeProxyAccounts(channelID, ocProxyMaxAccounts); err != nil {
			log.Warnf("opencode 代理账号清理失败: %v", err)
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
