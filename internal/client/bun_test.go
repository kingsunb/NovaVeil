package client

import (
	"net/http"
	"strings"
	"testing"
)

// TestBunRequestHeadOfficialClientWire 用 2026-10-29 官方 2.2.1 客户端实抓的完整头序
// 固定 bunRequestHead 输出: 公共块 → 小写排序的应用头 → 传输层追加段。
// Content-Length 属传输层追加, 必须位于末尾(实抓翻转了写进公共块的旧决定)。
func TestBunRequestHeadOfficialClientWire(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:9977/inference/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/latest/2.2.1/cli")
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
		"User-Agent: opencode/latest/2.2.1/cli",
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
