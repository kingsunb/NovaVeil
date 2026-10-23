package helper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/builtin"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// opencodeSessionPattern 校验 opencode 会话 ID 格式:
// ses_ + 12 个小写十六进制字符 + 14 个 [0-9A-Za-z] 字符。
var opencodeSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// newModelsTestServer 启动一个 httptest 上游, 对 /v1/models 用给定状态码与响应体应答。
// 用于在受控环境下回归 fetchOpenAIModels 的状态码校验, 不依赖真实上游与数据库初始化。
func newModelsTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// TestFetchOpenAIModelsRejectsNon2xx 是 H-01 的核心回归: 上游返回 401/429 时, 错误响应体
// (如 {"error":{...}}) 解码到 OpenAIModelList 会得到 Data: nil, 旧实现据此返回空切片且
// nil error, SyncModelsTask 把它当成成功空同步并删除渠道全部 auto 模型, 级联清理分组与
// 评估排序。修复后必须返回非 nil error, 且错误信息携带状态码与响应体片段用于排查。
func TestFetchOpenAIModelsRejectsNon2xx(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantStatus  string // 错误信息应包含的状态码片段, 如 "HTTP 401"。
		wantSnippet string // 错误信息应包含的响应体片段。
	}{
		{
			name:        "401 invalid api key",
			status:      http.StatusUnauthorized,
			body:        `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`,
			wantStatus:  "HTTP 401",
			wantSnippet: "invalid api key",
		},
		{
			name:        "429 rate limited",
			status:      http.StatusTooManyRequests,
			body:        `{"error":{"message":"rate limit exceeded","type":"rate_limit_exceeded"}}`,
			wantStatus:  "HTTP 429",
			wantSnippet: "rate limit exceeded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newModelsTestServer(t, tc.status, tc.body)
			t.Cleanup(srv.Close)

			models, err := fetchOpenAIModels(&http.Client{}, context.Background(), model.Channel{
				Type:    model.ChannelProviderOpenAI,
				BaseURL: srv.URL,
				Key:     "sk-test",
			})
			if err == nil {
				t.Fatalf("expected non-nil error for upstream HTTP %d, got nil (models=%v)", tc.status, models)
			}
			if models != nil {
				t.Fatalf("expected nil model slice on error, got %v", models)
			}
			if !strings.Contains(err.Error(), tc.wantStatus) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.wantStatus)
			}
			if !strings.Contains(err.Error(), tc.wantSnippet) {
				t.Fatalf("error %q should contain body snippet %q", err.Error(), tc.wantSnippet)
			}
		})
	}
}

// TestFetchOpenAIModelsAcceptsLegitimateEmpty 验证状态码守卫不破坏合法空列表:
// 上游 200 且 {"data":[]} 时仍应返回非 nil 空切片与 nil error —— 这是真实的「上游没有模型」,
// 与 401/429 的错误空列表现在已通过状态码区分开。
func TestFetchOpenAIModelsAcceptsLegitimateEmpty(t *testing.T) {
	srv := newModelsTestServer(t, http.StatusOK, `{"data":[]}`)
	t.Cleanup(srv.Close)

	models, err := fetchOpenAIModels(&http.Client{}, context.Background(), model.Channel{
		Type:    model.ChannelProviderOpenAI,
		BaseURL: srv.URL,
		Key:     "sk-test",
	})
	if err != nil {
		t.Fatalf("expected nil error for legitimate empty list, got: %v", err)
	}
	if models == nil {
		t.Fatal("expected non-nil empty slice for legitimate empty list, got nil")
	}
	if len(models) != 0 {
		t.Fatalf("expected empty slice, got %v", models)
	}
}

// TestFetchOpenAIModelsAcceptsPopulated 验证状态码守卫后的快乐路径仍能正常解码模型列表。
func TestFetchOpenAIModelsAcceptsPopulated(t *testing.T) {
	srv := newModelsTestServer(t, http.StatusOK, `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
	t.Cleanup(srv.Close)

	models, err := fetchOpenAIModels(&http.Client{}, context.Background(), model.Channel{
		Type:    model.ChannelProviderOpenAI,
		BaseURL: srv.URL,
		Key:     "sk-test",
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got, want := len(models), 2; got != want {
		t.Fatalf("expected %d models, got %d (%v)", want, got, models)
	}
}

// 验证内置 Groq 同步请求路径。
func TestBuiltinGroqModelSyncPath(t *testing.T) {
	var groq model.Channel
	for _, def := range builtin.BuiltinFreeChannels {
		if def.Name == "G4F Space Groq Free" {
			groq = def
			break
		}
	}
	if groq.BaseURL == "" {
		t.Fatal("G4F Space Groq Free not found")
	}
	parsed, err := url.Parse(groq.BaseURL)
	if err != nil {
		t.Fatalf("parse builtin base url: %v", err)
	}

	gotPath := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"llama-3.3-70b-versatile"}]}`))
	}))
	t.Cleanup(srv.Close)
	groq.BaseURL = srv.URL + parsed.Path

	models, err := fetchOpenAIModels(&http.Client{}, context.Background(), groq)
	if err != nil {
		t.Fatalf("fetchOpenAIModels: %v", err)
	}
	if got := <-gotPath; got != "/api/groq/openai/v1/models" {
		t.Fatalf("request path = %q, want /api/groq/openai/v1/models", got)
	}
	if len(models) != 1 || models[0] != "llama-3.3-70b-versatile" {
		t.Fatalf("models = %v", models)
	}
}

// TestFetchOpenAIModelsInjectsOpencodeSession 验证模型同步/探测路径与转发路径一致:
// OpencodeCompat=true 的 OpenAI 渠道拉取模型列表时, 必须注入 opencode 格式的
// x-opencode-session 及 x-opencode-session-id 头(两者同值), 对齐 opencode v2 官方客户端
// model-request.ts:283 的头集合, 否则 opencode.ai 上游会拒绝请求。
func TestFetchOpenAIModelsInjectsOpencodeSession(t *testing.T) {
	type headers struct {
		session   string
		sessionID string
	}
	got := make(chan headers, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- headers{
			session:   r.Header.Get("x-opencode-session"),
			sessionID: r.Header.Get("x-opencode-session-id"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchOpenAIModels(&http.Client{}, context.Background(), model.Channel{
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        srv.URL,
		Key:            "sk-test",
		OpencodeCompat: true,
	}); err != nil {
		t.Fatalf("fetchOpenAIModels: %v", err)
	}

	h := <-got
	if !opencodeSessionPattern.MatchString(h.session) {
		t.Fatalf("x-opencode-session = %q, 期望匹配 %s", h.session, opencodeSessionPattern.String())
	}
	if h.sessionID != h.session {
		t.Fatalf("x-opencode-session-id 应与 x-opencode-session 同值, session=%q session-id=%q", h.session, h.sessionID)
	}
}

// TestFetchOpenAIModelsSkipsOpencodeSessionWhenDisabled 验证 OpencodeCompat=false
// 的普通渠道不会被注入 x-opencode-session / x-opencode-session-id, 保持与转发路径相同的开关语义。
func TestFetchOpenAIModelsSkipsOpencodeSessionWhenDisabled(t *testing.T) {
	type headers struct {
		session   string
		sessionID string
	}
	got := make(chan headers, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- headers{
			session:   r.Header.Get("x-opencode-session"),
			sessionID: r.Header.Get("x-opencode-session-id"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchOpenAIModels(&http.Client{}, context.Background(), model.Channel{
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        srv.URL,
		Key:            "sk-test",
		OpencodeCompat: false,
	}); err != nil {
		t.Fatalf("fetchOpenAIModels: %v", err)
	}

	h := <-got
	if h.session != "" {
		t.Fatalf("OpencodeCompat=false 时不应注入 x-opencode-session, 实际注入 %q", h.session)
	}
	if h.sessionID != "" {
		t.Fatalf("OpencodeCompat=false 时不应注入 x-opencode-session-id, 实际注入 %q", h.sessionID)
	}
}

// TestFetchOpenAIModelsOverridesOpencodeLaneUserAgent 验证免费档同步请求动态覆盖 UA:
// 存量渠道静态 custom_header 可能停留在旧版本号(如 2.0.32), 不得外泄到上游;
// 非 zen 档与未开兼容开关的渠道保持原 UA 不动。
func TestFetchOpenAIModelsOverridesOpencodeLaneUserAgent(t *testing.T) {
	cases := []struct {
		name     string
		suffix   string
		compat   bool
		customUA string
		want     string
	}{
		{"v2 档覆盖陈旧 UA", "/inference", true, "opencode/latest/2.0.32/cli", model.OpencodeV2UserAgent()},
		{"v1 档补官方 UA", "/zen", true, "", model.OpencodeV1UserAgent(model.UpstreamProtocolChat)},
		{"非 zen 档保留静态 UA", "/v1", true, "opencode/latest/2.0.32/cli", "opencode/latest/2.0.32/cli"},
		{"未开兼容保留静态 UA", "/inference", false, "opencode/latest/2.0.32/cli", "opencode/latest/2.0.32/cli"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.Header.Get("User-Agent")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			t.Cleanup(srv.Close)

			channel := model.Channel{
				Type:           model.ChannelProviderOpenAI,
				BaseURL:        srv.URL + tc.suffix,
				Key:            "sk-test",
				OpencodeCompat: tc.compat,
			}
			if tc.customUA != "" {
				channel.CustomHeader = []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: tc.customUA}}
			}
			if _, err := fetchOpenAIModels(&http.Client{}, context.Background(), channel); err != nil {
				t.Fatalf("fetchOpenAIModels: %v", err)
			}
			if ua := <-got; ua != tc.want {
				t.Fatalf("User-Agent = %q, 期望 %q", ua, tc.want)
			}
		})
	}
}
