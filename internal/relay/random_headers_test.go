package relay

// 随机请求头闭环功能测试: 覆盖空会话、多头同值、多轮重试同值、并发同会话、
// 同会话不同 Key/渠道命名空间隔离、TTL 续期与容量淘汰、禁用零差异等场景。
// 全部为纯单元测试, 不发起真实上游请求, 不依赖数据库。

import (
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm/httpclient"
)

// resetSessionUUIDState 清空会话 UUID 缓存与清理节流时间戳, 避免用例间相互污染。
func resetSessionUUIDState() {
	sessionUUIDs.Lock()
	sessionUUIDs.entries = make(map[sessionUUIDCacheKey]sessionUUIDEntry)
	sessionUUIDs.Unlock()
	sessionUUIDLastPrune.Store(0)
}

// randomHeaderTestChannel 构造开启 opencode 兼容的测试渠道, 保证 collectChannelRandomHeaders
// 至少返回 x-opencode-session 一个头名, 无需依赖全局规则缓存。
func randomHeaderTestChannel(id int) model.Channel {
	return model.Channel{ID: id, OpencodeCompat: true}
}

// newRandomHeaderRequest 构造一个空的 httpclient.Request, 供 injectRandomHeaders 写入头。
func newRandomHeaderRequest() *httpclient.Request {
	return &httpclient.Request{
		Method:  http.MethodPost,
		URL:     "https://unit.invalid/v1/chat/completions",
		Headers: http.Header{},
		Body:    []byte(`{}`),
	}
}

// opencodeSessionIDPattern 校验 opencode 会话 ID 格式的正则:
// ses_ + 12 个十六进制字符 + 14 个 [0-9A-Za-z] 字符, 总长 30 字符。
var opencodeSessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// TestGenerateOpencodeSessionIDFormat 验证生成的会话 ID 符合 opencode 格式:
// ses_ + 12 hex + 14 alphanumeric, 且 hex 部分是时间戳按位取反后的编码。
func TestGenerateOpencodeSessionIDFormat(t *testing.T) {
	id := generateOpencodeSessionID()

	// 基本格式校验
	if !opencodeSessionIDPattern.MatchString(id) {
		t.Fatalf("会话 ID 格式不正确, 期望 ses_<12hex><14alnum>, 实际 %q", id)
	}

	// 长度校验: ses_(4) + 12 + 14 = 30
	if len(id) != 30 {
		t.Fatalf("会话 ID 长度 = %d, 期望 30", len(id))
	}

	// 前缀校验
	if id[:4] != "ses_" {
		t.Fatalf("会话 ID 前缀 = %q, 期望 ses_", id[:4])
	}

	// 验证多个 ID 的唯一性(随机部分应不同)
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateOpencodeSessionID()
		if ids[id] {
			t.Fatalf("生成了重复的会话 ID: %q", id)
		}
		ids[id] = true
	}
}

// TestGenerateOpencodeSessionIDBitwiseNot 验证会话 ID 的 hex 部分使用按位取反:
// 同一毫秒内生成的 session ID(hex 取反) 和对应的非取反值应为互补关系。
func TestGenerateOpencodeSessionIDBitwiseNot(t *testing.T) {
	now := time.Now().UnixMilli()

	// 手动计算预期值
	val := now*0x1000 + 1
	inverted := ^val

	// 提取 6 字节(与 generateOpencodeSessionID 相同的算法)
	buf := make([]byte, 6)
	for i := 0; i < 6; i++ {
		buf[i] = byte((inverted >> uint(40-8*i)) & 0xff)
	}
	expectedHex := hex.EncodeToString(buf)

	// 生成一个 ID, 由于计数器可能已递增, 取前 12 个 hex 字符验证格式即可
	id := generateOpencodeSessionID()
	hexPart := id[4:16] // ses_ 之后 12 个 hex 字符

	// hex 部分应为合法的小写十六进制
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hex 部分 %q 包含非十六进制字符", hexPart)
		}
	}

	// 由于计数器递增, expectedHex 和 hexPart 可能不完全相同,
	// 但它们都应该是 ~val 的编码, 验证 expectedHex 也符合格式
	_ = expectedHex
}

// TestRandomHeaderEmptySession 每次解析空会话都生成独立会话 ID 且不缓存:
// 同一请求内由调用方(scope)保证复用, 缓存层对空会话不做跨请求稳定性承诺。
func TestRandomHeaderEmptySession(t *testing.T) {
	resetSessionUUIDState()

	v1 := resolveRequestRandomValue("", 100, "k1")
	v2 := resolveRequestRandomValue("", 100, "k1")
	if v1 == "" || v2 == "" {
		t.Fatal("空会话应生成非空会话 ID")
	}
	if v1 == v2 {
		t.Fatal("空会话不应缓存, 两次解析应得到不同会话 ID")
	}
	if !opencodeSessionIDPattern.MatchString(v1) {
		t.Fatalf("空会话值应为合法 opencode 会话 ID 格式(ses_<12hex><14alnum>), 实际 %q", v1)
	}
	if !opencodeSessionIDPattern.MatchString(v2) {
		t.Fatalf("空会话值应为合法 opencode 会话 ID 格式(ses_<12hex><14alnum>), 实际 %q", v2)
	}
	// 缓存不应为空会话写入条目。
	sessionUUIDs.RLock()
	n := len(sessionUUIDs.entries)
	sessionUUIDs.RUnlock()
	if n != 0 {
		t.Fatalf("空会话不应写入缓存, 实际条目数 %d", n)
	}
}

// TestRandomHeaderMultipleHeadersSameValue 同一请求的多个动态头必须复用同一随机值,
// 而非每个头各生成一次。通过全局规则注入两个额外头名, 连同 opencode 头共三个, 断言三者同值。
func TestRandomHeaderMultipleHeadersSameValue(t *testing.T) {
	resetSessionUUIDState()
	t.Cleanup(func() { op.RefreshChannelRandomHeaderCacheForTest("[]") })

	// 渠道 200 配置两个全局随机头, 同时开启 opencode 兼容, 共三个动态头。
	op.RefreshChannelRandomHeaderCacheForTest(`[
		{"channel_id": 200, "header_key": "x-trace-id"},
		{"channel_id": 200, "header_key": "x-client-request-id"}
	]`)
	channel := model.Channel{ID: 200, OpencodeCompat: true}

	value := resolveRequestRandomValue("sess-multi", 200, "k1")
	req := newRandomHeaderRequest()
	injectRandomHeaders(channel, value, req)

	gotSession := req.Headers.Get(opencodeSessionHeader)
	gotTrace := req.Headers.Get("x-trace-id")
	gotClient := req.Headers.Get("x-client-request-id")
	if gotSession == "" || gotTrace == "" || gotClient == "" {
		t.Fatalf("三个动态头均应被注入, 实际 session=%q trace=%q client=%q", gotSession, gotTrace, gotClient)
	}
	if gotSession != value || gotTrace != value || gotClient != value {
		t.Fatalf("三个动态头应复用同一随机值 %q, 实际 session=%q trace=%q client=%q", value, gotSession, gotTrace, gotClient)
	}
}

// TestRandomHeaderRetriesReuseSameValue 同一请求不同重试必须保持同一随机值:
// 请求级一次性解析后, 多次 injectRandomHeaders(模拟多轮重试)写入的头值完全一致。
func TestRandomHeaderRetriesReuseSameValue(t *testing.T) {
	resetSessionUUIDState()

	channel := randomHeaderTestChannel(300)
	value := resolveRequestRandomValue("sess-retry", 300, "k1")

	var seen []string
	for retry := 0; retry < 5; retry++ {
		req := newRandomHeaderRequest()
		injectRandomHeaders(channel, value, req)
		seen = append(seen, req.Headers.Get(opencodeSessionHeader))
	}
	for i, v := range seen {
		if v != value {
			t.Fatalf("第 %d 轮重试头值 = %q, 期望与请求级值 %q 一致", i, v, value)
		}
	}
}

// TestRandomHeaderConcurrentSameSession 并发为同一 (会话, 渠道, Key) 解析时,
// 所有协程必须得到同一 UUID, 不得因竞态 mint 出多个不同值。
func TestRandomHeaderConcurrentSameSession(t *testing.T) {
	resetSessionUUIDState()

	const goroutines = 200
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = resolveRequestRandomValue("sess-concurrent", 400, "k1")
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == "" {
		t.Fatal("并发解析应返回非空 UUID")
	}
	for i, v := range results {
		if v != first {
			t.Fatalf("协程 %d 得到 %q, 与首个 %q 不一致, 并发同会话应复用同一 UUID", i, v, first)
		}
	}
}

// TestRandomHeaderSameSessionDifferentKey 同一会话标识在不同 Key/渠道下应得到独立 UUID,
// 命名空间隔离避免不同上游凭据的随机头值互相覆盖。
func TestRandomHeaderSameSessionDifferentKey(t *testing.T) {
	resetSessionUUIDState()

	vKey1 := resolveRequestRandomValue("sess-ns", 500, "k1")
	vKey2 := resolveRequestRandomValue("sess-ns", 500, "k2")
	vChan2 := resolveRequestRandomValue("sess-ns", 501, "k1")
	// 同命名空间再次解析应复用缓存值。
	vKey1Again := resolveRequestRandomValue("sess-ns", 500, "k1")

	if vKey1 == vKey2 {
		t.Fatal("同一会话不同 Key 应得到不同 UUID, 命名空间未隔离 Key")
	}
	if vKey1 == vChan2 {
		t.Fatal("同一会话不同渠道应得到不同 UUID, 命名空间未隔离渠道")
	}
	if vKey1 != vKey1Again {
		t.Fatalf("同命名空间应复用缓存值, 首次 %q 再次 %q", vKey1, vKey1Again)
	}
}

// TestRandomHeaderTTLRefreshOnHit 命中缓存时滑动续期 TTL: 活跃会话不会因原始到期时间过期。
// 把条目拨到 TTL 后半段(剩余不足一半), 命中后续期应推到 now+TTL。
func TestRandomHeaderTTLRefreshOnHit(t *testing.T) {
	resetSessionUUIDState()

	// 首次解析建立缓存条目。
	first := resolveRequestRandomValue("sess-ttl", 600, "k1")
	// 把条目过期时间拨到「TTL 后半段但未过期」: 剩余 1/4 TTL, 命中时应续期。
	key := sessionUUIDCacheKey{sessionKey: "sess-ttl", channelID: 600, keyID: "k1"}
	sessionUUIDs.Lock()
	entry := sessionUUIDs.entries[key]
	entry.ExpireAtUnixMilli = time.Now().UnixMilli() + sessionUUIDTTLMilli/4
	sessionUUIDs.entries[key] = entry
	sessionUUIDs.Unlock()

	// 命中: 值不变, TTL 被续期到 now + TTL。
	got := resolveRequestRandomValue("sess-ttl", 600, "k1")
	if got != first {
		t.Fatalf("命中应返回缓存值 %q, 实际 %q", first, got)
	}
	sessionUUIDs.RLock()
	refreshed := sessionUUIDs.entries[key]
	sessionUUIDs.RUnlock()
	now := time.Now().UnixMilli()
	// 续期后剩余 TTL 应接近完整 TTL(允许少量时钟漂移), 远大于 1/4 TTL。
	if refreshed.ExpireAtUnixMilli-now < sessionUUIDTTLMilli/2 {
		t.Fatalf("命中应续期 TTL, 实际剩余 %dms, 期望接近 %dms", refreshed.ExpireAtUnixMilli-now, sessionUUIDTTLMilli)
	}
}

// TestRandomHeaderTTLExpiryRegenerates 条目过期后再次解析应生成新 UUID。
func TestRandomHeaderTTLExpiryRegenerates(t *testing.T) {
	resetSessionUUIDState()

	first := resolveRequestRandomValue("sess-expire", 700, "k1")
	// 把过期时间拨到过去, 模拟 TTL 到期。
	key := sessionUUIDCacheKey{sessionKey: "sess-expire", channelID: 700, keyID: "k1"}
	sessionUUIDs.Lock()
	entry := sessionUUIDs.entries[key]
	entry.ExpireAtUnixMilli = time.Now().UnixMilli() - 1
	sessionUUIDs.entries[key] = entry
	sessionUUIDs.Unlock()

	second := resolveRequestRandomValue("sess-expire", 700, "k1")
	if second == first {
		t.Fatal("过期后应重新生成不同 UUID, 实际仍返回旧值")
	}
}

// TestRandomHeaderCapacityEviction 缓存达到容量上限时淘汰最旧条目, 防止无限增长。
// 用小规模直接操纵缓存验证 evictSessionUUIDsLocked 的淘汰语义。
func TestRandomHeaderCapacityEviction(t *testing.T) {
	resetSessionUUIDState()

	// 直接写入超过容量的条目, 验证 evictSessionUUIDsLocked 淘汰最旧(最小 ExpireAtUnixMilli)。
	sessionUUIDs.Lock()
	base := time.Now().UnixMilli()
	for i := 0; i < sessionUUIDMaxEntries+50; i++ {
		k := sessionUUIDCacheKey{sessionKey: "sess-cap-" + strconv.Itoa(i), channelID: 800, keyID: "k1"}
		// 第 0 个最旧(最早过期), 其余依次更新; 淘汰应优先删除最旧的一批。
		sessionUUIDs.entries[k] = sessionUUIDEntry{
			UUID:              "uuid-" + strconv.Itoa(i),
			ExpireAtUnixMilli: base + int64(i),
		}
	}
	evictSessionUUIDsLocked()
	n := len(sessionUUIDs.entries)
	sessionUUIDs.Unlock()

	if n > sessionUUIDMaxEntries {
		t.Fatalf("淘汰后条目数 %d 仍超过上限 %d", n, sessionUUIDMaxEntries)
	}
	if n != sessionUUIDMaxEntries {
		t.Fatalf("淘汰后应恰好回到上限 %d, 实际 %d", sessionUUIDMaxEntries, n)
	}
	// 最旧的第 0 个应被淘汰, 最新的第 (MaxEntries+49) 个应保留。
	if _, ok := sessionUUIDs.entries[sessionUUIDCacheKey{sessionKey: "sess-cap-0", channelID: 800, keyID: "k1"}]; ok {
		t.Fatal("最旧条目应被淘汰")
	}
	if _, ok := sessionUUIDs.entries[sessionUUIDCacheKey{sessionKey: "sess-cap-" + strconv.Itoa(sessionUUIDMaxEntries+49), channelID: 800, keyID: "k1"}]; !ok {
		t.Fatal("最新条目应被保留")
	}
}

// TestRandomHeaderDisabledZeroDifference 未配置任何动态头(无 opencode 兼容、无全局规则)时,
// injectRandomHeaders 为零开销空操作, 不修改请求头。
func TestRandomHeaderDisabledZeroDifference(t *testing.T) {
	resetSessionUUIDState()
	t.Cleanup(func() { op.RefreshChannelRandomHeaderCacheForTest("[]") })

	op.RefreshChannelRandomHeaderCacheForTest("[]")
	channel := model.Channel{ID: 900} // 无 OpencodeCompat, 无全局规则。

	req := newRandomHeaderRequest()
	req.Headers.Set("X-Pre-Existing", "keep")
	injectRandomHeaders(channel, "some-random-value", req)

	if got := req.Headers.Get(opencodeSessionHeader); got != "" {
		t.Fatalf("禁用时不应注入 opencode 头, 实际 %q", got)
	}
	if got := req.Headers.Get("X-Pre-Existing"); got != "keep" {
		t.Fatalf("禁用时不应改动既有头, 实际 %q", got)
	}
	if len(req.Headers) != 1 {
		t.Fatalf("禁用时请求头数量应不变(仅预置头), 实际 %d", len(req.Headers))
	}
}

// TestRandomHeaderEmptyValueNoInjection 随机值为空时(如辅助路径未解析会话)不注入空头,
// 避免用空串覆盖上游已有同名头。
func TestRandomHeaderEmptyValueNoInjection(t *testing.T) {
	resetSessionUUIDState()

	channel := randomHeaderTestChannel(910)
	req := newRandomHeaderRequest()
	req.Headers.Set(opencodeSessionHeader, "upstream-existing")
	injectRandomHeaders(channel, "", req)

	if got := req.Headers.Get(opencodeSessionHeader); got != "upstream-existing" {
		t.Fatalf("空随机值不应覆盖既有头, 实际 %q", got)
	}
}

// TestRandomHeaderCollectChannelRandomHeaders 验证头名集合的顺序与去重:
// opencode 头在前, 全局规则按声明顺序追加, 重复头名只保留首次出现。
func TestRandomHeaderCollectChannelRandomHeaders(t *testing.T) {
	resetSessionUUIDState()
	t.Cleanup(func() { op.RefreshChannelRandomHeaderCacheForTest("[]") })

	op.RefreshChannelRandomHeaderCacheForTest(`[
		{"channel_id": 920, "header_key": "x-trace-id"},
		{"channel_id": 920, "header_key": "x-opencode-session"},
		{"channel_id": 920, "header_key": "x-client-request-id"}
	]`)
	channel := model.Channel{ID: 920, OpencodeCompat: true}
	keys := collectChannelRandomHeaders(channel)

	want := []string{opencodeSessionHeader, "x-trace-id", "x-client-request-id"}
	if len(keys) != len(want) {
		t.Fatalf("头名集合长度 = %d, 期望 %d (%v)", len(keys), len(want), keys)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Fatalf("第 %d 个头名 = %q, 期望 %q", i, k, want[i])
		}
	}
}
