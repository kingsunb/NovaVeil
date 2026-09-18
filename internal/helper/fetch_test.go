package helper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

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
