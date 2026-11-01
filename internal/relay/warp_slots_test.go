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
		pending:   map[int]warpSlotPending{},
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

// 单次 429 只在最后一个在途请求结束后轮换该出口。
func TestWarpSlotRateLimitedRotatesAfterRelease(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query().Get("egress") + "|" + r.URL.Query().Get("reason")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := warpTestPool()
	pool.controlBase = server.URL
	pool.reserved[0] = 1
	lease := &warpSlotLease{pool: pool, slot: 0, egress: "a", generation: 1}
	lease.MarkRateLimited()
	select {
	case <-requests:
		t.Fatal("在途请求结束前不应轮换")
	case <-time.After(50 * time.Millisecond):
	}
	lease.Release()
	select {
	case got := <-requests:
		if got != "a|429" {
			t.Fatalf("轮换参数 = %q, 期望 a|429", got)
		}
	case <-time.After(time.Second):
		t.Fatal("最后一个请求结束后应轮换")
	}
}

// 最后一个请求成功时保留出口, 不触发轮换。
func TestWarpSlotRateLimitedKeepsExitAfterSuccess(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query().Get("egress")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := warpTestPool()
	pool.controlBase = server.URL
	pool.reserved[0] = 1
	lease := &warpSlotLease{pool: pool, slot: 0, egress: "a", generation: 1}
	lease.MarkRateLimited()
	lease.MarkSuccess()
	lease.Release()
	select {
	case <-requests:
		t.Fatal("最后一次成功时不应轮换")
	case <-time.After(200 * time.Millisecond):
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
		pending:  map[int]warpSlotPending{},
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

// 槽位代次变化说明出口已换, 应解除旧出口的 429 暂停。
func TestWarpSlotPoolRefreshClearsSuspectOnGeneration(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generation := 1
		if calls.Add(1) > 1 {
			generation = 2
		}
		_ = json.NewEncoder(w).Encode(warpSlotsSnapshot{
			Size: 1,
			Slots: []warpSlotInfo{
				{Slot: 0, Account: "warp-0", Egress: "a", Prefix: "p/64", State: "active", Generation: generation},
			},
		})
	}))
	defer server.Close()

	pool := &warpSlotPool{
		slotsURL: server.URL,
		client:   server.Client(),
		reserved: map[int]int{},
		suspect:  map[int]time.Time{},
		pending:  map[int]warpSlotPending{},
	}
	if err := pool.refresh(t.Context()); err != nil {
		t.Fatalf("首次刷新: %v", err)
	}
	pool.mu.Lock()
	pool.suspect[0] = time.Now().Add(time.Minute)
	pool.fetchedAt = time.Now().Add(-2 * warpSlotsCacheTTL)
	pool.mu.Unlock()
	if err := pool.refresh(t.Context()); err != nil {
		t.Fatalf("代次刷新: %v", err)
	}
	pool.mu.Lock()
	_, suspect := pool.suspect[0]
	pool.mu.Unlock()
	if suspect {
		t.Fatal("槽位代次变化后不应保留旧出口的暂停标记")
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
