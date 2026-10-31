package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

const (
	// warpSlotsCacheTTL 限定槽位快照的最大缓存时间。
	warpSlotsCacheTTL = time.Second
	// warpSlotRateLimitWindow 是单槽 429 的暂停派发时长。
	warpSlotRateLimitWindow = 60 * time.Second
	// warpSlotAllCoolingWindow 是三个不同 /64 都被 429 后的全槽冷却时长。
	warpSlotAllCoolingWindow = 60 * time.Second
	// warpSlotProxySuspect 是代理自身 407/503 后的短暂停。
	warpSlotProxySuspect = 2 * time.Second
	// warpSlotNetworkSuspect 是出口网络失败后的短暂停。
	warpSlotNetworkSuspect = 5 * time.Second
	// warpSlotsRetryCap 限制返回给客户端的 Retry-After。
	warpSlotsRetryCap = time.Minute
	// warpSlotSessionTTL 是会话内槽位去重的记忆时长。
	warpSlotSessionTTL = 24 * time.Hour
)

// warpSlotInfo 是 WARP /slots 返回的单个槽位。
type warpSlotInfo struct {
	Slot         int    `json:"slot"`
	Account      string `json:"account"`
	Egress       string `json:"egress"`
	Prefix       string `json:"prefix"`
	State        string `json:"state"`
	InFlight     int    `json:"in_flight"`
	CoolingUntil string `json:"cooling_until"`
	Generation   int    `json:"generation"`
}

// warpSlotsSnapshot 是 WARP /slots 的完整快照。
type warpSlotsSnapshot struct {
	Size      int            `json:"size"`
	UpdatedAt string         `json:"updated_at"`
	Slots     []warpSlotInfo `json:"slots"`
}

// warpSlotPool 保存单个 WARP 控制端的槽位缓存与本地占位。
type warpSlotPool struct {
	controlBase string
	slotsURL    string
	client      *http.Client

	mu              sync.Mutex
	fetchMu         sync.Mutex
	fetchedAt       time.Time
	snapshot        warpSlotsSnapshot
	reserved        map[int]int
	suspect         map[int]time.Time
	votes           map[string]time.Time
	allCoolingUntil time.Time
	next            int
	// sessionUsed 记录会话已用过的槽位, 同会话不重复请求同一账号。
	sessionUsed map[string]map[int]time.Time
}

// warpSlotLease 是一次槽位占位, 必须在本轮结束时 Release。
type warpSlotLease struct {
	pool       *warpSlotPool
	slot       int
	account    string
	egress     string
	prefix     string
	generation int
	once       sync.Once
}

// warpSlotsBusyError 表示 WARP 槽位暂不可用, 由 handler 转成 503+Retry-After。
type warpSlotsBusyError struct {
	RetryAfter time.Duration
	cause      error
}

func (e *warpSlotsBusyError) Error() string {
	return "WARP 槽位全部不可用，请稍候重试"
}

func (e *warpSlotsBusyError) Unwrap() error {
	return e.cause
}

var warpSlotPools sync.Map

// pickWarpSlot 为渠道选择一个可用槽位并占位; session 非空时同会话不重复用槽。
func pickWarpSlot(ctx context.Context, channel model.Channel, session string) (*warpSlotLease, error) {
	base, err := warpControlBase(channel)
	if err != nil {
		return nil, err
	}
	pool := warpSlotPoolFor(base)
	if err := pool.refresh(ctx); err != nil {
		return nil, &warpSlotsBusyError{RetryAfter: time.Second, cause: err}
	}
	lease, err := pool.pick(session)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, lease.Release)
	return lease, nil
}

func warpSlotPoolFor(base string) *warpSlotPool {
	if value, ok := warpSlotPools.Load(base); ok {
		return value.(*warpSlotPool)
	}
	pool := &warpSlotPool{
		controlBase: base,
		slotsURL:    base + "/slots",
		client: &http.Client{
			Timeout:   3 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
		reserved:    map[int]int{},
		suspect:     map[int]time.Time{},
		votes:       map[string]time.Time{},
		sessionUsed: map[string]map[int]time.Time{},
	}
	actual, _ := warpSlotPools.LoadOrStore(base, pool)
	return actual.(*warpSlotPool)
}

// refresh 按 TTL 刷新槽位快照; 并发刷新会串行化。
func (p *warpSlotPool) refresh(ctx context.Context) error {
	p.mu.Lock()
	fresh := !p.fetchedAt.IsZero() && time.Since(p.fetchedAt) < warpSlotsCacheTTL
	p.mu.Unlock()
	if fresh {
		return nil
	}

	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	p.mu.Lock()
	fresh = !p.fetchedAt.IsZero() && time.Since(p.fetchedAt) < warpSlotsCacheTTL
	p.mu.Unlock()
	if fresh {
		return nil
	}

	snapshot, err := p.fetch(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.snapshot = snapshot
	p.fetchedAt = time.Now()
	p.mu.Unlock()
	return nil
}

func (p *warpSlotPool) fetch(ctx context.Context) (warpSlotsSnapshot, error) {
	var snapshot warpSlotsSnapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.slotsURL, nil)
	if err != nil {
		return snapshot, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return snapshot, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot, fmt.Errorf("WARP /slots 返回 %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("解析 WARP /slots: %w", err)
	}
	if snapshot.Size <= 0 || len(snapshot.Slots) != snapshot.Size {
		return snapshot, fmt.Errorf("WARP /slots 数量异常: size=%d slots=%d", snapshot.Size, len(snapshot.Slots))
	}
	seen := make([]bool, snapshot.Size)
	for _, slot := range snapshot.Slots {
		if slot.Slot < 0 || slot.Slot >= snapshot.Size || seen[slot.Slot] {
			return snapshot, fmt.Errorf("WARP /slots 槽位号异常: %d", slot.Slot)
		}
		if slot.Account != "warp-"+strconv.Itoa(slot.Slot) {
			return snapshot, fmt.Errorf("WARP /slots 账号异常: slot=%d account=%q", slot.Slot, slot.Account)
		}
		seen[slot.Slot] = true
	}
	return snapshot, nil
}

func (p *warpSlotPool) pick(session string) (*warpSlotLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.pruneVotesLocked(now)
	p.pruneSessionsLocked(now)
	if now.Before(p.allCoolingUntil) {
		return nil, &warpSlotsBusyError{RetryAfter: p.allCoolingUntil.Sub(now)}
	}

	for attempt := 0; attempt < 2; attempt++ {
		if lease := p.pickAvailableLocked(now, session); lease != nil {
			return lease, nil
		}
		if session == "" || len(p.sessionUsed[session]) == 0 {
			break
		}
		// 本会话已用完全部候选槽位时允许复用, 避免把请求钉死。
		delete(p.sessionUsed, session)
	}
	return nil, &warpSlotsBusyError{RetryAfter: p.retryAfterLocked(now)}
}

// pickAvailableLocked 按负载选空闲槽, 并排除本会话已用过的槽位; 调用方需持锁。
func (p *warpSlotPool) pickAvailableLocked(now time.Time, session string) *warpSlotLease {
	count := len(p.snapshot.Slots)
	used := p.sessionUsed[session]
	best := -1
	bestLoad := 0
	for offset := 0; offset < count; offset++ {
		index := (p.next + offset) % count
		slot := p.snapshot.Slots[index]
		if slot.State != "active" || slot.Account == "" || slot.Egress == "" {
			continue
		}
		if slot.InFlight+p.reserved[slot.Slot] > 0 {
			continue
		}
		if until, ok := p.suspect[slot.Slot]; ok && now.Before(until) {
			continue
		}
		if session != "" {
			if _, seen := used[slot.Slot]; seen {
				continue
			}
		}
		load := slot.InFlight + p.reserved[slot.Slot]
		if best == -1 || load < bestLoad {
			best = index
			bestLoad = load
		}
	}
	if best == -1 {
		return nil
	}

	slot := p.snapshot.Slots[best]
	p.reserved[slot.Slot]++
	p.next = (best + 1) % count
	if session != "" {
		if p.sessionUsed == nil {
			p.sessionUsed = map[string]map[int]time.Time{}
		}
		if p.sessionUsed[session] == nil {
			p.sessionUsed[session] = map[int]time.Time{}
		}
		p.sessionUsed[session][slot.Slot] = now
	}
	return &warpSlotLease{
		pool:       p,
		slot:       slot.Slot,
		account:    slot.Account,
		egress:     slot.Egress,
		prefix:     slot.Prefix,
		generation: slot.Generation,
	}
}

// pruneSessionsLocked 清理过期的会话槽位记忆; 调用方需持锁。
func (p *warpSlotPool) pruneSessionsLocked(now time.Time) {
	for session, used := range p.sessionUsed {
		for slot, at := range used {
			if now.Sub(at) >= warpSlotSessionTTL {
				delete(used, slot)
			}
		}
		if len(used) == 0 {
			delete(p.sessionUsed, session)
		}
	}
}

func (p *warpSlotPool) retryAfterLocked(now time.Time) time.Duration {
	retry := time.Duration(0)
	consider := func(until time.Time) {
		if until.After(now) {
			wait := until.Sub(now)
			if retry == 0 || wait < retry {
				retry = wait
			}
		}
	}
	consider(p.allCoolingUntil)
	for _, until := range p.suspect {
		consider(until)
	}
	for _, slot := range p.snapshot.Slots {
		if slot.State != "cooling" || slot.CoolingUntil == "" {
			continue
		}
		if until, err := time.Parse(time.RFC3339, slot.CoolingUntil); err == nil {
			consider(until)
		}
	}
	if retry == 0 {
		retry = time.Second
	}
	if retry < time.Second {
		retry = time.Second
	}
	if retry > warpSlotsRetryCap {
		retry = warpSlotsRetryCap
	}
	return retry
}

func (p *warpSlotPool) release(slot int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if count := p.reserved[slot]; count <= 1 {
		delete(p.reserved, slot)
	} else {
		p.reserved[slot] = count - 1
	}
}

func (p *warpSlotPool) invalidate() {
	p.mu.Lock()
	p.fetchedAt = time.Time{}
	p.mu.Unlock()
}

func (p *warpSlotPool) pruneVotesLocked(now time.Time) {
	for prefix, at := range p.votes {
		if now.Sub(at) >= warpSlotRateLimitWindow {
			delete(p.votes, prefix)
		}
	}
}

// Account 返回槽位对应的代理用户名。
func (l *warpSlotLease) Account() string {
	if l == nil {
		return ""
	}
	return l.account
}

// Egress 返回槽位当前出口地址。
func (l *warpSlotLease) Egress() string {
	if l == nil {
		return ""
	}
	return l.egress
}

// Release 归还槽位占位, 幂等。
func (l *warpSlotLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.pool.release(l.slot)
	})
}

// MarkSuccess 清除该槽的短期标记。
func (l *warpSlotLease) MarkSuccess() {
	if l == nil {
		return
	}
	l.pool.mu.Lock()
	delete(l.pool.suspect, l.slot)
	l.pool.mu.Unlock()
}

// MarkRateLimited 记录单槽 429; 三个不同 /64 在窗口内达到后返回 true。
func (l *warpSlotLease) MarkRateLimited() bool {
	if l == nil {
		return false
	}
	pool := l.pool
	pool.mu.Lock()
	defer pool.mu.Unlock()
	now := time.Now()
	pool.suspect[l.slot] = now.Add(warpSlotRateLimitWindow)
	if l.prefix != "" {
		pool.pruneVotesLocked(now)
		if _, ok := pool.votes[l.prefix]; !ok {
			pool.votes[l.prefix] = now
		}
	}
	if len(pool.votes) < 3 {
		return false
	}
	if now.Before(pool.allCoolingUntil) {
		return false
	}
	pool.allCoolingUntil = now.Add(warpSlotAllCoolingWindow)
	pool.votes = map[string]time.Time{}
	return true
}

// MarkProxyUnavailable 处理代理自身 407/503: 短暂停并刷新快照, 不淘汰出口。
func (l *warpSlotLease) MarkProxyUnavailable() {
	if l == nil {
		return
	}
	l.pool.mu.Lock()
	l.pool.suspect[l.slot] = time.Now().Add(warpSlotProxySuspect)
	l.pool.mu.Unlock()
	l.pool.invalidate()
}

// MarkNetworkFailure 淘汰该槽当前出口并刷新快照。
func (l *warpSlotLease) MarkNetworkFailure(reason string) {
	if l == nil {
		return
	}
	l.pool.mu.Lock()
	l.pool.suspect[l.slot] = time.Now().Add(warpSlotNetworkSuspect)
	l.pool.mu.Unlock()
	warpPenalizeEgress(l.pool.controlBase, l.egress, reason)
	l.pool.invalidate()
}

// RotateAll 触发全池轮换并刷新快照。
func (l *warpSlotLease) RotateAll() {
	if l == nil {
		return
	}
	warpRotateAll(l.pool.controlBase)
	l.pool.invalidate()
}
