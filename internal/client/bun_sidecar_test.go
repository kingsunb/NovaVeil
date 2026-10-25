package client

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	req.Header.Set("User-Agent", "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4")
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
	decoded, err := base64.StdEncoding.DecodeString(captured.Body)
	if err != nil || string(decoded) != `{"model":"beam-free"}` {
		t.Fatalf("forward body = %q err=%v", captured.Body, err)
	}
	headers := make(map[string]string)
	for _, item := range captured.Headers {
		headers[item[0]] = item[1]
	}
	for name, want := range map[string]string{
		"Authorization":       "Bearer public",
		"Content-Type":        "application/json",
		"User-Agent":          "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4",
		"x-opencode-session":  "ses_e8c23e83effeqhaDsqfGMpEXYq",
	} {
		if headers[name] != want {
			t.Fatalf("header %s = %q, want %q", name, headers[name], want)
		}
	}
}

func TestBunSidecarRoundTripPropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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

func TestNewBunSidecarClientRejectsSocksProxy(t *testing.T) {
	proxy, err := url.Parse("socks5h://Default.demo-account:secret@resin:2260")
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}
	if _, err := newBunSidecarClient(proxy); err == nil || !strings.Contains(err.Error(), "socks5h") {
		t.Fatalf("socks proxy error = %v", err)
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
