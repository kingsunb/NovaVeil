package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// STA-10: 跨源重定向不得带走非标准认证头。下列测试以两个隔离 mock host 验证
// upstreamCheckRedirect 策略: A 返回跨源 Location 指向 B, B 记录到达的请求,
// 用假 Key/请求体验证 B 不收凭据与请求体。覆盖同源、不同端口、不同主机名、
// HTTPS→HTTP 降级、跳转次数上限, 以及模型同步(GET+X-Api-Key)与流式透传
// (POST+Authorization+body, 307) 两种出站形态。

// recordedRequest 保存 mock 上游收到的单次请求, 用于断言凭据/请求体是否被跨源重定向带走。
type recordedRequest struct {
	method  string
	path    string
	headers http.Header
	body    string
}

// recordingHandler 记录全部到达的请求; 跨源重定向被正确拒绝时, 目标 host 的记录器应保持空。
type recordingHandler struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	h.mu.Lock()
	h.requests = append(h.requests, recordedRequest{
		method:  r.Method,
		path:    r.URL.Path,
		headers: r.Header.Clone(),
		body:    string(body),
	})
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *recordingHandler) snapshot() []recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedRequest, len(h.requests))
	copy(out, h.requests)
	return out
}

// newProtectedClient 构造一个使用 upstreamCheckRedirect 的 http.Client, 复用默认 Transport 的克隆,
// 与 newHTTPClientNoProxy/newHTTPClientCustomProxy 产出的客户端在重定向策略上完全一致。
func newProtectedClient(t *testing.T) *http.Client {
	t.Helper()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("default transport is not *http.Transport")
	}
	return &http.Client{Transport: tr.Clone(), CheckRedirect: upstreamCheckRedirect}
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// closeBody 安全关闭 client.Do 在重定向错误下可能返回的非 nil 响应(Body 已被 net/http 关闭)。
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// withHostname 把 URL 的主机名替换为 newHostname(保留端口), 用于构造跨主机名的重定向目标。
func withHostname(rawURL, newHostname string) string {
	u := mustParseURL(rawURL)
	u.Host = newHostname + ":" + u.Port()
	return u.String()
}

// TestFactoryClientsHaveCheckRedirect 确保两个真实出站客户端工厂都装上重定向安全策略。
func TestFactoryClientsHaveCheckRedirect(t *testing.T) {
	c, err := newHTTPClientNoProxy()
	if err != nil {
		t.Fatalf("newHTTPClientNoProxy: %v", err)
	}
	if c.CheckRedirect == nil {
		t.Fatal("newHTTPClientNoProxy client must carry upstreamCheckRedirect")
	}
	// 端口 9(discard)仅用于构造客户端, 不会发起拨号。
	cp, err := newHTTPClientCustomProxy("http://127.0.0.1:9")
	if err != nil {
		t.Fatalf("newHTTPClientCustomProxy: %v", err)
	}
	if cp.CheckRedirect == nil {
		t.Fatal("newHTTPClientCustomProxy client must carry upstreamCheckRedirect")
	}
}

// TestUpstreamCheckRedirect_SameOriginFollowed 同源(同主机名同端口)重定向被允许跟随,
// 且自定义认证头 X-Api-Key 在跟随后仍送达目标, 不被策略误删。
func TestUpstreamCheckRedirect_SameOriginFollowed(t *testing.T) {
	var dest recordingHandler
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dest", http.StatusFound)
	})
	mux.Handle("/dest", &dest)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := newProtectedClient(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/redirect", nil)
	req.Header.Set("X-Api-Key", "fake-secret-key")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("same-origin redirect must be followed; got error: %v", err)
	}
	defer resp.Body.Close()
	if dest.count() != 1 {
		t.Fatalf("dest must be reached exactly once; got %d", dest.count())
	}
	if got := dest.snapshot()[0].headers.Get("X-Api-Key"); got != "fake-secret-key" {
		t.Fatalf("same-origin redirect must preserve X-Api-Key; got %q", got)
	}
}

// TestUpstreamCheckRedirect_CrossOriginDifferentHostRejected 不同主机名的重定向被拒绝,
// 目标 B 不收任何请求, 凭据不会泄露。
func TestUpstreamCheckRedirect_CrossOriginDifferentHostRejected(t *testing.T) {
	var b recordingHandler
	serverB := httptest.NewServer(&b)
	defer serverB.Close()

	// 把 B 的 URL 主机名改写成 localhost, 与 A 的 127.0.0.1 构成不同主机名(跨源)。
	target := withHostname(serverB.URL, "localhost")
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/models", http.StatusFound)
	}))
	defer serverA.Close()

	client := newProtectedClient(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, serverA.URL+"/models", nil)
	req.Header.Set("X-Api-Key", "fake-secret-key")
	resp, err := client.Do(req)
	closeBody(t, resp)
	if err == nil {
		t.Fatal("cross-origin (different host) redirect must be rejected")
	}
	if b.count() != 0 {
		t.Fatalf("B must not be contacted on cross-origin redirect; got %d request(s)", b.count())
	}
}

// TestUpstreamCheckRedirect_DifferentPortRejected 同主机名不同端口被视为跨源并拒绝,
// 目标 B 不收任何请求。
func TestUpstreamCheckRedirect_DifferentPortRejected(t *testing.T) {
	var b recordingHandler
	serverB := httptest.NewServer(&b)
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL+"/dest", http.StatusFound)
	}))
	defer serverA.Close()

	ua := mustParseURL(serverA.URL)
	ub := mustParseURL(serverB.URL)
	if ua.Hostname() != ub.Hostname() || ua.Port() == ub.Port() {
		t.Fatalf("test setup: A and B must share hostname but differ by port; A=%s B=%s", ua.Host, ub.Host)
	}

	client := newProtectedClient(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, serverA.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer fake-secret")
	resp, err := client.Do(req)
	closeBody(t, resp)
	if err == nil {
		t.Fatal("redirect to a different port must be rejected")
	}
	if b.count() != 0 {
		t.Fatalf("B on different port must not be contacted; got %d request(s)", b.count())
	}
}

// TestUpstreamCheckRedirect_HTTPSDowngradeRejected HTTPS→HTTP 降级被拒绝(即便同源)。
func TestUpstreamCheckRedirect_HTTPSDowngradeRejected(t *testing.T) {
	prev := &http.Request{URL: mustParseURL("https://api.example.com/v1/models")}
	next := &http.Request{URL: mustParseURL("http://api.example.com/v1/models")}
	err := upstreamCheckRedirect(next, []*http.Request{prev})
	if err == nil {
		t.Fatal("https→http downgrade must be rejected")
	}
	if !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("error should mention insecure downgrade; got %v", err)
	}
}

// TestUpstreamCheckRedirect_HTTPSSameHostAllowed HTTPS→HTTPS 同源重定向被允许。
func TestUpstreamCheckRedirect_HTTPSSameHostAllowed(t *testing.T) {
	prev := &http.Request{URL: mustParseURL("https://api.example.com/v1/models")}
	next := &http.Request{URL: mustParseURL("https://api.example.com/v1/models?page=2")}
	if err := upstreamCheckRedirect(next, []*http.Request{prev}); err != nil {
		t.Fatalf("https→https same host must be allowed; got %v", err)
	}
}

// TestUpstreamCheckRedirect_LimitExceeded 同源重定向环在达到次数上限后被中止。
func TestUpstreamCheckRedirect_LimitExceeded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := newProtectedClient(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/loop", nil)
	resp, err := client.Do(req)
	closeBody(t, resp)
	if err == nil {
		t.Fatal("redirect loop must hit the redirect limit and return an error")
	}
	if !strings.Contains(err.Error(), "redirect limit") {
		t.Fatalf("error should mention redirect limit; got %v", err)
	}
}

// TestUpstreamCheckRedirect_ModelSyncStyleNoLeak 模拟 helper/fetch.go 的模型同步请求形态:
// GET /models 携带 X-Api-Key 与 Anthropic-Version, 上游 A 以 302 跨源指向 B。
// 策略拒绝跳转, B 不收任何请求, X-Api-Key 不会泄露到 B。
func TestUpstreamCheckRedirect_ModelSyncStyleNoLeak(t *testing.T) {
	var b recordingHandler
	serverB := httptest.NewServer(&b)
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL+"/models", http.StatusFound)
	}))
	defer serverA.Close()

	client := newProtectedClient(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, serverA.URL+"/models", nil)
	req.Header.Set("X-Api-Key", "fake-anthropic-key")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := client.Do(req)
	closeBody(t, resp)
	if err == nil {
		t.Fatal("model-sync cross-origin redirect must be rejected")
	}
	if b.count() != 0 {
		t.Fatalf("B must not receive the model-sync request; got %d", b.count())
	}
}

// TestUpstreamCheckRedirect_PassthroughStyleNoLeak 模拟 relay/upstream.go 的流式透传请求形态:
// POST 携带 Authorization 与请求体, 上游 A 以 307 跨源指向 B(307 保留方法与请求体)。
// 策略拒绝跳转, B 不收任何请求, Authorization 与请求体都不会泄露到 B。
func TestUpstreamCheckRedirect_PassthroughStyleNoLeak(t *testing.T) {
	var b recordingHandler
	serverB := httptest.NewServer(&b)
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer serverA.Close()

	client := newProtectedClient(t)
	body := strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, serverA.URL+"/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer fake-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	closeBody(t, resp)
	if err == nil {
		t.Fatal("passthrough cross-origin redirect must be rejected")
	}
	if b.count() != 0 {
		t.Fatalf("B must not receive the passthrough request body/auth; got %d", b.count())
	}
}

// TestMaskProxySecret 覆盖日志展示用的代理打码: 密码段必须隐去, 且掩码本身不得被
// net/url 转义成 %2A(否则日志里读不出"这是打码"反而像一段乱码); 用户名(含 {account}
// 解析出的别名)与协议/主机/端口须原样保留, 便于按出口与实际配置逐字比对。
func TestMaskProxySecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		leak string // 输出中不得出现的原始凭据子串
	}{
		{"socks5h 带密码", "socks5h://temp:1@resin:2260", "socks5h://temp:****@resin:2260", ":1@"},
		{"http 带密码", "http://user:secret@10.0.0.8:8080", "http://user:****@10.0.0.8:8080", "secret"},
		{"无 userinfo 原样", "socks5h://resin:2260", "socks5h://resin:2260", ""},
		{"有用户名无密码", "socks5h://u.alias@host:1080", "socks5h://u.alias@host:1080", ""},
		{"用户名含需转义字符", "socks5h://a%40b:pw@host:2260", "socks5h://a%40b:****@host:2260", ":pw@"},
		{"首尾空白", "  socks5://u:p@host  ", "socks5://u:****@host", ":p@"},
		{"非法地址不回显原文", "socks5h://%zz@host:2260", "(invalid proxy url)", "%zz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskProxySecret(tt.in)
			if got != tt.want {
				t.Fatalf("MaskProxySecret(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "%2A") {
				t.Fatalf("掩码被转义成 %%2A, 日志无法直读: %q", got)
			}
			if tt.leak != "" && strings.Contains(got, tt.leak) {
				t.Fatalf("输出泄漏原始凭据 %q: %q", tt.leak, got)
			}
		})
	}
}
