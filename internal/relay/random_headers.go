package relay

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm/httpclient"
)

// opencodeSessionHeader opencode 兼容请求头的固定头名。
const opencodeSessionHeader = "x-opencode-session"

// opencodeIDCharset opencode ID 随机后缀使用的字符集: [0-9A-Za-z], 共 62 个字符。
// 与 opencode 二进制中的字符表完全一致。
const opencodeIDCharset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// opencodeIDCounter 与 opencodeIDTimestamp 实现 opencode 的 ID 计数器:
// 同一毫秒内多次生成时计数器递增, 跨毫秒时重置为 0。与 opencode 的 tU() 函数行为一致。
var opencodeIDCounter atomic.Int64
var opencodeIDTimestamp atomic.Int64

// generateOpencodeSessionID 使用 opencode 的 ID 生成算法生成会话 ID:
//   - 前缀 "ses_"
//   - 12 个十六进制字符: 由 ~(timestamp_ms * 4096 + counter) 的高 6 字节(大端)编码
//   - 14 个随机字符: 从 [0-9A-Za-z] 中选取
//
// opencode.ai/zen 上游会验证 x-opencode-session 的值格式, 非 opencode 格式的值
// (如随机 UUID)会被拒绝并返回 "FreeTierError: OpenCode's free tier can only be
// used from within OpenCode"。算法逆向自 opencode 二进制中的 tU(!0) 函数。
func generateOpencodeSessionID() string {
	now := time.Now().UnixMilli()

	// 计数器管理: 时间戳变更时重置, 每次调用递增。与 opencode 的 tU() 行为一致。
	stamp := opencodeIDTimestamp.Load()
	if stamp != now {
		if opencodeIDTimestamp.CompareAndSwap(stamp, now) {
			opencodeIDCounter.Store(0)
		}
	}
	counter := opencodeIDCounter.Add(1)

	// val = timestamp_ms * 4096 + counter, 与 opencode 的 BigInt(Y)*0x1000n+BigInt(cU) 一致。
	val := now*0x1000 + counter

	// 会话 ID 使用按位取反(^), 与 opencode 的 tU(true) → ~$ 一致。
	// Go 的 int64 对负数的 >> 做算术右移(符号扩展), 与 JS BigInt 行为一致。
	inverted := ^val

	// 提取高 6 字节(大端序) → 12 个十六进制字符。
	buf := make([]byte, 6)
	for i := 0; i < 6; i++ {
		buf[i] = byte((inverted >> uint(40-8*i)) & 0xff)
	}
	hexPart := hex.EncodeToString(buf)

	// 生成 14 个随机字符, 从 [0-9A-Za-z] 中选取。
	randBuf := make([]byte, 14)
	if _, err := rand.Read(randBuf); err != nil {
		// crypto/rand 失败时的降级: 用时间戳填充, 极低概率发生。
		ts := time.Now().UnixNano()
		for i := range randBuf {
			randBuf[i] = byte(ts >> uint((i*8)%64))
		}
	}
	randPart := make([]byte, 14)
	for i, b := range randBuf {
		randPart[i] = opencodeIDCharset[b%62]
	}

	return "ses_" + hexPart + string(randPart)
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
	if sessionKey == "" {
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

// injectRandomHeaders 在静态自定义 Header 之后注入会话级动态头: 同一请求的所有头复用
// 同一随机值(由调用方在请求级一次性解析并传入), 天然覆盖同名静态头(http.Header.Set 为覆盖写);
// 无任何动态头可注入或随机值为空时立即返回(零开销快速路径)。
// 头名合法性已在校验阶段拒绝敏感头, 此处无需再守卫上游认证凭据。
func injectRandomHeaders(channel model.Channel, randomValue string, request *httpclient.Request) {
	keys := collectChannelRandomHeaders(channel)
	if len(keys) == 0 || randomValue == "" {
		return
	}
	for _, key := range keys {
		request.Headers.Set(key, randomValue)
	}
}

// resolveRequestRandomValue 在请求级一次性解析本请求所有随机头应使用的稳定会话 ID:
// 同一请求的所有头与所有重试复用同一值, 不在每次重试或每个头各调用一次。
// sessionKey 为空时生成请求级独立 ID(不进缓存); 非空时按 (sessionKey, 渠道, Key) 命名空间
// 复用缓存值, 保证同一会话在同一渠道/Key 下的跨请求稳定性。
// channelID 与 keyID 取本请求首次发起上游时所选渠道与 Key, 命名空间隔离不同上游的会话。
func resolveRequestRandomValue(sessionKey string, channelID int, keyID string) string {
	return sessionUUIDFor(sessionKey, channelID, keyID)
}
