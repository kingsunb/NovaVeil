package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func warpTestPool(slots ...warpSlotInfo) *warpSlotPool {
	return &warpSlotPool{
		fetchedAt: time.Now(),
		snapshot:  warpSlotsSnapshot{Size: len(slots), Slots: slots},
		reserved:  map[int]int{},
		suspect:   map[int]time.Time{},
		votes:     map[string]time.Time{},
	}
}

// 选槽必须避开在途槽, 并在同负载时轮询。
func TestWarpSlotPoolPickRoundRobinAndRelease(t *testing.T) {
	pool := warpTestPool(
		warpSlotInfo{Slot: 0, Account: "warp-0", Egress: "a", Prefix: "p0/64", State: "active"},
		warpSlotInfo{Slot: 1, Account: "warp-1", Egress: "b", Prefix: "p1/64", State: "active", InFlight: 1},
		warpSlotInfo{Slot: 2, Account: "warp-2", Egress: "c", Prefix: "p2/64", State: "active"},
	)

	first, err := pool.pick("")
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if first.Account() != "warp-0" {
		t.Fatalf("首个槽位 = %q, 期望 warp-0", first.Account())
	}
	if pool.reserved[0] != 1 {
		t.Fatalf("槽位 0 应被占位")
	}
	first.Release()
	first.Release()
	if len(pool.reserved) != 0 {
		t.Fatalf("Release 后不应残留占位: %+v", pool.reserved)
	}

	second, err := pool.pick("")
	if err != nil {
		t.Fatalf("第二次 pick: %v", err)
	}
	if second.Account() != "warp-2" {
		t.Fatalf("轮询应选中槽位 2, 实际 %q", second.Account())
	}
	second.Release()
}

// 全槽在途时返回可重试的忙错误。
func TestWarpSlotPoolPickBusy(t *testing.T) {
	pool := warpTestPool(
		warpSlotInfo{Slot: 0, Account: "warp-0", Egress: "a", State: "active", InFlight: 1},
		warpSlotInfo{Slot: 1, Account: "warp-1", Egress: "b", State: "busy"},
	)
	_, err := pool.pick("")
	busy, ok := err.(*warpSlotsBusyError)
	if !ok {
		t.Fatalf("期望忙错误, 实际 %v", err)
	}
	if busy.RetryAfter < time.Second {
		t.Fatalf("RetryAfter 应至少 1s, 实际 %v", busy.RetryAfter)
	}
}

// 同一会话不重复使用同一槽位, 槽位耗尽后允许复用, 其他会话不受影响。
func TestWarpSlotPoolPickSessionDedup(t *testing.T) {
	pool := warpTestPool(
		warpSlotInfo{Slot: 0, Account: "warp-0", Egress: "a", State: "active"},
		warpSlotInfo{Slot: 1, Account: "warp-1", Egress: "b", State: "active"},
		warpSlotInfo{Slot: 2, Account: "warp-2", Egress: "c", State: "active"},
	)

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		lease, err := pool.pick("s1")
		if err != nil {
			t.Fatalf("第 %d 次 pick: %v", i+1, err)
		}
		if seen[lease.Account()] {
			t.Fatalf("同会话重复槽位 %q", lease.Account())
		}
		seen[lease.Account()] = true
		lease.Release()
	}
	if len(seen) != 3 {
		t.Fatalf("同会话应占满 3 个不同槽位, 实际 %d", len(seen))
	}

	fourth, err := pool.pick("s1")
	if err != nil {
		t.Fatalf("槽位耗尽后应允许复用: %v", err)
	}
	fourth.Release()

	other, err := pool.pick("s2")
	if err != nil {
		t.Fatalf("其他会话 pick: %v", err)
	}
	other.Release()
}

// 两个不同 /64 才触发全槽冷却, 重复前缀不计数。
func TestWarpSlotRateLimitQuorum(t *testing.T) {
	pool := warpTestPool()
	lease := func(slot int, prefix string) *warpSlotLease {
		return &warpSlotLease{pool: pool, slot: slot, prefix: prefix}
	}
	if lease(0, "a/64").MarkRateLimited() {
		t.Fatal("单前缀不应触发全槽冷却")
	}
	if lease(1, "a/64").MarkRateLimited() {
		t.Fatal("重复前缀不应计数")
	}
	if !lease(2, "b/64").MarkRateLimited() {
		t.Fatal("两个不同前缀应触发全槽冷却")
	}
	if _, err := pool.pick(""); err == nil {
		t.Fatal("全槽冷却期间应拒绝派发")
	}
}

// warpTestLease 构造只带上报字段的租约。
func warpTestLease(pool *warpSlotPool, egress string) *warpSlotLease {
	return &warpSlotLease{
		pool:    pool,
		slot:    0,
		account: "warp-0",
		egress:  egress,
		prefix:  "p/64",
	}
}

// waitWarpReports 轮询等待条件成立, 最多等 timeout。
func waitWarpReports(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// 成功上报需带出口参数, 十分钟内只报一次。
func TestWarpSlotMarkSuccessReportsEgressOnce(t *testing.T) {
	var calls atomic.Int32
	egresses := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nodes/success" {
			return
		}
		calls.Add(1)
		egresses <- r.URL.Query().Get("egress")
	}))
	defer server.Close()

	pool := warpTestPool()
	pool.controlBase = server.URL
	lease := warpTestLease(pool, "203.0.113.7")

	lease.MarkSuccess()
	if !waitWarpReports(t, time.Second, func() bool { return calls.Load() == 1 }) {
		t.Fatal("未收到 /nodes/success 上报")
	}
	select {
	case got := <-egresses:
		if got != "203.0.113.7" {
			t.Fatalf("egress 参数 = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("缺少 egress 参数")
	}

	lease.MarkSuccess()
	if waitWarpReports(t, 200*time.Millisecond, func() bool { return calls.Load() > 1 }) {
		t.Fatalf("十分钟内只应上报一次, 实际 %d 次", calls.Load())
	}
}

// 429 上报需带出口与原因, 不做去重。
func TestWarpSlotMarkRateLimitedReportsFail(t *testing.T) {
	type nodeReport struct{ egress, reason string }
	reports := make(chan nodeReport, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nodes/fail" {
			return
		}
		q := r.URL.Query()
		reports <- nodeReport{egress: q.Get("egress"), reason: q.Get("reason")}
	}))
	defer server.Close()

	pool := warpTestPool()
	pool.controlBase = server.URL
	lease := warpTestLease(pool, "198.51.100.9")

	lease.MarkRateLimited()
	select {
	case got := <-reports:
		if got.egress != "198.51.100.9" || got.reason != "429" {
			t.Fatalf("429 上报 = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 /nodes/fail 上报")
	}
}

// 控制地址为空时不得发请求, 也不记录上报。
func TestWarpSlotReportSkipsWithoutControlBase(t *testing.T) {
	pool := warpTestPool()
	lease := warpTestLease(pool, "203.0.113.7")
	lease.MarkSuccess()
	lease.MarkRateLimited()
	if len(pool.reported) != 0 {
		t.Fatalf("缺少控制地址时不应记录上报: %+v", pool.reported)
	}
}

// /slots 缓存一秒钟, 过期后重新拉取。
func TestWarpSlotPoolRefreshCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(warpSlotsSnapshot{
			Size: 1,
			Slots: []warpSlotInfo{
				{Slot: 0, Account: "warp-0", Egress: "a", Prefix: "p/64", State: "active"},
			},
		})
	}))
	defer server.Close()

	pool := &warpSlotPool{
		slotsURL: server.URL,
		client:   server.Client(),
		reserved: map[int]int{},
		suspect:  map[int]time.Time{},
		votes:    map[string]time.Time{},
	}
	if err := pool.refresh(t.Context()); err != nil {
		t.Fatalf("首次刷新: %v", err)
	}
	if err := pool.refresh(t.Context()); err != nil {
		t.Fatalf("缓存内刷新: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("一秒内应只请求一次, 实际 %d", got)
	}
	pool.mu.Lock()
	pool.fetchedAt = time.Now().Add(-2 * warpSlotsCacheTTL)
	pool.mu.Unlock()
	if err := pool.refresh(t.Context()); err != nil {
		t.Fatalf("过期刷新: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("过期后应重新请求, 实际 %d", got)
	}
}

// 槽位数量与 size 不一致时 fail-fast。
func TestWarpSlotPoolRefreshRejectsBadSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"size":2,"slots":[{"slot":0}]}`))
	}))
	defer server.Close()
	pool := &warpSlotPool{slotsURL: server.URL, client: server.Client()}
	if err := pool.refresh(t.Context()); err == nil {
		t.Fatal("size 与槽位数量不一致应报错")
	}
}

// 控制地址从渠道代理推导, 占位符不能破坏 URL 解析。
func TestWarpControlBase(t *testing.T) {
	template := "socks5h://{account}:pw@NovaVeil_WARP_socks5:1080"
	channel := model.Channel{ChannelProxy: &template}
	base, err := warpControlBase(channel)
	if err != nil {
		t.Fatalf("warpControlBase: %v", err)
	}
	if base != "http://NovaVeil_WARP_socks5:1081" {
		t.Fatalf("控制地址 = %q", base)
	}
}

// 代理自身 407/503 必须与出口网络失败区分。
func TestIsWarpProxyRejection(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"proxyconnect tcp: 503 Service Unavailable", true},
		{"proxyconnect tcp: 407 Proxy Authentication Required", true},
		{"socks connect tcp: connection not allowed by ruleset", true},
		{"dial tcp 1.1.1.1:443: i/o timeout", false},
	}
	for _, tc := range cases {
		if got := isWarpProxyRejection(errString(tc.text)); got != tc.want {
			t.Fatalf("isWarpProxyRejection(%q) = %v, 期望 %v", tc.text, got, tc.want)
		}
	}
	if isWarpProxyRejection(nil) {
		t.Fatal("nil 不应判为代理拒绝")
	}
}

// HTTP 代理 CONNECT 的 503 只保留原因短语, 需单独识别。
func TestIsWarpSlotUnavailable(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Get \"https://api.example.com\": Service Unavailable", true},
		{"Get \"https://api.example.com\": Bad Gateway", false},
		{"dial tcp 1.1.1.1:443: i/o timeout", false},
	}
	for _, tc := range cases {
		if got := isWarpSlotUnavailable(errString(tc.text)); got != tc.want {
			t.Fatalf("isWarpSlotUnavailable(%q) = %v, 期望 %v", tc.text, got, tc.want)
		}
	}
	if isWarpSlotUnavailable(nil) {
		t.Fatal("nil 不应判为槽位不可用")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestWarpSlotsEndpointMarksPlaceholder(t *testing.T) {
	template := "socks5h://{account}:pw@NovaVeil_WARP_socks5:1080"
	channel := model.Channel{ChannelProxy: &template}
	endpoint, err := warpSlotsEndpoint(channel)
	if err != nil {
		t.Fatalf("warpSlotsEndpoint: %v", err)
	}
	if !strings.HasSuffix(endpoint, "/slots") {
		t.Fatalf("端点 = %q", endpoint)
	}
}

// 模板占位符应替换为完整槽位账号, 不能叠加 warp- 前缀。
func TestWarpSlotTemplateUsesSlotAccount(t *testing.T) {
	template := "socks5h://{account}:pw@NovaVeil_WARP_socks5:1080"
	resolved, err := helper.ResolveProxyTemplate(template, "warp-0")
	if err != nil {
		t.Fatalf("ResolveProxyTemplate: %v", err)
	}
	if got := resolved.User.Username(); got != "warp-0" {
		t.Fatalf("代理用户名 = %q, 期望 warp-0", got)
	}
}

// WARP 渠道探测必须返回哨兵, 不得生成账号别名或落库。
func TestWarpProbeChannelRejected(t *testing.T) {
	template := "socks5h://{account}:pw@NovaVeil_WARP_socks5:1080"
	channel := model.Channel{ID: 9, Enabled: true, ChannelProxy: &template}
	if _, _, _, err := effectiveProbeChannel(channel); err != errWarpProbeUnsupported {
		t.Fatalf("effectiveProbeChannel err = %v", err)
	}
	if _, _, _, err := effectiveTestChannel(channel, "missing"); err != errWarpProbeUnsupported {
		t.Fatalf("effectiveTestChannel err = %v", err)
	}
}

// WARP 选 Key 时保留占位符, 槽位账号由转发路径解析。
func TestWarpEffectiveChannelKeepsPlaceholder(t *testing.T) {
	template := "socks5h://{account}:pw@NovaVeil_WARP_socks5:1080"
	channel := model.Channel{
		ID:           9,
		Keys:         []model.ChannelKey{{ID: "key-1", Key: "sk-1"}},
		ChannelProxy: &template,
	}
	effective, err := effectiveChannelForKey(channel, channel.Keys[0])
	if err != nil {
		t.Fatalf("effectiveChannelForKey: %v", err)
	}
	if effective.ChannelProxy == nil || *effective.ChannelProxy != template {
		t.Fatalf("WARP 代理模板应保留占位符, 实际 %v", effective.ChannelProxy)
	}
	if effective.Key != "sk-1" || effective.Keys != nil {
		t.Fatalf("生效 Key = %q, Keys = %v", effective.Key, effective.Keys)
	}
}
