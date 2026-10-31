package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
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
	// 403/429 被拒账号冷却上限, 无 Retry-After 时的回落值。
	ocProxyRejectedCooldown = 24 * time.Hour
	// ocProxyMaxAccounts 是每渠道账号数上限, 库内按此淘汰。
	ocProxyMaxAccounts = 30000
	// 每成员每请求最多换号三次。
	ocProxyMaxRetries = 3
	// ocPruneInterval 限制淘汰查询频率, 避免每次落库都全表排序。
	ocPruneInterval = 10 * time.Minute
	// ocProxySessionTTL 是会话内账号去重的记忆时长。
	ocProxySessionTTL = 24 * time.Hour
	// ocProxy429VoteWindow 是 429 计票窗口。
	ocProxy429VoteWindow = 10 * time.Minute
	// ocProxy429Quorum 是触发冷却所需的不同会话数。
	ocProxy429Quorum = 3
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
	// sessionUsed 记录会话已用过的账号, 保证同会话不重复请求同一账号。
	sessionUsed map[string]map[string]time.Time
	// rateLimited 按会话累计 429 计票, 达到 quorum 才冷却账号。
	rateLimited map[string]time.Time
}

var ocProxyPools sync.Map

// ocProxyPoolFor 返回渠道账号池, 首次调用时懒加载库内记录。
func ocProxyPoolFor(channelID int) *ocProxyPool {
	if cached, ok := ocProxyPools.Load(channelID); ok {
		return cached.(*ocProxyPool)
	}
	pool := &ocProxyPool{
		accounts:    make(map[string]*ocProxyAccount),
		sessionUsed: make(map[string]map[string]time.Time),
		rateLimited: make(map[string]time.Time),
	}
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
	return model.HasAccountPlaceholder(*channel.ChannelProxy)
}

// pickOpencodeProxyAccount 选出本轮使用的账号并占用一个并发槽位。
// session 非空时同一会话不重复使用同一账号, 会话内账号耗尽后铸造新账号。
func pickOpencodeProxyAccount(ctx context.Context, channelID int, session string) string {
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	loadOpencodeProxyPool(pool, channelID)
	picked := pool.pickLocked(time.Now(), session)
	picked.active++
	account := picked.account
	pool.mu.Unlock()
	context.AfterFunc(ctx, func() { releaseOpencodeProxyAccount(channelID, account) })
	return account
}

// pickLocked 按可用性与最近成功优先选号, 并排除本会话已用过的账号; 调用方需持锁。
func (p *ocProxyPool) pickLocked(now time.Time, session string) *ocProxyAccount {
	p.pruneLocked(now)
	used := p.sessionUsed[session]
	var picked *ocProxyAccount
	for _, candidate := range p.accounts {
		if !ocProxyUsable(candidate, now) {
			continue
		}
		if session != "" {
			if _, seen := used[candidate.account]; seen {
				continue
			}
		}
		if picked == nil || candidate.verified && (!picked.verified || candidate.lastSuccessAt.After(picked.lastSuccessAt)) {
			picked = candidate
		}
	}
	if picked == nil {
		picked = &ocProxyAccount{account: newOpencodeProxyAccount()}
		p.accounts[picked.account] = picked
		p.trimLocked(picked.account)
	}
	if session != "" {
		if p.sessionUsed == nil {
			p.sessionUsed = map[string]map[string]time.Time{}
		}
		if p.sessionUsed[session] == nil {
			p.sessionUsed[session] = map[string]time.Time{}
		}
		p.sessionUsed[session][picked.account] = now
	}
	return picked
}

// pruneLocked 清理过期的会话记忆与 429 计票; 调用方需持锁。
func (p *ocProxyPool) pruneLocked(now time.Time) {
	for session, used := range p.sessionUsed {
		for account, at := range used {
			if now.Sub(at) >= ocProxySessionTTL {
				delete(used, account)
			}
		}
		if len(used) == 0 {
			delete(p.sessionUsed, session)
		}
	}
	for session, at := range p.rateLimited {
		if now.Sub(at) >= ocProxy429VoteWindow {
			delete(p.rateLimited, session)
		}
	}
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

// trimLocked 把内存账号数压回上限, 淘汰序与库内一致: 未成功过的先清,
// 同组按最久未活跃; 调用方需持锁。
func (p *ocProxyPool) trimLocked(keep string) {
	for len(p.accounts) > ocProxyMaxAccounts {
		var oldest *ocProxyAccount
		for _, state := range p.accounts {
			if state.account == keep {
				continue
			}
			if oldest == nil || ocProxyEvictFirst(state, oldest) {
				oldest = state
			}
		}
		if oldest == nil {
			return
		}
		delete(p.accounts, oldest.account)
	}
}

// ocProxyRecency 取最近成功与冷却截止的较大者作为活跃时间。
func ocProxyRecency(state *ocProxyAccount) time.Time {
	if state.cooldownUntil.After(state.lastSuccessAt) {
		return state.cooldownUntil
	}
	return state.lastSuccessAt
}

// ocProxyEvictFirst 报告 a 是否比 b 先淘汰: 未成功过的先清, 同组最久未活跃先清。
func ocProxyEvictFirst(a, b *ocProxyAccount) bool {
	if a.verified != b.verified {
		return !a.verified
	}
	return ocProxyRecency(a).Before(ocProxyRecency(b))
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

// markOpencodeProxySuccess 记录账号成功并异步落库; probe 为 true 记体检成功,
// false 记下游真实转发成功, 两路共用最近成功时间与成功天数, 计数列分开累计。
func markOpencodeProxySuccess(channelID int, account string, probe bool) {
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
	save := op.SaveOpencodeProxySuccess
	if probe {
		save = op.SaveOpencodeProxyProbeSuccess
	}
	go func() {
		if err := save(channelID, account, now); err != nil {
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

// markOpencodeProxyRealFailure 累计一次下游真实转发失败并异步落库;
// 只计数, 不影响池内可用性与冷却。
func markOpencodeProxyRealFailure(channelID int, account string) {
	go func() {
		if err := op.SaveOpencodeProxyRealFailure(channelID, account); err != nil {
			log.Warnf("opencode 代理账号失败记录落库失败: %v", err)
		}
	}()
}

// 403 属于凭据/权限拒绝, 单次即冷却账号并异步落库。
func markOpencodeProxyRejected(channelID int, account, retryAfter string) {
	markOpencodeProxyCooldown(channelID, account, opencodeRejectCooldown(retryAfter, time.Now()))
}

// recordRateLimitedLocked 记录一次会话 429, 返回是否达到冷却 quorum; 调用方需持锁。
func (p *ocProxyPool) recordRateLimitedLocked(session string, now time.Time) bool {
	p.pruneLocked(now)
	if p.rateLimited == nil {
		p.rateLimited = map[string]time.Time{}
	}
	p.rateLimited[session] = now
	if len(p.rateLimited) < ocProxy429Quorum {
		return false
	}
	p.rateLimited = map[string]time.Time{}
	return true
}

// markOpencodeProxyRateLimited 处理 429: 窗口内三个不同会话报出才冷却账号,
// 单会话仅计票不冷却; 无会话标识时无法计票, 回退单次冷却避免反复使用被限流账号。
func markOpencodeProxyRateLimited(channelID int, account, session, retryAfter string) {
	now := time.Now()
	if session == "" {
		markOpencodeProxyCooldown(channelID, account, opencodeRejectCooldown(retryAfter, now))
		return
	}
	pool := ocProxyPoolFor(channelID)
	pool.mu.Lock()
	loadOpencodeProxyPool(pool, channelID)
	quorum := pool.recordRateLimitedLocked(session, now)
	pool.mu.Unlock()
	if quorum {
		markOpencodeProxyCooldown(channelID, account, opencodeRejectCooldown(retryAfter, now))
	}
}

// opencodeRejectCooldown 按上游 Retry-After(秒)定冷却时长; 缺失时回落到下一个
// UTC 零点——免费档配额按 UTC 天重置, 固定冷却会跨过重置点白白闲置出口。
func opencodeRejectCooldown(retryAfter string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if cooldown := time.Duration(seconds) * time.Second; cooldown < ocProxyRejectedCooldown {
			return cooldown
		}
		return ocProxyRejectedCooldown
	}
	utc := now.UTC()
	next := utc.Truncate(24 * time.Hour).Add(24 * time.Hour)
	if cooldown := next.Sub(utc); cooldown > 0 && cooldown < ocProxyRejectedCooldown {
		return cooldown
	}
	return ocProxyRejectedCooldown
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
