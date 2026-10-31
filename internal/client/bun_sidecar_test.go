package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newFakeBunSidecarManager(endpoint string) *bunSidecarManager {
	return &bunSidecarManager{sidecar: &bunSidecar{endpoint: endpoint, done: make(chan struct{})}}
}

func TestBunSidecarRoundTripForwardsAndStreams(t *testing.T) {
	var captured bunForwardRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/forward" {
			t.Errorf("sidecar endpoint = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read sidecar payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(data, &captured); err != nil {
			t.Errorf("decode sidecar payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer server.Close()

	proxy, err := url.Parse("http://Default.demo-account:secret@resin:2260")
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}
	transport := &bunSidecarTransport{
		manager: newFakeBunSidecarManager(server.URL + "/forward"),
		proxy:   proxy,
		client:  server.Client(),
	}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(`{"model":"beam-free"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.19.5 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4")
	req.Header.Set("X-Opencode-Session", "ses_e8c23e83effeqhaDsqfGMpEXYq")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "data: ok\n\n" {
		t.Fatalf("stream body = %q", body)
	}
	if resp.StatusCode != http.StatusOK || resp.Request != req {
		t.Fatalf("response contract: status=%d request=%v", resp.StatusCode, resp.Request)
	}
	if captured.Method != http.MethodPost || captured.URL != req.URL.String() {
		t.Fatalf("forward target = %s %s", captured.Method, captured.URL)
	}
	if captured.Proxy != proxy.String() {
		t.Fatalf("forward proxy = %q", captured.Proxy)
	}
	if string(captured.Body) != `{"model":"beam-free"}` {
		t.Fatalf("forward body = %q", captured.Body)
	}
	headers := make(map[string]string)
	for _, item := range captured.Headers {
		headers[item[0]] = item[1]
	}
	for name, want := range map[string]string{
		"Authorization":       "Bearer public",
		"Content-Type":        "application/json",
		"User-Agent":          "opencode/1.19.5 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4",
		"x-opencode-session":  "ses_e8c23e83effeqhaDsqfGMpEXYq",
	} {
		if headers[name] != want {
			t.Fatalf("header %s = %q, want %q", name, headers[name], want)
		}
	}
}

func TestBunSidecarRoundTripPropagatesError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		w.Header().Set("X-Noveil-Sidecar-Error", "1")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream fetch failed"}`))
	}))
	defer server.Close()

	transport := &bunSidecarTransport{
		manager: newFakeBunSidecarManager(server.URL + "/forward"),
		client:  server.Client(),
	}
	req, err := http.NewRequest(http.MethodGet, "https://opencode.ai/inference/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "upstream fetch failed") {
		t.Fatalf("round trip error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream failure replayed: requests=%d", got)
	}
}

func TestBunSidecarRoundTripPreservesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"FreeUsageLimitError"}}`))
	}))
	defer server.Close()

	transport := &bunSidecarTransport{
		manager: newFakeBunSidecarManager(server.URL + "/forward"),
		client:  server.Client(),
	}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "120" {
		t.Fatalf("upstream response lost: status=%d retry=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if !strings.Contains(string(body), "FreeUsageLimitError") {
		t.Fatalf("upstream body = %q", body)
	}
}

// 二进制和空体保持线格式兼容。
func TestBunSidecarRoundTripBodyWireCompatibility(t *testing.T) {
	for name, body := range map[string][]byte{
		"absent": nil,
		"empty":  {},
		"binary": {0, 255, 128, '\n', '"', '\\'},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Errorf("decode wire payload: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if len(body) == 0 {
					if _, exists := wire["body"]; exists {
						t.Errorf("empty body must remain omitted: %s", wire["body"])
					}
				} else {
					var encoded string
					if err := json.Unmarshal(wire["body"], &encoded); err != nil {
						t.Errorf("body must remain a base64 string: %v", err)
					}
					if want := base64.StdEncoding.EncodeToString(body); encoded != want {
						t.Errorf("wire body=%q, want %q", encoded, want)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			transport := &bunSidecarTransport{
				manager: newFakeBunSidecarManager(server.URL + "/forward"),
				client:  server.Client(),
			}
			var requestBody io.Reader
			if body != nil {
				requestBody = io.NopCloser(bytes.NewReader(body))
			}
			req, err := http.NewRequest(http.MethodPost, "https://example.invalid/echo", requestBody)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

// 已取消请求不得读取或启动。
func TestBunSidecarRoundTripAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &bunSidecarManager{}
	transport := &bunSidecarTransport{manager: manager, client: &http.Client{}}
	body := bytes.NewBufferString("payload")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/", io.NopCloser(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error=%v", err)
	}
	if manager.sidecar != nil || manager.startErr != nil {
		t.Fatal("already canceled request attempted to start sidecar")
	}
	if body.String() != "payload" {
		t.Fatal("already canceled request consumed its body")
	}
}

// 启动失败仅在冷却期缓存。
func TestBunSidecarManagerStartupFailureCooldown(t *testing.T) {
	t.Setenv("NOVEIL_BUN_BIN", filepath.Join(t.TempDir(), "missing-bun"))
	t.Setenv("BUN_BIN", "")
	manager := &bunSidecarManager{}
	before := time.Now()
	if _, err := manager.current(); err == nil {
		t.Fatal("missing runtime must fail")
	}
	firstErr, nextStart := manager.startErr, manager.nextStart
	if nextStart.Before(before.Add(bunSidecarStartRetryDelay)) {
		t.Fatalf("startup cooldown=%v", nextStart.Sub(before))
	}
	t.Setenv("NOVEIL_BUN_BIN", "")
	manager.nextStart = time.Now().Add(time.Hour)
	nextStart = manager.nextStart
	if _, err := manager.current(); err != firstErr || manager.nextStart != nextStart {
		t.Fatalf("cooldown must preserve failure: %v", err)
	}
	manager.nextStart = time.Now().Add(-time.Second)
	if _, err := manager.current(); err == nil || err.Error() != "bun runtime not found" {
		t.Fatalf("expired cooldown must recheck runtime: %v", err)
	}
}

func TestNewBunSidecarClientRejectsSocksProxy(t *testing.T) {
	proxy, err := url.Parse("socks5h://Default.demo-account:secret@resin:2260")
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}
	if _, err := newBunSidecarClient(proxy); err == nil || !strings.Contains(err.Error(), "socks5h") {
		t.Fatalf("socks proxy error = %v", err)
	}
}

func bunSidecarTestBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping bun process test in short mode")
	}
	binary, _, err := bunSidecarCommand()
	if err != nil {
		t.Fatalf("bun command: %v", err)
	}
	if binary == "" {
		t.Skip("bun runtime not configured")
	}
	return binary
}

// 暂时失败和退出后可恢复启动。
func TestBunSidecarProcessStartupRecovers(t *testing.T) {
	t.Setenv("NOVEIL_BUN_BIN", bunSidecarTestBinary(t))
	tempDir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, filepath.Join(tempDir, "missing"))
	}
	manager := &bunSidecarManager{}
	if _, err := manager.current(); err == nil || !strings.Contains(err.Error(), "create bun sidecar script") {
		t.Fatalf("unavailable temporary directory must fail startup: %v", err)
	}
	firstErr := manager.startErr
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tempDir)
	}
	manager.nextStart = time.Now().Add(time.Hour)
	if _, err := manager.current(); err != firstErr {
		t.Fatalf("startup retried inside cooldown: %v", err)
	}
	manager.nextStart = time.Now().Add(-time.Second)
	sidecar, err := manager.current()
	if err != nil {
		t.Fatalf("startup did not recover: %v", err)
	}
	defer sidecar.Close()
	if !sidecar.alive() || manager.startErr != nil || !manager.nextStart.IsZero() {
		t.Fatal("successful startup did not clear failure state")
	}
	if err := sidecar.Close(); err != nil {
		t.Fatalf("stop first sidecar: %v", err)
	}
	restarted, err := manager.current()
	if err != nil {
		t.Fatalf("restart exited sidecar: %v", err)
	}
	defer restarted.Close()
	if restarted == sidecar || !restarted.alive() {
		t.Fatal("exited sidecar must be replaced")
	}
}

// 并发首请求共用一个进程端口。
func TestBunSidecarProcessConcurrentFirstRequests(t *testing.T) {
	t.Setenv("NOVEIL_BUN_BIN", bunSidecarTestBinary(t))
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	manager := &bunSidecarManager{}
	transport := &bunSidecarTransport{manager: manager, client: &http.Client{}}
	defer transport.CloseIdleConnections()
	defer func() {
		if manager.sidecar != nil {
			manager.sidecar.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const count = 12
	start := make(chan struct{})
	endpoints := make(map[string]int)
	var endpointsMu sync.Mutex
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			requestContext := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) {
					endpointsMu.Lock()
					endpoints[info.Conn.RemoteAddr().String()]++
					endpointsMu.Unlock()
				},
			})
			req, err := http.NewRequestWithContext(requestContext, http.MethodPost, upstream.URL, nil)
			if err != nil {
				t.Errorf("new request: %v", err)
				return
			}
			response, err := transport.RoundTrip(req)
			if err != nil {
				t.Errorf("first request: %v", err)
				return
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Errorf("status=%d", response.StatusCode)
			}
		}()
	}
	close(start)
	workers.Wait()
	if len(endpoints) != 1 {
		t.Fatalf("concurrent requests used sidecar ports: %v", endpoints)
	}
	for endpoint, connections := range endpoints {
		if connections != count {
			t.Fatalf("sidecar %s connections=%d, want %d", endpoint, connections, count)
		}
	}
	if requests.Load() != count {
		t.Fatalf("upstream requests=%d, want %d", requests.Load(), count)
	}
}

// 真实进程转发二进制和空体。
func TestBunSidecarProcessBodyBytes(t *testing.T) {
	sidecar, err := startBunSidecar(bunSidecarTestBinary(t))
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	defer sidecar.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	transport := &bunSidecarTransport{
		manager: &bunSidecarManager{sidecar: sidecar},
		client:  &http.Client{Timeout: 5 * time.Second},
	}
	defer transport.CloseIdleConnections()
	for name, body := range map[string][]byte{
		"absent": nil,
		"empty":  {},
		"binary": {0, 255, 128, '\n', '"', '\\'},
	} {
		t.Run(name, func(t *testing.T) {
			var requestBody io.Reader
			if body != nil {
				requestBody = io.NopCloser(bytes.NewReader(body))
			}
			req, err := http.NewRequest(http.MethodPost, upstream.URL, requestBody)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Equal(got, body) || response.StatusCode != http.StatusOK {
				t.Fatalf("echo status=%d body=%v want=%v error=%v", response.StatusCode, got, body, err)
			}
		})
	}
}

// 响应前后取消均须抵达上游。
func TestBunSidecarProcessCancellationReachesUpstream(t *testing.T) {
	sidecar, err := startBunSidecar(bunSidecarTestBinary(t))
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	defer sidecar.Close()
	for _, streaming := range []bool{false, true} {
		name := "before-response"
		if streaming {
			name = "during-response"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			canceled := make(chan struct{}, 1)
			release := make(chan struct{})
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Errorf("read upstream body: %v", err)
					return
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: first\n\n"))
					w.(http.Flusher).Flush()
				}
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
					select {
					case canceled <- struct{}{}:
					default:
					}
				case <-release:
				}
			}))
			defer upstream.Close()
			defer close(release)
			transport := &bunSidecarTransport{
				manager: &bunSidecarManager{sidecar: sidecar},
				client:  &http.Client{},
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				response *http.Response
				err      error
			}
			finished := make(chan result, 1)
			go func() {
				response, err := transport.RoundTrip(req)
				finished <- result{response: response, err: err}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not receive request")
			}
			if streaming {
				select {
				case result := <-finished:
					if result.err != nil {
						t.Fatalf("stream response: %v", result.err)
					}
					defer result.response.Body.Close()
					first := make([]byte, len("data: first\n\n"))
					if _, err := io.ReadFull(result.response.Body, first); err != nil {
						t.Fatalf("first stream frame: %v", err)
					}
					cancel()
					if _, err := io.ReadAll(result.response.Body); !errors.Is(err, context.Canceled) {
						t.Fatalf("stream cancellation error=%v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("first stream response did not arrive")
				}
			} else {
				cancel()
				select {
				case result := <-finished:
					if result.response != nil {
						result.response.Body.Close()
					}
					if !errors.Is(result.err, context.Canceled) {
						t.Fatalf("request cancellation error=%v", result.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("canceled RoundTrip did not return")
				}
			}
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("Bun did not cancel upstream request")
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("canceled request replayed: requests=%d", got)
			}
		})
	}
}

func TestBunSidecarProcessRoundTrip(t *testing.T) {
	binary, _, err := bunSidecarCommand()
	if err != nil {
		t.Fatalf("bun command: %v", err)
	}
	if binary == "" {
		t.Skip("bun runtime not configured")
	}
	if testing.Short() {
		t.Skip("skipping bun process test in short mode")
	}
	sidecar, err := startBunSidecar(binary)
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	defer sidecar.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream target = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("upstream Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: live\n\n"))
	}))
	defer upstream.Close()

	transport := &bunSidecarTransport{
		manager: &bunSidecarManager{sidecar: sidecar},
		client:  &http.Client{},
	}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/chat/completions", strings.NewReader(`{"model":"beam-free"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "data: live\n\n" {
		t.Fatalf("live sidecar response: status=%d body=%q", resp.StatusCode, body)
	}
}

// TestBunSidecarProcessStreamSurvivesIdleGap 回归: Bun.serve 默认 10 秒空闲
// 会在响应流中途掐连接, 思考模型静默超过该阈值后 Go 侧只能读到 unexpected EOF。
func TestBunSidecarProcessStreamSurvivesIdleGap(t *testing.T) {
	binary, _, err := bunSidecarCommand()
	if err != nil {
		t.Fatalf("bun command: %v", err)
	}
	if binary == "" {
		t.Skip("bun runtime not configured")
	}
	if testing.Short() {
		t.Skip("skipping bun process test in short mode")
	}
	sidecar, err := startBunSidecar(binary)
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	defer sidecar.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(11 * time.Second)
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer upstream.Close()

	transport := &bunSidecarTransport{
		manager: &bunSidecarManager{sidecar: sidecar},
		client:  &http.Client{},
	}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/chat/completions", strings.NewReader(`{"model":"beam-free"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data: second") {
		t.Fatalf("空档后的增量应照常到达, 实际 response=%q err=%v", body, readErr)
	}
	if readErr != nil {
		t.Fatalf("流不得在空闲空档处被掐断: %v", readErr)
	}
}
