package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/tidwall/gjson"
)

// TestChannelProbesNativeRequests 验证单模型与逐密钥测试实际发出的协议报文,
// 包括模型协议覆盖与完全透传的优先级, 同时断言原生响应正文可读。
func TestChannelProbesNativeRequests(t *testing.T) {
	tests := []struct {
		name        string
		provider    model.ChannelProvider
		protocol    string
		opencode    bool
		passthrough bool
		path        string
		format      string
		messagePath string
		tokenPath   string
		response    string
	}{
		{"chat", model.ChannelProviderOpenAI, "", false, false, "/v1/chat/completions", "openai_chat", "messages.0.content", "max_tokens", testPathOpenAIChatResponse},
		{"responses", model.ChannelProviderOpenAIResponses, "", false, false, "/v1/responses", "openai_responses", "input", "max_output_tokens", testPathOpenAIResponsesResponse},
		{"anthropic", model.ChannelProviderAnthropic, "", false, false, "/v1/messages", "anthropic", "messages.0.content", "max_tokens", testPathAnthropicResponse},
		{"opencode responses", model.ChannelProviderOpenAI, "responses", true, false, "/v1/responses", "openai_responses", "input", "max_output_tokens", testPathOpenAIResponsesResponse},
		{"opencode anthropic", model.ChannelProviderOpenAI, "anthropic", true, false, "/v1/messages", "anthropic", "messages.0.content", "max_tokens", testPathAnthropicResponse},
		{"ordinary ignores model protocol", model.ChannelProviderOpenAI, "responses", false, false, "/v1/chat/completions", "openai_chat", "messages.0.content", "max_tokens", testPathOpenAIChatResponse},
		{"complete passthrough keeps channel protocol", model.ChannelProviderAnthropic, "responses", true, true, "/v1/messages", "anthropic", "messages.0.content", "max_tokens", testPathAnthropicResponse},
	}
	for _, tt := range tests {
		for _, perKey := range []bool{false, true} {
			name := tt.name + "/model"
			if perKey {
				name = tt.name + "/key"
			}
			t.Run(name, func(t *testing.T) {
				beforeID := idSeq.Load()
				defer cleanupProbeRequests(beforeID)
				type capturedRequest struct {
					path string
					body []byte
				}
				captured := make(chan capturedRequest, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if tt.format == "anthropic" && (r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("X-Api-Key") != "test-key") {
						t.Errorf("missing Anthropic version/auth headers: %v", r.Header)
					}
					captured <- capturedRequest{r.URL.Path, body}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tt.response))
				}))
				defer upstream.Close()
				channel := model.Channel{
					ID: 9871, Name: name, Type: tt.provider, Enabled: true,
					BaseURL: upstream.URL, Key: "test-key", OpencodeCompat: tt.opencode,
					PassThroughBodyEnabled: tt.passthrough,
					Models:                 []model.ChannelModel{{Name: "m", UpstreamProtocol: tt.protocol}},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				content := ""
				if perKey {
					result := ChannelKeyTestResult{Label: "#1(test)"}
					if err := sendKeyTestRequest(ctx, channel, "m", "hello", &result); err != nil {
						t.Fatal(err)
					}
					content = result.Content
				} else {
					result, err := sendChannelTestRequest(ctx, channel, "m", "hello", "#1(test)", testPanelMaxTokens)
					if err != nil {
						t.Fatal(err)
					}
					content = result.Content
				}
				if content != "ok" {
					t.Fatalf("content = %q", content)
				}
				var sent capturedRequest
				select {
				case sent = <-captured:
				default:
					t.Fatal("no upstream request")
				}
				if sent.path != tt.path || gjson.GetBytes(sent.body, tt.messagePath).String() != "hello" {
					t.Fatalf("unexpected request: %s %s", sent.path, sent.body)
				}
				if gjson.GetBytes(sent.body, "model").String() != "m" || gjson.GetBytes(sent.body, "stream").Type != gjson.False || gjson.GetBytes(sent.body, tt.tokenPath).Int() != testPanelMaxTokens {
					t.Fatalf("missing model/non-stream/output limit: %s", sent.body)
				}
				snapshot, stream := OpenRequestStream()
				CloseRequestStream(stream)
				var logged []RequestState
				for _, state := range snapshot {
					if state.ID > beforeID {
						logged = append(logged, state)
					}
				}
				if len(logged) != 1 || logged[0].Status != StatusSuccess || logged[0].ClientFormat != tt.format || logged[0].UpstreamType != tt.format || logged[0].RelayMode != "passthrough" || logged[0].KeyLabel != "#1(test)" {
					t.Fatalf("unexpected log: %+v", logged)
				}
			})
		}
	}
}

// TestOpencodeCompatProbesInjectValidSession 验证面板测试/逐密钥诊断路径(无客户端会话)
// 只对 OpencodeCompat 渠道写合法 ses_ 格式的 x-opencode-session, 其他渠道保持不注入;
// 否则 OpenCode 上游会以 MissingSessionID 拒绝测试与后台探测请求。
func TestOpencodeCompatProbesInjectValidSession(t *testing.T) {
	compatCases := []struct {
		compat bool
		label  string
	}{
		{true, "opencode"},
		{false, "ordinary"},
	}
	for _, cc := range compatCases {
		for _, perKey := range []bool{false, true} {
			mode := "model"
			if perKey {
				mode = "key"
			}
			t.Run(cc.label+"/"+mode, func(t *testing.T) {
				beforeID := idSeq.Load()
				defer cleanupProbeRequests(beforeID)
				sessionCh := make(chan string, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sessionCh <- r.Header.Get(opencodeSessionHeader)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(testPathOpenAIChatResponse))
				}))
				defer upstream.Close()
				channel := model.Channel{
					ID: 9874, Name: cc.label + "/" + mode, Type: model.ChannelProviderOpenAI, Enabled: true,
					BaseURL: upstream.URL, Key: "test-key", OpencodeCompat: cc.compat,
					Models: []model.ChannelModel{{Name: "m", UpstreamProtocol: model.UpstreamProtocolChat}},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var err error
				if perKey {
					result := ChannelKeyTestResult{Label: "#1(test)"}
					err = sendKeyTestRequest(ctx, channel, "m", "hello", &result)
				} else {
					_, err = sendChannelTestRequest(ctx, channel, "m", "hello", "#1(test)", testPanelMaxTokens)
				}
				if err != nil {
					t.Fatal(err)
				}
				var session string
				select {
				case session = <-sessionCh:
				default:
					t.Fatal("no upstream request captured")
				}
				if cc.compat {
					if !validOpencodeSessionID(session) {
						t.Fatalf("OpencodeCompat 测试/探测应注入合法 x-opencode-session, 实际 %q", session)
					}
				} else if session != "" {
					t.Fatalf("非 OpencodeCompat 测试/探测不应注入 x-opencode-session, 实际 %q", session)
				}
			})
		}
	}
}

func awaitProbeState(t *testing.T, stream <-chan RequestState, match func(RequestState) bool) RequestState {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case state, ok := <-stream:
			if !ok {
				t.Fatal("request stream closed")
			}
			if match(state) {
				return state
			}
		case <-timer.C:
			t.Fatal("missing probe state")
		}
	}
}

// TestChannelProbeLifecycle 阻塞上游, 确认发送期间可见运行态且可人工终止,
// 成功/HTTP 失败/取消均以同一 ID 定稿。逐密钥测试共用相同契约。
func TestChannelProbeLifecycle(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		for _, perKey := range []bool{false, true} {
			name := outcome + "/model"
			if perKey {
				name = outcome + "/key"
			}
			t.Run(name, func(t *testing.T) {
				beforeID := idSeq.Load()
				defer cleanupProbeRequests(beforeID)
				beforeCount := totalRequestsCount.Load()
				_, stream := OpenRequestStream()
				defer CloseRequestStream(stream)
				entered := make(chan struct{}, 1)
				release := make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Errorf("read request: %v", err)
						return
					}
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if outcome == "failure" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
						return
					}
					_, _ = w.Write([]byte(testPathOpenAIChatResponse))
				}))
				defer upstream.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				channel := model.Channel{ID: 9872, Name: name, Type: model.ChannelProviderOpenAI, BaseURL: upstream.URL, Key: "test-key"}
				done := make(chan error, 1)
				go func() {
					if perKey {
						result := ChannelKeyTestResult{Label: "#1(test)"}
						done <- sendKeyTestRequest(ctx, channel, "m", "hello", &result)
					} else {
						_, err := sendChannelTestRequest(ctx, channel, "m", "hello", "#1(test)", testPanelMaxTokens)
						done <- err
					}
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("upstream was not called")
				}
				running := awaitProbeState(t, stream, func(state RequestState) bool {
					return state.Status == StatusRunning && state.Sending
				})
				if running.ClientIP != testProbeClientIP || running.Round != 1 || len(running.Attempts) != 1 || RequestBody(running.ID) == "" {
					t.Fatalf("invalid running probe: %+v", running)
				}
				if outcome == "cancel" {
					if !StopRequestByID(running.ID) {
						t.Fatal("probe cannot be stopped")
					}
				} else {
					release <- struct{}{}
				}
				var err error
				select {
				case err = <-done:
				case <-ctx.Done():
					t.Fatal("probe did not finish")
				}
				wantStatus, wantAttempt := StatusSuccess, AttemptSuccess
				if outcome == "failure" {
					wantStatus, wantAttempt = StatusFailed, AttemptFailed
				} else if outcome == "cancel" {
					wantStatus, wantAttempt = StatusCanceled, AttemptCanceled
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel error = %v", err)
					}
				}
				if (err == nil) != (outcome == "success") {
					t.Fatalf("unexpected error: %v", err)
				}
				finished := awaitProbeState(t, stream, func(state RequestState) bool {
					return state.ID == running.ID && state.Status != StatusRunning
				})
				if finished.Status != wantStatus || finished.Sending || finished.Attempts[0].Outcome != wantAttempt || idSeq.Load() != running.ID {
					t.Fatalf("invalid final state: %+v", finished)
				}
				if outcome == "success" && ResponseBody(finished.ID) != testPathOpenAIChatResponse {
					t.Fatal("response body missing")
				}
				if outcome == "failure" && (finished.Class != ErrClassUpstream4xx || finished.Error == "" || len(FailureSummaries(1, "")) != 1 || FailureSummaries(1, "")[0].ID != finished.ID) {
					t.Fatalf("failure missing from log: %+v", finished)
				}
				if outcome == "cancel" && finished.Class != ErrClassAdminAbort {
					t.Fatalf("cancel classification = %s", finished.Class)
				}
				if totalRequestsCount.Load() != beforeCount {
					t.Fatal("diagnostic probe counted as business client request")
				}
			})
		}
	}
}

func TestChannelProbeBuildFailureIsLogged(t *testing.T) {
	beforeID := idSeq.Load()
	defer cleanupProbeRequests(beforeID)
	_, stream := OpenRequestStream()
	defer CloseRequestStream(stream)
	_, err := sendChannelTestRequest(context.Background(), model.Channel{ID: 9873, Name: "bad-provider", Type: "invalid"}, "m", "hello", "", testPanelMaxTokens)
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	failed := awaitProbeState(t, stream, func(state RequestState) bool { return state.Status == StatusFailed })
	if failed.Error == "" || failed.TargetChannel != "bad-provider" || failed.Round != 0 {
		t.Fatalf("invalid preparation failure: %+v", failed)
	}
}
