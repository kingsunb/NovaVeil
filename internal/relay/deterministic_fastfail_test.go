package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// createOpencodeIntegrationChannel 构造 OpencodeCompat 渠道;
// ChannelCreate 强制关闭该标记, 故落库后改列并刷新缓存。
func createOpencodeIntegrationChannel(t *testing.T, name string, baseURL, modelName string) model.Channel {
	t.Helper()
	channel := createIntegrationChannel(t, name, model.ChannelProviderOpenAI, baseURL, modelName)
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).Update("opencode_compat", true).Error; err != nil {
		t.Fatalf("标记 OpenCode 兼容渠道失败: %v", err)
	}
	enabled := true
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Enabled: &enabled}, context.Background()); err != nil {
		t.Fatalf("刷新 OpenCode 兼容渠道缓存失败: %v", err)
	}
	core, err := op.ChannelGetCore(channel.ID)
	if err != nil || !core.OpencodeCompat {
		t.Fatalf("OpenCode 标记未进入缓存: %v", err)
	}
	return channel
}

func TestDeterministicRequestRejectionContinuesAcrossMembers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		stream bool
	}{
		{
			name:   "400 context length",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"maximum context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		},
		{
			name:   "422 invalid request",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":{"message":"prompt is too long","type":"invalid_request_error"}}`,
		},
		{
			name:   "400 streaming",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"string_above_max_length","type":"invalid_request_error"}}`,
			stream: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setupFailoverTest(t)

			var firstHits, secondHits atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondHits.Add(1)
				if testCase.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-second","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"second-member"}}]}`)
					writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-second","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
					writeUpstreamSSE(t, w, "", "[DONE]")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(chatCompletionBody("chatcmpl-should-not-run", "second-member", 1, 1))
			}))
			defer second.Close()

			firstModel := integrationUniqueName("it-det-fastfail-first")
			secondModel := integrationUniqueName("it-det-fastfail-second")
			firstChannel := createOpencodeIntegrationChannel(t, "it-det-fastfail-first-ch", first.URL, firstModel)
			secondChannel := createIntegrationChannel(t, "it-det-fastfail-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
			group := createIntegrationGroup(t, "it-det-fastfail-group", refSkipConfig(),
				integrationLeafItem(t, firstChannel, firstModel),
				integrationLeafItem(t, secondChannel, secondModel))
			firstItemID := itemIDByModelName(t, group, firstModel)
			secondItemID := itemIDByModelName(t, group, secondModel)

			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, group.Name, testCase.stream)
			begin := time.Now()
			expectedID := nextRequestID()
			recorder := postRelayJSON(t, engine, path, body, "", nil)
			elapsed := time.Since(begin)

			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "second-member") {
				t.Fatalf("确定性拒绝应切换到下一成员并成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
			}
			if got := firstHits.Load(); got != 1 {
				t.Fatalf("首个成员应只调用一次, 实际 %d", got)
			}
			if got := secondHits.Load(); got != 1 {
				t.Fatalf("第二个成员应承载一次, 实际调用 %d 次", got)
			}
			if elapsed >= 6*time.Second {
				t.Fatalf("应快速切换成员, 实际耗时 %v", elapsed)
			}
			snapshot := routeSnapshot(t, group.ID)
			if _, cooling := snapshot.Cooldowns[firstItemID]; cooling {
				t.Fatalf("首个成员未达失败阈值, 不应冷却, 实际冷却截止 %d", snapshot.Cooldowns[firstItemID])
			}
			state := requestStateOf(t, expectedID)
			if state.Status != StatusSuccess || len(state.Attempts) != 2 {
				t.Fatalf("应记录一次失败后成功, 实际 %s/%+v", state.Status, state.Attempts)
			}
			if state.Attempts[0].MemberID != firstItemID || state.Attempts[0].Outcome != AttemptFailed || state.Attempts[0].ErrClass != ErrClassUpstream4xx {
				t.Fatalf("首轮应为首个成员的 4xx 失败, 实际 %+v", state.Attempts[0])
			}
			if state.Attempts[1].MemberID != secondItemID || state.Attempts[1].Outcome != AttemptSuccess {
				t.Fatalf("次轮应为第二个成员成功, 实际 %+v", state.Attempts[1])
			}
		})
	}
}

func TestOrdinary400SanitizesAndRetriesBeforeSuccess(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	var retryRole atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取上游请求失败: %v", err)
			return
		}
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role","type":"server_error"}}`))
			return
		}
		retryRole.Store(gjson.GetBytes(raw, "messages.0.role").String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-sanitized", "sanitized-ok", 2, 3))
	}))
	defer upstream.Close()

	modelName := integrationUniqueName("it-ordinary-400-model")
	channel := createIntegrationChannel(t, "it-ordinary-400-ch", model.ChannelProviderOpenAI, upstream.URL, modelName)
	group := createIntegrationGroup(t, "it-ordinary-400-group", refSkipConfig(), integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("普通 400 清洗后应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("普通 400 应先清洗并重试一次, 上游实际调用 %d 次", got)
	}
	if role, _ := retryRole.Load().(string); role != "system" {
		t.Fatalf("重试请求首条角色应清洗为 system, 实际 %q", role)
	}
}

func TestSingleOpencodeDeterministic400SanitizesOnceThenStops(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"prompt is too long","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	modelName := integrationUniqueName("it-det-sanitize")
	channel := createOpencodeIntegrationChannel(t, "it-det-sanitize-ch", upstream.URL, modelName)
	group := createIntegrationGroup(t, "it-det-sanitize-group", refSkipConfig(),
		integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowExceeded.Error()) {
		t.Fatalf("单成员清洗后仍上下文超限应以明确 400 收尾, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("单成员应只清洗重试一次后失败, 实际调用 %d 次", got)
	}
	if elapsed >= 6*time.Second {
		t.Fatalf("单成员确定性 400 应立即失败, 实际耗时 %v", elapsed)
	}
}

func TestDeterministic400ExhaustsAllMembersOnce(t *testing.T) {
	setupFailoverTest(t)

	var firstHits, secondHits atomic.Int64
	badUpstream := func(counter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`))
		}))
	}
	first := badUpstream(&firstHits)
	defer first.Close()
	second := badUpstream(&secondHits)
	defer second.Close()

	firstModel := integrationUniqueName("it-det-all-first")
	secondModel := integrationUniqueName("it-det-all-second")
	firstChannel := createOpencodeIntegrationChannel(t, "it-det-all-first-ch", first.URL, firstModel)
	secondChannel := createOpencodeIntegrationChannel(t, "it-det-all-second-ch", second.URL, secondModel)
	group := createIntegrationGroup(t, "it-det-all-group", refSkipConfig(),
		integrationLeafItem(t, firstChannel, firstModel),
		integrationLeafItem(t, secondChannel, secondModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowExceeded.Error()) {
		t.Fatalf("全组上下文超限应以明确 400 终止, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("首个成员应只尝试一次, 实际 %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("第二个成员应只尝试一次, 实际 %d", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed || len(state.Attempts) != 2 {
		t.Fatalf("应记录两个成员各一次失败, 实际 %s/%+v", state.Status, state.Attempts)
	}
	for i, attempt := range state.Attempts {
		if attempt.Outcome != AttemptFailed || attempt.ErrClass != ErrClassUpstream4xx {
			t.Fatalf("第 %d 轮应为 4xx 失败, 实际 %+v", i+1, attempt)
		}
	}
}

func TestConvertedDeterministic400FailsOverToNextMember(t *testing.T) {
	setupFailoverTest(t)

	var firstHits, secondHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-not-used", "second-member", 1, 1))
	}))
	defer second.Close()

	firstModel := integrationUniqueName("it-det-converted-first")
	secondModel := integrationUniqueName("it-det-converted-second")
	firstChannel := createOpencodeIntegrationChannel(t, "it-det-converted-first-ch", first.URL, firstModel)
	secondChannel := createIntegrationChannel(t, "it-det-converted-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
	group := createIntegrationGroup(t, "it-det-converted-group", refSkipConfig(),
		integrationLeafItem(t, firstChannel, firstModel),
		integrationLeafItem(t, secondChannel, secondModel))

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "second-member") {
		t.Fatalf("转换路径确定性拒绝应切换到下一成员并成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("转换路径首个成员应只调用一次, 实际 %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("转换路径第二个成员应承载一次, 实际调用 %d 次", got)
	}
}

// 普通渠道命中确定性标记时同样逐成员排除, 不得整请求终止。
func TestDeterministicRequestRejectionFailsOverForOrdinaryChannels(t *testing.T) {
	cases := []struct {
		name                string
		status              int
		body                string
		wantFirstMemberHits int64
	}{
		{
			name:                "400 invalid request",
			status:              http.StatusBadRequest,
			body:                `{"error":{"message":"invalid parameter","type":"invalid_request_error"}}`,
			wantFirstMemberHits: 1,
		},
		{
			name:                "422 prompt too long",
			status:              http.StatusUnprocessableEntity,
			body:                `{"error":{"message":"prompt is too long","type":"invalid_request_error"}}`,
			wantFirstMemberHits: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setupFailoverTest(t)

			var firstHits, secondHits atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(chatCompletionBody("chatcmpl-scoped", "second-member", 1, 1))
			}))
			defer second.Close()

			firstModel := integrationUniqueName("it-scoped-fastfail-first")
			secondModel := integrationUniqueName("it-scoped-fastfail-second")
			firstChannel := createIntegrationChannel(t, "it-scoped-fastfail-first-ch", model.ChannelProviderOpenAI, first.URL, firstModel)
			secondChannel := createIntegrationChannel(t, "it-scoped-fastfail-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
			group := createIntegrationGroup(t, "it-scoped-fastfail-group", refSkipConfig(),
				integrationLeafItem(t, firstChannel, firstModel),
				integrationLeafItem(t, secondChannel, secondModel))

			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
			recorder := postRelayJSON(t, engine, path, body, "", nil)

			if recorder.Code != http.StatusOK {
				t.Fatalf("普通渠道应故障转移成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
			}
			if got := firstHits.Load(); got != testCase.wantFirstMemberHits {
				t.Fatalf("普通渠道首个成员调用次数 = %d, 期望 %d", got, testCase.wantFirstMemberHits)
			}
			if got := secondHits.Load(); got != 1 {
				t.Fatalf("应有且仅有一次第二成员调用, 实际 %d", got)
			}
		})
	}
}

func TestCredentialAndRateLimitStatusesRemainRetryable(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{name: "401", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden},
		{name: "429", status: http.StatusTooManyRequests},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setupFailoverTest(t)
			resetChannelKeyHealth()

			var hits atomic.Int64
			var mu sync.Mutex
			var authHeaders []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				authHeaders = append(authHeaders, r.Header.Get("Authorization"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if hits.Add(1) == 1 {
					w.WriteHeader(testCase.status)
					_, _ = w.Write([]byte(`{"error":{"message":"maximum context length","type":"invalid_request_error","code":"context_length_exceeded"}}`))
					return
				}
				_, _ = w.Write(chatCompletionBody("chatcmpl-second-key", "second-key-ok", 2, 3))
			}))
			defer upstream.Close()

			modelName := integrationUniqueName("it-retryable-status-model")
			channel := model.Channel{
				Name:    integrationUniqueName("it-retryable-status-ch"),
				Type:    model.ChannelProviderOpenAI,
				Enabled: true,
				BaseURL: upstream.URL,
				Keys: []model.ChannelKey{
					{Key: "retryable-alpha", Remark: "alpha"},
					{Key: "retryable-beta", Remark: "beta"},
				},
				Models: []model.ChannelModel{{Name: modelName, Source: model.ChannelModelSourceManual}},
			}
			if err := op.ChannelCreate(&channel, context.Background()); err != nil {
				t.Fatalf("创建多 Key 渠道失败: %v", err)
			}
			group := createIntegrationGroup(t, "it-retryable-status-group", refSkipConfig(), integrationLeafItem(t, channel, modelName))

			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
			recorder := postRelayJSON(t, engine, path, body, "", nil)

			if recorder.Code != http.StatusOK {
				t.Fatalf("%d 应保持可重试并最终成功, 实际 %d: %s", testCase.status, recorder.Code, recorder.Body.String())
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("%d 应轮换到第二把 Key, 上游实际调用 %d 次", testCase.status, got)
			}
			if testCase.status == http.StatusTooManyRequests && !channelKeyCooling(channel.ID, channel.Keys[0].ID) {
				t.Fatal("非 OpenCode 渠道的 429 仍应冷却首把 Key")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(authHeaders) != 2 || authHeaders[0] == authHeaders[1] {
				t.Fatalf("%d 应使用两把不同 Key, 实际 %v", testCase.status, authHeaders)
			}
		})
	}
}

func TestIsDeterministicClientResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"400 context length", http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded"}}`, true},
		{"400 invalid request", http.StatusBadRequest, `{"error":{"type":"invalid_request"}}`, true},
		{"422 invalid request", http.StatusUnprocessableEntity, `{"error":{"type":"invalid_request_error"}}`, true},
		{"413 excluded", http.StatusRequestEntityTooLarge, `{"error":{"code":"context_length_exceeded"}}`, false},
		{"401 excluded", http.StatusUnauthorized, `{"error":{"type":"invalid_request_error"}}`, false},
		{"403 excluded", http.StatusForbidden, `{"error":{"type":"invalid_request_error"}}`, false},
		{"429 excluded", http.StatusTooManyRequests, `{"error":{"type":"invalid_request_error"}}`, false},
		{"generic 400", http.StatusBadRequest, `{"error":{"type":"server_error"}}`, false},
		{"500 excluded", http.StatusInternalServerError, `{"error":{"code":"context_length_exceeded"}}`, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isDeterministicClientResponse(testCase.status, []byte(testCase.body)); got != testCase.want {
				t.Fatalf("isDeterministicClientResponse(%d, %q) = %v, 期望 %v", testCase.status, testCase.body, got, testCase.want)
			}
		})
	}
}
