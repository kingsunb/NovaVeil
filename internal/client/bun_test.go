package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBunRequestHeadOfficialClientWire 用 2026-11-09 官方 2.2.6 客户端实抓的完整头序
// 固定 bunRequestHead 输出: 公共块 → 小写排序的应用头 → 传输层追加段。
// Content-Length 属传输层追加, 必须位于末尾(实抓翻转了写进公共块的旧决定)。
func TestBunRequestHeadOfficialClientWire(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:9977/inference/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/latest/2.2.6/cli")
	req.Header.Set("b3", "trace-span-1-parent")
	req.Header.Set("traceparent", "00-trace-span-01")
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", "project")
	req.Header.Set("x-opencode-session", "session")
	req.Header.Set("x-opencode-session-id", "session")
	req.Header.Set("x-session-affinity", "session")
	req.Header.Set("X-Session-Id", "session")
	req.Header.Set("Accept", "*/*")

	head, decompress, err := bunRequestHead(req, []byte("{}"))
	if err != nil {
		t.Fatalf("bunRequestHead: %v", err)
	}
	if !decompress {
		t.Fatalf("未显式设置 Accept-Encoding 时应由传输层解码")
	}
	want := strings.Join([]string{
		"POST /inference/openai/v1/chat/completions HTTP/1.1",
		"Authorization: Bearer public",
		"Content-Type: application/json",
		"User-Agent: opencode/latest/2.2.6/cli",
		"b3: trace-span-1-parent",
		"traceparent: 00-trace-span-01",
		"x-opencode-client: cli",
		"x-opencode-project: project",
		"x-opencode-session: session",
		"x-opencode-session-id: session",
		"x-session-affinity: session",
		"x-session-id: session",
		"Connection: keep-alive",
		"Accept: */*",
		"Host: 127.0.0.1:9977",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: 2",
		"",
		"",
	}, "\r\n")
	if string(head) != want {
		t.Fatalf("头序与官方实抓不一致:\n--- got ---\n%s\n--- want ---\n%s", head, want)
	}
}

// TestBunRequestHeadDefaults 验证无 UA 的无体 GET: 走 Bun 默认 UA, 且不写 Content-Length。
func TestBunRequestHeadDefaults(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com:9977/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	head, decompress, err := bunRequestHead(req, nil)
	if err != nil {
		t.Fatalf("bunRequestHead: %v", err)
	}
	if !decompress {
		t.Fatalf("未显式设置 Accept-Encoding 时应由传输层解码")
	}
	want := strings.Join([]string{
		"GET /v1/models HTTP/1.1",
		"Connection: keep-alive",
		"User-Agent: Bun/" + bunRuntimeVersion,
		"Accept: */*",
		"Host: example.com:9977",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"",
		"",
	}, "\r\n")
	if string(head) != want {
		t.Fatalf("默认头序不符:\n--- got ---\n%s\n--- want ---\n%s", head, want)
	}
}

func newDroppedReuseServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newDroppedReuseClient(t *testing.T) *http.Client {
	t.Helper()
	transport, err := NewBunTransport(BunOptions{ConnectTimeout: time.Second, ResponseHeaderTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewBunTransport: %v", err)
	}
	return &http.Client{Transport: transport}
}

func warmBunConnection(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("预热连接: %v", err)
	}
	_ = resp.Body.Close()
}

func TestBunTransportReplaysNonIdempotentOnce(t *testing.T) {
	var calls atomic.Int32
	srv := newDroppedReuseServer(t, &calls)
	client := newDroppedReuseClient(t)
	warmBunConnection(t, client, srv.URL+"/v1/models")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST 应重放一次后成功: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("POST 请求次数 = %d, 期望 3 (预热 + 断开 + 重放)", got)
	}
}

func TestBunTransportReplaysIdempotentOnce(t *testing.T) {
	var calls atomic.Int32
	srv := newDroppedReuseServer(t, &calls)
	client := newDroppedReuseClient(t)
	warmBunConnection(t, client, srv.URL+"/v1/models")

	resp, err := client.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET 应重放一次后成功: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("GET 请求次数 = %d, 期望 3 (首次 + 断开 + 重放)", got)
	}
}

func TestBunClientCacheEvictsAtLimit(t *testing.T) {
	bunClients.Range(func(key, _ any) bool {
		bunClients.Delete(key)
		return true
	})
	t.Cleanup(func() {
		bunClients.Range(func(key, _ any) bool {
			bunClients.Delete(key)
			return true
		})
	})

	for i := 0; i < maxCustomProxyClients; i++ {
		bunClients.Store(fmt.Sprintf("proxy-%d", i), &http.Client{})
	}
	got := cacheBunClient("fresh", &http.Client{})
	if got == nil {
		t.Fatal("fresh client was not cached")
	}
	if size := bunClientsApproxSize(); size != 1 {
		t.Fatalf("缓存条目 = %d, 期望软上限后清空并只保留新条目", size)
	}
}
