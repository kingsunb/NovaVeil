package mask

import (
	"sync"
	"time"
)

// Mapping 一个会话的双向映射表: forward(原文→占位符)供脱敏复用, reverse(占位符→原文)供还原。
// 同一长对话中同一敏感值在多轮间必须映射为同一占位符, 否则大模型上下文逻辑混乱。
// Mapping 自带读写锁, 脱敏与还原可并发访问同一会话映射。
type Mapping struct {
	mu      sync.RWMutex
	forward map[string]string // 原文 → 占位符(脱敏用)
	reverse map[string]string // 占位符 → 原文(还原用)
	// onBind 在首次写入 forward 时调用一次, 把尚无命中的会话发布进表。
	// 未命中的会话不会调用它, 因此不会入表。
	onBind func()
}

// newMapping 构造空映射表。
func newMapping() *Mapping {
	return &Mapping{
		forward: make(map[string]string),
		reverse: make(map[string]string),
	}
}

// Recall 取原文对应的占位符: 已存在则复用(多轮一致), 否则生成新占位符并双向登记。
//
// 防套娃: 若 original 自身就是占位符(多轮历史带入),
// 严禁为其分配新 token, 否则 A→B→C 无限套娃。先反查其真实明文(递归解套, 深度上限 5),
// 查不到则原样返回自身, 绝不套娃。
func (m *Mapping) Recall(original, label string) string {
	// 防套娃: original 是占位符时, 先解出真实明文。
	if IsPlaceholder(original) {
		for depth := 0; depth < 5 && IsPlaceholder(original); depth++ {
			m.mu.RLock()
			real, ok := m.reverse[original]
			m.mu.RUnlock()
			if !ok {
				return original // 查不到真实明文, 原样返回, 绝不为占位符分配新 token
			}
			original = real
		}
		if IsPlaceholder(original) {
			return original // 解到仍是占位符, 放弃
		}
	}

	// 先读锁查复用。
	m.mu.RLock()
	tok, ok := m.forward[original]
	m.mu.RUnlock()
	if ok {
		return tok
	}

	// 未命中: 加写锁生成新占位符, double-check 防并发重复生成。
	m.mu.Lock()
	defer m.mu.Unlock()
	if tok, ok := m.forward[original]; ok {
		return tok
	}

	// 生成不与已有占位符冲突的新 token(查重 20 次, 实际不可达: 20^6≈4700万)。
	existing := make(map[string]struct{}, len(m.reverse))
	for t := range m.reverse {
		existing[t] = struct{}{}
	}
	var token string
	for i := 0; i < 20; i++ {
		token = NewToken(label)
		if _, dup := existing[token]; !dup {
			break
		}
	}
	m.forward[original] = token
	m.reverse[token] = original
	if m.onBind != nil {
		bind := m.onBind
		m.onBind = nil
		bind()
	}
	return token
}

// Lookup 占位符 → 原文, 供还原侧使用。未命中返回 ("", false); 调用方须原样保留占位符, 绝不猜。
func (m *Mapping) Lookup(placeholder string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	orig, ok := m.reverse[placeholder]
	return orig, ok
}

// SessionTTL 有会话键的映射表空闲回收时长, 与随机头 UUID / 会话粘合同量级:
// 多轮对话在 TTL 内复用同一占位符; 过期后下一请求重新分配。
const SessionTTL = 30 * time.Minute

// maxMaskSessions 脱敏会话表条数上限。未命中不入表; 超限时按最久未访问淘汰。
// 测试可临时调低。4096 与单组粘合上限同量级, 够多轮对话复用, 又不会只靠 TTL 无限涨。
var maxMaskSessions = 4096

type sessionRecord struct {
	mapping    *Mapping
	lastAccess time.Time
}

// inflightSession 是尚未命中、因此还没进持久表的共享映射。
// 同一键的并发首请求共用一张表; 全部结束后若仍无命中则丢弃。
type inflightSession struct {
	mapping *Mapping
	refs    int
}

// SessionStore 按会话键管理各会话的映射表。会话键通常来自隔离后的会话键;
// 无会话标识时不得写入本表(调用方使用请求级 Mapping, 见 GetOrCreate 空键分支)。
// 只有产生了占位符的会话才进入 sessions; 未命中只留在 inflight, 请求结束即丢。
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionRecord
	inflight map[string]*inflightSession
}

// NewSessionStore 构造空会话存储。
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*sessionRecord),
		inflight: make(map[string]*inflightSession),
	}
}

// GetOrCreate 返回指定会话的映射表。
// 空会话键返回全新的请求级 Mapping 且不入表, 避免所有无会话请求共享一张永不回收的表。
// 非空键若表中已有则复用并续期; 尚无命中时返回临时映射, 要等 Recall 写出占位符才入表。
// 调用方结束时必须 End, 否则未命中的临时项会留到进程退出。
func (s *SessionStore) GetOrCreate(sessionKey string) *Mapping {
	if sessionKey == "" {
		return newMapping()
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.sessions[sessionKey]; ok {
		rec.lastAccess = now
		return rec.mapping
	}
	if inf, ok := s.inflight[sessionKey]; ok {
		inf.refs++
		return inf.mapping
	}
	m := newMapping()
	m.onBind = func() { s.publish(sessionKey, m) }
	s.inflight[sessionKey] = &inflightSession{mapping: m, refs: 1}
	return m
}

// End 结束一次 GetOrCreate。已入表的会话只续期; 仍无命中且没有其他调用方时丢弃临时项。
func (s *SessionStore) End(sessionKey string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.sessions[sessionKey]; ok {
		rec.lastAccess = time.Now()
	}
	inf, ok := s.inflight[sessionKey]
	if !ok {
		return
	}
	inf.refs--
	if inf.refs > 0 {
		return
	}
	delete(s.inflight, sessionKey)
}

// publish 把已经产生占位符的映射放进持久表, 必要时淘汰最久未访问的条目。
// 由 Mapping.Recall 在持有映射写锁时调用; 本方法只拿会话表锁, 不再回头拿映射锁。
func (s *SessionStore) publish(key string, m *Mapping) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, key)
	if _, ok := s.sessions[key]; ok {
		return
	}
	now := time.Now()
	s.pruneExpiredLocked(now)
	s.evictOldestLocked()
	s.sessions[key] = &sessionRecord{mapping: m, lastAccess: now}
}

// Delete 回收指定会话的映射表与未命中临时项。空键是 no-op。
func (s *SessionStore) Delete(sessionKey string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey)
	delete(s.inflight, sessionKey)
}

// Len 返回当前持久会话条目数, 供测试与指标。
func (s *SessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// PruneExpired 删除超过 SessionTTL 未访问的会话映射, 返回删除条数。
func (s *SessionStore) PruneExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneExpiredLocked(now)
}

func (s *SessionStore) pruneExpiredLocked(now time.Time) int {
	removed := 0
	for key, rec := range s.sessions {
		if now.Sub(rec.lastAccess) >= SessionTTL {
			delete(s.sessions, key)
			removed++
		}
	}
	return removed
}

// evictOldestLocked 在条数达到上限时删掉最久未访问的一条, 给新命中腾位置。
// 上限小于 1 时不淘汰, 避免测试把上限打成 0 后死循环。调用方必须持有锁。
func (s *SessionStore) evictOldestLocked() {
	if maxMaskSessions < 1 {
		return
	}
	for len(s.sessions) >= maxMaskSessions {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, rec := range s.sessions {
			if first || rec.lastAccess.Before(oldest) {
				oldestKey = key
				oldest = rec.lastAccess
				first = false
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.sessions, oldestKey)
	}
}
