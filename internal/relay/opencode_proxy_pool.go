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
	// ocProxyPerAccountLimit 是单个代理账号的并发请求上限。
	ocProxyPerAccountLimit = 5
	// ocProxyPerAccountSessions 是单账号同时承载的活跃会话上限。
	ocProxyPerAccountSessions = 1
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
	// ocProxySessionTTL 是会话与账号粘合的记忆时长。
	ocProxySessionTTL = 24 * time.Hour
	// ocProxy429VoteWindow 是 429 计票窗口。
	ocProxy429VoteWindow = 24 * time.Hour
	// ocProxy429Quorum 是触发冷却所需的不同会话数。
	ocProxy429Quorum = 2
	// ocProxyFailureWindow 是真实失败翻滚窗口。
	ocProxyFailureWindow = 24 * time.Hour
	// ocProxyFailureWindowLimit 是窗口内触发冷却的失败次数。
	ocProxyFailureWindowLimit = 6
)

// ocProxyAccount 是一个代理账号的进程内状态。
type ocProxyAccount struct {
	account       string
	lastSuccessAt time.Time
	cooldownUntil time.Time
	active        int
	// 活跃会话及请求数, 空会话不登记。
	sessions map[string]int
	verified bool
	// 连续真实失败数, 仅内存, 成功或冷却即清零。
	consecutiveFailures int
	// 24h 失败窗口, 落库供重启恢复。
	failureWindowStart time.Time
	failureWindowCount int
}

// ocProxyPool 是单渠道的账号池, 首次使用时从库加载。
type ocProxyPool struct {
	mu          sync.Mutex
	loaded      bool
	lastPruneAt time.Time
	accounts    map[string]*ocProxyAccount
	// sessionBound 记录会话粘合的账号: 同会话恒定同一出口, 跨轮稳定;
	// 同账号同时只承载一个活跃会话, 不同活跃会话不复用同一账号。
	// 同一会话标识跨出口复用会被上游关联标记, 故换号必须换会话标识。
	sessionBound map[string]ocSessionBinding
	// sessionBanned 记录会话内已被 429/403 拉黑的账号: 粘合选号时排除,
	// 防止换号重试落回刚被拒绝的出口。带时间戳供 TTL 清理。
	sessionBanned map[string]map[string]time.Time
	// rateLimited 按会话累计 429 计票, 达到 quorum 才冷却账号。
	rateLimited map[string]time.Time
}

// ocSessionBinding 是会话与账号的粘合记录, at 用于 TTL 清理。
type ocSessionBinding struct {
	account string
	at      time.Time
}

// ocProxyPoolKey 是账号池的隔离维度: 同渠道下每个 Key 拥有独立池。
type ocProxyPoolKey struct {
	channelID int
	keyID     string
}

var (
	// ocProxyPools 保存旧单 Key 渠道的池, 兼容历史调用与测试。
	ocProxyPools sync.Map
	// ocProxyKeyPools 保存 keyed 渠道按 Key 隔离的池。
	ocProxyKeyPools sync.Map
)

// ocProxyPoolFor 返回渠道旧单 Key 账号池。
func ocProxyPoolFor(channelID int) *ocProxyPool {
	if cached, ok := ocProxyPools.Load(channelID); ok {
		return cached.(*ocProxyPool)
	}
	pool := newOpencodeProxyPool()
	actual, _ := ocProxyPools.LoadOrStore(channelID, pool)
	return actual.(*ocProxyPool)
}

// ocProxyPoolForKey 返回渠道指定 Key 的账号池, 首次调用时懒加载库内记录。
func ocProxyPoolForKey(channelID int, keyID string) *ocProxyPool {
	if keyID == "" {
		return ocProxyPoolFor(channelID)
	}
	cacheKey := ocProxyPoolKey{channelID: channelID, keyID: keyID}
	if cached, ok := ocProxyKeyPools.Load(cacheKey); ok {
		return cached.(*ocProxyPool)
	}
	pool := newOpencodeProxyPool()
	actual, _ := ocProxyKeyPools.LoadOrStore(cacheKey, pool)
	return actual.(*ocProxyPool)
}

func newOpencodeProxyPool() *ocProxyPool {
	return &ocProxyPool{
		accounts:      make(map[string]*ocProxyAccount),
		sessionBound:  make(map[string]ocSessionBinding),
		sessionBanned: make(map[string]map[string]time.Time),
		rateLimited:   make(map[string]time.Time),
	}
}

// loadOpencodeProxyPool 从库加载渠道旧单 Key 账号。
func loadOpencodeProxyPool(pool *ocProxyPool, channelID int) {
	loadOpencodeProxyPoolForKey(pool, channelID, "")
}

// loadOpencodeProxyPoolForKey 从库加载指定 Key 的已验证账号, 每池只执行一次。
func loadOpencodeProxyPoolForKey(pool *ocProxyPool, channelID int, keyID string) {
	if pool.loaded {
		return
	}
	pool.loaded = true
	rows, err := op.LoadOpencodeProxyAccountsForKey(channelID, keyID)
	if err != nil {
		log.Warnf("opencode 代理账号加载失败, 降级为内存池: %v", err)
		return
	}
	for _, row := range rows {
		// 台账里存在未探测的生成账号(探测前即落账), 不能整行视为已验证:
		// 只有真实成功过(含冷却行的历史成功)才可信, 未探测项降级为普通候选。
		state := &ocProxyAccount{
			account:       row.Account,
			lastSuccessAt: row.LastSuccessAt,
			cooldownUntil: row.CooldownUntil,
			verified:      !row.LastSuccessAt.IsZero(),
		}
		// 冷却行的窗口由冷却写入清零; 未冷却行按库内快照恢复, 到期后接着累计。
		if !row.CooldownUntil.After(time.Now()) {
			state.failureWindowStart = row.FailureWindowStart
			state.failureWindowCount = row.FailureWindowCount
		}
		pool.accounts[row.Account] = state
	}
}

// proxyNeedsOpencodeAccount 报告渠道是否需要按 IP 轮换代理账号。
func proxyNeedsOpencodeAccount(channel model.Channel) bool {
	if !isOpencodeFreeLane(channel) || channel.ChannelProxy == nil {
		return false
	}
	return model.HasAccountPlaceholder(*channel.ChannelProxy)
}

// pickOpencodeProxyAccount 选出旧单 Key 渠道本轮使用的账号。
func pickOpencodeProxyAccount(ctx context.Context, channelID int, session string) string {
	return pickOpencodeProxyAccountForKey(ctx, channelID, "", session)
}

// pickOpencodeProxyAccountForKey 选出指定 Key 本轮使用的账号并占用一个并发槽位。
// keyID 非空时只使用该 Key 的固定账号, 不生成新账号; 空 keyID 走旧多账号池逻辑。
func pickOpencodeProxyAccountForKey(ctx context.Context, channelID int, keyID, session string) string {
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	var picked *ocProxyAccount
	if keyID == "" {
		picked = pool.pickLocked(time.Now(), session)
	} else {
		picked = pool.pickFixedLocked(time.Now(), session, keyID)
	}
	if picked == nil {
		pool.mu.Unlock()
		log.Warnf("opencode 渠道 %d 密钥 %s 无可用固定代理账号", channelID, keyID)
		return ""
	}
	picked.active++
	if session != "" {
		if picked.sessions == nil {
			picked.sessions = make(map[string]int)
		}
		picked.sessions[session]++
	}
	account := picked.account
	pool.mu.Unlock()
	context.AfterFunc(ctx, func() { releaseOpencodeProxyAccountForKey(channelID, keyID, account, session) })
	return account
}

// pickLocked 按会话粘合选号: 同会话优先绑定同一账号, 同账号同时只承载一个活跃会话,
// 绑定账号不可用或已被其他活跃会话占用才换号改绑; 调用方需持锁。
func (p *ocProxyPool) pickLocked(now time.Time, session string) *ocProxyAccount {
	p.pruneLocked(now)
	bound := ""
	banned := p.sessionBanned[session]
	if session != "" {
		if binding, ok := p.sessionBound[session]; ok {
			bound = binding.account
			if _, isBanned := banned[bound]; !isBanned {
				if a, ok := p.accounts[bound]; ok && ocProxyUsable(a, now) && ocProxySessionAvailable(a, session) {
					p.sessionBound[session] = ocSessionBinding{account: bound, at: now}
					return a
				}
			}
		}
	}
	// 换号: 排除失效的旧绑定与本会话拉黑的账号, 避免落回被拒出口。
	var picked *ocProxyAccount
	for _, candidate := range p.accounts {
		if !ocProxyUsable(candidate, now) || !ocProxySessionAvailable(candidate, session) {
			continue
		}
		if _, isBanned := banned[candidate.account]; isBanned {
			continue
		}
		if bound != "" && candidate.account == bound {
			continue
		}
		if picked == nil || ocProxyCandidateBetter(candidate, picked) {
			picked = candidate
		}
	}
	if picked == nil {
		picked = &ocProxyAccount{account: newOpencodeProxyAccount()}
		p.accounts[picked.account] = picked
		p.trimLocked(picked.account)
	}
	if session != "" {
		if p.sessionBound == nil {
			p.sessionBound = make(map[string]ocSessionBinding)
		}
		p.sessionBound[session] = ocSessionBinding{account: picked.account, at: now}
	}
	return picked
}

// pickFixedLocked 为 keyed 渠道选中唯一固定账号, 不生成额外账号; 调用方需持锁。
func (p *ocProxyPool) pickFixedLocked(now time.Time, session, account string) *ocProxyAccount {
	p.pruneLocked(now)
	state, ok := p.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		p.accounts[account] = state
	}
	if !ocProxyUsable(state, now) || !ocProxySessionAvailable(state, session) {
		return nil
	}
	if _, banned := p.sessionBanned[session][account]; banned {
		return nil
	}
	if session != "" {
		if p.sessionBound == nil {
			p.sessionBound = make(map[string]ocSessionBinding)
		}
		p.sessionBound[session] = ocSessionBinding{account: account, at: now}
	}
	return state
}

// ocProxyCandidateBetter 报告 a 是否优先于 b; 调用方需持锁。
// 连续失败少者优先(哪怕未验证), 同级已验证优先, 再按最近成功时间新者优先。
func ocProxyCandidateBetter(a, b *ocProxyAccount) bool {
	if a.consecutiveFailures != b.consecutiveFailures {
		return a.consecutiveFailures < b.consecutiveFailures
	}
	if a.verified != b.verified {
		return a.verified
	}
	return a.verified && a.lastSuccessAt.After(b.lastSuccessAt)
}

// pruneLocked 清理过期的会话记忆与 429 计票; 调用方需持锁。
func (p *ocProxyPool) pruneLocked(now time.Time) {
	for session, binding := range p.sessionBound {
		if now.Sub(binding.at) >= ocProxySessionTTL {
			delete(p.sessionBound, session)
		}
	}
	for session, accounts := range p.sessionBanned {
		for account, at := range accounts {
			if now.Sub(at) >= ocProxySessionTTL {
				delete(accounts, account)
			}
		}
		if len(accounts) == 0 {
			delete(p.sessionBanned, session)
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

// ocProxySessionAvailable 报告账号能否容纳该会话; 空会话不登记、不独占。
func ocProxySessionAvailable(account *ocProxyAccount, session string) bool {
	if session == "" {
		return true
	}
	if len(account.sessions) > ocProxyPerAccountSessions {
		return false
	}
	if account.sessions[session] > 0 {
		return true
	}
	return len(account.sessions) < ocProxyPerAccountSessions
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
		for session, binding := range p.sessionBound {
			if binding.account == oldest.account {
				delete(p.sessionBound, session)
			}
		}
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

// releaseOpencodeProxyAccount 归还旧单 Key 渠道账号的并发槽位。
func releaseOpencodeProxyAccount(channelID int, account, session string) {
	releaseOpencodeProxyAccountForKey(channelID, "", account, session)
}

// releaseOpencodeProxyAccountForKey 归还指定 Key 账号的并发槽位与活跃会话计数。
func releaseOpencodeProxyAccountForKey(channelID int, keyID, account, session string) {
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	state, ok := pool.accounts[account]
	if !ok {
		return
	}
	if state.active > 0 {
		state.active--
	}
	if session == "" {
		return
	}
	if state.sessions[session] > 1 {
		state.sessions[session]--
		return
	}
	delete(state.sessions, session)
}

// markOpencodeProxySuccess 记录旧单 Key 渠道账号成功。
func markOpencodeProxySuccess(channelID int, account string, probe bool) {
	markOpencodeProxySuccessForKey(channelID, "", account, probe)
}

// markOpencodeProxySuccessForKey 记录指定 Key 账号成功并异步落库; probe 为 true
// 记体检成功, false 记下游真实转发成功, 两路共用最近成功时间与成功天数。
func markOpencodeProxySuccessForKey(channelID int, keyID, account string, probe bool) {
	now := time.Now()
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	state, ok := pool.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		pool.accounts[account] = state
		pool.trimLocked(account)
	}
	state.verified = true
	state.lastSuccessAt = now
	state.cooldownUntil = time.Time{}
	state.consecutiveFailures = 0
	prune := pool.needsPrune()
	pool.mu.Unlock()
	save := op.SaveOpencodeProxySuccessForKey
	if probe {
		save = op.SaveOpencodeProxyProbeSuccessForKey
	}
	goSafe("opencode-proxy-success", func() {
		if err := save(channelID, keyID, account, now); err != nil {
			log.Warnf("opencode 代理账号成功记录落库失败: %v", err)
			return
		}
		if !prune {
			return
		}
		if err := op.PruneOpencodeProxyAccountsForKey(channelID, keyID, ocProxyMaxAccounts); err != nil {
			log.Warnf("opencode 代理账号清理失败: %v", err)
		}
	})
}

// markOpencodeProxyRealFailure 锁内记账一次下游真实转发失败, 锁外异步落库。
// 已在冷却中的账号只累计台账失败数, 不推进连续失败与窗口, 也不改冷却截止。
// 未冷却时 24h 窗口计满即冷却到下一个 UTC 零点, 并清零连续失败与窗口。
func markOpencodeProxyRealFailure(channelID int, account string) {
	markOpencodeProxyRealFailureForKey(channelID, "", account)
}

// markOpencodeProxyRealFailureForKey 锁内记账指定 Key 的一次下游真实转发失败。
func markOpencodeProxyRealFailureForKey(channelID int, keyID, account string) {
	now := time.Now()
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	state, ok := pool.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		pool.accounts[account] = state
		pool.trimLocked(account)
	}
	cooling := now.Before(state.cooldownUntil)
	if !cooling {
		state.consecutiveFailures++
		if state.failureWindowStart.IsZero() || now.Sub(state.failureWindowStart) > ocProxyFailureWindow {
			state.failureWindowStart = now
			state.failureWindowCount = 1
		} else {
			state.failureWindowCount++
		}
	}
	var cooldown time.Duration
	if !cooling && state.failureWindowCount >= ocProxyFailureWindowLimit {
		cooldown = opencodeRejectCooldown("", now)
		state.cooldownUntil = now.Add(cooldown)
		state.consecutiveFailures = 0
		state.failureWindowStart = time.Time{}
		state.failureWindowCount = 0
	}
	pool.mu.Unlock()
	goSafe("opencode-proxy-failure", func() {
		if err := op.SaveOpencodeProxyRealFailureForKey(channelID, keyID, account); err != nil {
			log.Warnf("opencode 代理账号失败记录落库失败: %v", err)
		}
	})
	if cooldown > 0 {
		markOpencodeProxyCooldownForKey(channelID, keyID, account, cooldown)
	}
}

// banOpencodeProxyAccount 在会话维度拉黑旧单 Key 渠道账号。
func banOpencodeProxyAccount(channelID int, session, account string) {
	banOpencodeProxyAccountForKey(channelID, "", session, account)
}

// banOpencodeProxyAccountForKey 在会话维度拉黑指定 Key 的账号并解除粘合:
// 429/403 后同会话不再使用该账号, 下次选号换新出口, 出站会话标识随新账号重派。
func banOpencodeProxyAccountForKey(channelID int, keyID, session, account string) {
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	if pool.sessionBanned == nil {
		pool.sessionBanned = make(map[string]map[string]time.Time)
	}
	if pool.sessionBanned[session] == nil {
		pool.sessionBanned[session] = make(map[string]time.Time)
	}
	pool.sessionBanned[session][account] = time.Now()
	if binding, ok := pool.sessionBound[session]; ok && binding.account == account {
		delete(pool.sessionBound, session)
	}
	pool.mu.Unlock()
}

// 403 属于凭据/权限拒绝, 单次即冷却旧单 Key 渠道账号并异步落库。
func markOpencodeProxyRejected(channelID int, account, retryAfter string) {
	markOpencodeProxyRejectedForKey(channelID, "", account, retryAfter)
}

// markOpencodeProxyRejectedForKey 冷却指定 Key 的账号。
func markOpencodeProxyRejectedForKey(channelID int, keyID, account, retryAfter string) {
	markOpencodeProxyCooldownForKey(channelID, keyID, account, opencodeRejectCooldown(retryAfter, time.Now()))
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

// markOpencodeProxyRateLimited 处理 429: 24 小时窗口内两个不同会话报出才冷却账号,
// 单会话仅计票不冷却; 无会话标识时无法计票, 回退单次冷却避免反复使用被限流账号。
func markOpencodeProxyRateLimited(channelID int, account, session, retryAfter string) {
	markOpencodeProxyRateLimitedForKey(channelID, "", account, session, retryAfter)
}

// markOpencodeProxyRateLimitedForKey 处理指定 Key 的 429 计票与冷却。
func markOpencodeProxyRateLimitedForKey(channelID int, keyID, account, session, retryAfter string) {
	now := time.Now()
	if session == "" {
		markOpencodeProxyCooldownForKey(channelID, keyID, account, opencodeRejectCooldown(retryAfter, now))
		return
	}
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	quorum := pool.recordRateLimitedLocked(session, now)
	pool.mu.Unlock()
	if quorum {
		markOpencodeProxyCooldownForKey(channelID, keyID, account, opencodeRejectCooldown(retryAfter, now))
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

// markOpencodeProxyCooldown 写入旧单 Key 渠道账号冷却。
func markOpencodeProxyCooldown(channelID int, account string, cooldown time.Duration) {
	markOpencodeProxyCooldownForKey(channelID, "", account, cooldown)
}

// markOpencodeProxyCooldownForKey 写入指定 Key 的账号冷却并异步落库; 未成功过的账号同样入槽。
func markOpencodeProxyCooldownForKey(channelID int, keyID, account string, cooldown time.Duration) {
	pool := ocProxyPoolForKey(channelID, keyID)
	pool.mu.Lock()
	// 与成功回填同口径: 先确保首载完成, 避免后到的首载用库内旧行覆盖本冷却。
	loadOpencodeProxyPoolForKey(pool, channelID, keyID)
	state, ok := pool.accounts[account]
	if !ok {
		state = &ocProxyAccount{account: account}
		pool.accounts[account] = state
		pool.trimLocked(account)
	}
	until := time.Now().Add(cooldown)
	state.cooldownUntil = until
	state.consecutiveFailures = 0
	state.failureWindowStart = time.Time{}
	state.failureWindowCount = 0
	prune := pool.needsPrune()
	pool.mu.Unlock()
	goSafe("opencode-proxy-cooldown", func() {
		if err := op.SaveOpencodeProxyCooldownForKey(channelID, keyID, account, until); err != nil {
			log.Warnf("opencode 代理账号冷却落库失败: %v", err)
			return
		}
		if !prune {
			return
		}
		if err := op.PruneOpencodeProxyAccountsForKey(channelID, keyID, ocProxyMaxAccounts); err != nil {
			log.Warnf("opencode 代理账号清理失败: %v", err)
		}
	})
}

// newOpencodeProxyAccount 生成新的随机账号标识。
func newOpencodeProxyAccount() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "opencode" + hex.EncodeToString([]byte(time.Now().Format("150405")))
	}
	return "opencode" + hex.EncodeToString(buf)
}
