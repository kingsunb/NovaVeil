package relay

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/utils/opencodeid"
	"github.com/looplj/axonhub/llm/httpclient"
)

// opencodeSessionHeader opencode 兼容请求头的固定头名。
const opencodeSessionHeader = "x-opencode-session"

// opencodeSessionIDPattern 是上游接受的 x-opencode-session 整段格式:
// ses_ + 12 位小写十六进制 + 14 位字母数字。与模型同步测试里的格式正则对齐。
// 不接受前后空白, 调用方不得 TrimSpace 后再拿来匹配。
var opencodeSessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// validOpencodeSessionID 报告 value 是否整段是合法 opencode 会话号。
// 不改大小写, 不截断, 不 trim。空白、大写十六进制和长度偏差都不是合法值。
func validOpencodeSessionID(value string) bool {
	return opencodeSessionIDPattern.MatchString(value)
}

// resolveOpencodeSessionHeader 为一次转发选择上游 x-opencode-session。
// 入站 x-opencode-session 合法时原样返回; 否则 X-Session-Id 合法时原样返回;
// 两者都合法但不相同的, 用 x-opencode-session。都不合法时返回 fallback,
// fallback 应是本请求已经解析好的 sessionUUIDFor 结果。
// 命中入站合法值时不再铸新号, 也不把该值写进随机头缓存, 避免 x-trace-id 变成同一个会话号。
// 缓存里已有的随机值不能覆盖本请求带进来的合法值。
func resolveOpencodeSessionHeader(opencodeHeader, sessionIDHeader, fallback string) string {
	if validOpencodeSessionID(opencodeHeader) {
		return opencodeHeader
	}
	if validOpencodeSessionID(sessionIDHeader) {
		return sessionIDHeader
	}
	return fallback
}

// generateOpencodeSessionID 生成 opencode 格式的会话 ID。算法抽到 internal/utils/opencodeid
// 公共包，供转发路径与模型同步/探测路径共用，保证两处注入值格式一致。
func generateOpencodeSessionID() string {
	return opencodeid.GenerateSessionID()
}

// sessionUUIDEntry 一个会话(按 Key/渠道命名空间)的随机头会话 ID 映射记录, 到期前同一
// (会话, 渠道, Key) 复用同一 ID。ExpireAtUnixMilli 在命中时滑动续期, 活跃会话不会过期。
type sessionUUIDEntry struct {
	UUID              string // 会话级稳定的 opencode 格式会话 ID(格式: ses_<12 hex><14 random>)。
	ExpireAtUnixMilli int64  // 过期 Unix 毫秒时间, 到期即重新随机; 命中时刷新。
}

// sessionUUIDTTLMilli 会话 UUID 的固定存活时长(30 分钟), 与分组粘合时长解耦。
const sessionUUIDTTLMilli = int64(30 * time.Minute / time.Millisecond)

// sessionUUIDMaxEntries 会话 UUID 缓存的容量上限, 防止异常流量把进程内映射撑到不可用。
// 达到上限时按最旧(最小 ExpireAtUnixMilli, 即最久未续期)淘汰, 与 LRU 语义对齐。
const sessionUUIDMaxEntries = 100000

// sessionUUIDCacheKey 复合缓存键: 同一会话标识在不同渠道/Key 下独立缓存, 避免不同
// 上游凭据的会话 UUID 互相覆盖。用结构体作 map 键, 避免每次查询拼字符串分配。
type sessionUUIDCacheKey struct {
	sessionKey string
	channelID  int
	keyID      string
}

// sessionUUIDs 会话标识到随机头 UUID 的进程内映射: 独立于会话粘合表(粘合表按分组
// 分片且值域是成员 ID, 语义不同), 只共享会话标识这一锚点; 由独立 RWMutex
// 保护(读多写少), 不与 routeMu 共享以免放大临界区。
var sessionUUIDs = struct {
	sync.RWMutex
	entries map[sessionUUIDCacheKey]sessionUUIDEntry
}{entries: make(map[sessionUUIDCacheKey]sessionUUIDEntry)}

// sessionUUIDLastPrune 记录上次全量清理的 Unix 毫秒, 节流写路径的顺带清理。
var sessionUUIDLastPrune atomic.Int64

// sessionUUIDPruneInterval 两次顺带全量清理之间的最小间隔, 与粘合表 prune 节流对齐。
const sessionUUIDPruneInterval = int64(60 * time.Second / time.Millisecond)

// sessionUUIDFor 返回 (会话标识, 渠道, Key) 对应的稳定 opencode 格式会话 ID:
//   - 命中未过期条目时滑动续期 TTL 并返回缓存值(活跃会话不会过期);
//   - 未命中则生成新 opencode 会话 ID 并缓存, 顺带按间隔清理过期残留并在超容量时淘汰最旧条目;
//   - sessionKey 为空时每次生成新 ID 且不缓存(降级为每请求独立值, 由请求级 scope 保证同请求复用)。
//
// channelID 与 keyID 共同构成命名空间: 不同渠道/Key 的同一会话标识互不冲突,
// 避免指向不同上游的会话 ID 互相覆盖。keyID 为空串表示单 Key 渠道。
func sessionUUIDFor(sessionKey string, channelID int, keyID string) string {
	// 空键与超长键不进缓存。调用方应传入 sessionScopeKey, 使不同 API Key 的同名会话
	// 不共用上游会话号; 本函数不把键剥回原始 sessionKey。
	if sessionKey == "" || len(sessionKey) > maxStickyKeyBytes {
		return generateOpencodeSessionID()
	}
	cacheKey := sessionUUIDCacheKey{sessionKey: sessionKey, channelID: channelID, keyID: keyID}
	now := time.Now().UnixMilli()

	// 读路径: 命中且未过期时滑动续期。续期需要写锁; 为避免每次命中都抢写锁,
	// 仅在条目进入后半段 TTL(剩余不足一半)时续期——前半段条目本就新鲜, 无需续期。
	// 这仍保证活跃会话的 TTL 被持续刷新, 不会因长期活跃而过期。
	sessionUUIDs.RLock()
	entry, ok := sessionUUIDs.entries[cacheKey]
	sessionUUIDs.RUnlock()
	if ok && entry.ExpireAtUnixMilli > now {
		if entry.ExpireAtUnixMilli-now > sessionUUIDTTLMilli/2 {
			// 仍在 TTL 前半段, 直接复用, 不抢写锁。
			return entry.UUID
		}
		// 进入后半段, 滑动续期 TTL。
		sessionUUIDs.Lock()
		// 双检: 续期前再次确认条目仍存在且仍为本值, 避免并发淘汰/刷新造成回退。
		if current, still := sessionUUIDs.entries[cacheKey]; still && current.UUID == entry.UUID {
			current.ExpireAtUnixMilli = now + sessionUUIDTTLMilli
			sessionUUIDs.entries[cacheKey] = current
		}
		sessionUUIDs.Unlock()
		return entry.UUID
	}

	value := generateOpencodeSessionID()
	sessionUUIDs.Lock()
	defer sessionUUIDs.Unlock()
	// 双检: 写入前再次检查, 避免并发请求为同一命名空间 mint 出两个不同 UUID。
	if existing, ok := sessionUUIDs.entries[cacheKey]; ok && existing.ExpireAtUnixMilli > now {
		return existing.UUID
	}
	// 顺带清理过期残留并按容量淘汰最旧: 按间隔节流, 过期条目在读取路径本就会被即时忽略。
	last := sessionUUIDLastPrune.Load()
	if now-last >= sessionUUIDPruneInterval && sessionUUIDLastPrune.CompareAndSwap(last, now) {
		pruneSessionUUIDsLocked(now)
	}
	sessionUUIDs.entries[cacheKey] = sessionUUIDEntry{UUID: value, ExpireAtUnixMilli: now + sessionUUIDTTLMilli}
	// 容量上限保护: 即便未到清理间隔, 超容量时也立即淘汰最旧条目, 防止异常流量撑爆映射。
	evictSessionUUIDsLocked()
	return value
}

// pruneSessionUUIDsLocked 清理已过期的会话 UUID 残留; 调用方必须持有写锁。
func pruneSessionUUIDsLocked(now int64) {
	for key, entry := range sessionUUIDs.entries {
		if entry.ExpireAtUnixMilli <= now {
			delete(sessionUUIDs.entries, key)
		}
	}
}

// evictSessionUUIDsLocked 在缓存超过容量上限时淘汰最旧(ExpireAtUnixMilli 最小, 即最久未续期)
// 的条目, 直至回到上限以内; 调用方必须持有写锁。O(n) 扫描仅在超容量时触发, 正常流量下为零开销。
func evictSessionUUIDsLocked() {
	for len(sessionUUIDs.entries) > sessionUUIDMaxEntries {
		var oldestKey sessionUUIDCacheKey
		var oldestExpiry int64
		first := true
		for key, entry := range sessionUUIDs.entries {
			if first || entry.ExpireAtUnixMilli < oldestExpiry {
				oldestKey = key
				oldestExpiry = entry.ExpireAtUnixMilli
				first = false
			}
		}
		delete(sessionUUIDs.entries, oldestKey)
	}
}

// collectChannelRandomHeaders 返回渠道当前应注入的动态头名集合(保持顺序、去重):
// opencode 兼容开关开启时加入 x-opencode-session, 再并入全局随机头规则指向的头名。
func collectChannelRandomHeaders(channel model.Channel) []string {
	var keys []string
	seen := make(map[string]bool)
	if channel.OpencodeCompat {
		keys = append(keys, opencodeSessionHeader)
		seen[opencodeSessionHeader] = true
	}
	for _, key := range op.ChannelRandomHeaderKeys(channel.ID) {
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

// injectRandomHeaders 在静态自定义 Header 之后注入会话级动态头。
// x-trace-id 等普通动态头复用调用方传入的 randomValue。
// x-opencode-session 不走这个共享值: 转发路径会在本函数之后用单独解析的会话号覆盖。
// 没有单独会话号、且头尚未存在时, 才用 randomValue 占位, 让没有客户端会话的探测请求仍能带上头。
// 无任何动态头可注入或随机值为空时立即返回(零开销快速路径)。
// 头名合法性已在校验阶段拒绝敏感头, 此处无需再守卫上游认证凭据。
func injectRandomHeaders(channel model.Channel, randomValue string, request *httpclient.Request) {
	keys := collectChannelRandomHeaders(channel)
	if len(keys) == 0 || randomValue == "" {
		return
	}
	for _, key := range keys {
		if strings.EqualFold(key, opencodeSessionHeader) {
			continue
		}
		request.Headers.Set(key, randomValue)
	}
	if channel.OpencodeCompat && request.Headers.Get(opencodeSessionHeader) == "" {
		request.Headers.Set(opencodeSessionHeader, randomValue)
	}
}

// applyResolvedOpencodeSession 把本请求已经解析好的 x-opencode-session 写到上游请求。
// OpencodeCompat 为假或会话值为空时不注入。调用方应在 injectRandomHeaders 之后调用,
// 以便覆盖共享随机值占位和静态自定义头。
func applyResolvedOpencodeSession(channel model.Channel, session string, request *httpclient.Request) {
	if !channel.OpencodeCompat || session == "" || request == nil {
		return
	}
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	request.Headers.Set(opencodeSessionHeader, session)
}

// resolveRequestRandomValue 在请求级一次性解析本请求所有随机头应使用的稳定会话 ID:
// 同一请求的所有头与所有重试复用同一值, 不在每次重试或每个头各调用一次。
// sessionKey 为空时生成请求级独立 ID(不进缓存); 非空时按 (sessionKey, 渠道, Key) 命名空间
// 复用缓存值, 保证同一会话在同一渠道/Key 下的跨请求稳定性。
// channelID 与 keyID 取本请求首次发起上游时所选渠道与 Key, 命名空间隔离不同上游的会话。
func resolveRequestRandomValue(sessionKey string, channelID int, keyID string) string {
	return sessionUUIDFor(sessionKey, channelID, keyID)
}
