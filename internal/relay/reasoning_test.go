package relay

// 多轮 reasoning 回传缓存测试: 覆盖 LRU/TTL 行为(注入假时钟)、聚合响应体采集、
// 发送前注入的幂等与渠道矩阵, 以及两轮对话端到端回放集成。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// isolateReasoningCache 把全局缓存替换为独立实例并在测试结束后还原, 避免用例间串扰。
func isolateReasoningCache(t *testing.T) *reasoningCache {
	t.Helper()
	cache := newReasoningCache(reasoningCacheCapacity, reasoningCacheTTL, time.Now)
	old := reasonings
	reasonings = cache
	t.Cleanup(func() { reasonings = old })
	return cache
}

// TestReasoningCacheLRUEviction 验证超容淘汰最久未使用条目, 且 Get 命中会刷新近期使用。
func TestReasoningCacheLRUEviction(t *testing.T) {
	cache := newReasoningCache(3, time.Hour, time.Now)
	cache.Set("a", "ra")
	cache.Set("b", "rb")
	cache.Set("c", "rc")
	// 命中 a 使其成为最近使用, 最久未使用变为 b。
	if value, ok := cache.Get("a"); !ok || value != "ra" {
		t.Fatalf("a 应命中且值为 ra, 得到 %q/%v", value, ok)
	}
	cache.Set("d", "rd")
	if _, ok := cache.Get("b"); ok {
		t.Fatal("容量为 3 时最久未使用的 b 应被淘汰")
	}
	for key, want := range map[string]string{"a": "ra", "c": "rc", "d": "rd"} {
		if value, ok := cache.Get(key); !ok || value != want {
			t.Fatalf("%s 应保留且值为 %q, 得到 %q/%v", key, want, value, ok)
		}
	}
	// 重复写同一键只占一个条目并刷新值。
	cache.Set("a", "ra2")
	if value, ok := cache.Get("a"); !ok || value != "ra2" {
		t.Fatalf("重复写入应刷新值, 得到 %q/%v", value, ok)
	}
	cache.mu.Lock()
	size := cache.order.Len()
	cache.mu.Unlock()
	if size != 3 {
		t.Fatalf("重复写入不应增加条目数, 实际 %d 条", size)
	}
}

// TestReasoningCacheTTLExpiry 用假时钟验证 TTL 过期与 Set 时的惰性清理。
func TestReasoningCacheTTLExpiry(t *testing.T) {
	current := time.Unix(1700000000, 0)
	cache := newReasoningCache(8, 2*time.Hour, func() time.Time { return current })

	cache.Set("call-ttl", "think")
	current = current.Add(time.Hour - time.Minute)
	if value, ok := cache.Get("call-ttl"); !ok || value != "think" {
		t.Fatalf("TTL 内应命中, 得到 %q/%v", value, ok)
	}
	// 共推进 2 小时零 1 分钟, 条目过期即未命中且当场清除。
	current = current.Add(2*time.Hour + time.Minute)
	if _, ok := cache.Get("call-ttl"); ok {
		t.Fatal("超过 TTL 后应视为未命中")
	}
	cache.mu.Lock()
	size := cache.entries["call-ttl"] != nil
	cache.mu.Unlock()
	if size {
		t.Fatal("过期的条目应在 Get 时被清除")
	}
	// Set 时同样惰性清理已过期条目。
	cache.Set("stale", "old")
	current = current.Add(3 * time.Hour)
	cache.Set("fresh", "new")
	cache.mu.Lock()
	count := cache.order.Len()
	cache.mu.Unlock()
	if count != 1 {
		t.Fatalf("Set 惰性清理后应只剩 1 条, 实际 %d 条", count)
	}
	if value, ok := cache.Get("fresh"); !ok || value != "new" {
		t.Fatalf("未过期的新条目应保留, 得到 %q/%v", value, ok)
	}
}

// TestCollectReasoningsFromMessageShape 验证 OpenAI Chat 聚合形状的采集:
// 同一消息内多个 tool_call 各记一条, 无 tool_calls 的消息不记录。
func TestCollectReasoningsFromMessageShape(t *testing.T) {
	isolateReasoningCache(t)
	body := []byte(`{"id":"resp","choices":[
		{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
			"reasoning_content":"step-one",
			"tool_calls":[{"id":"call_m1","type":"function","function":{"name":"f","arguments":"{}"}},
			              {"id":"call_m2","type":"function","function":{"name":"g","arguments":"{}"}}]}},
		{"index":1,"finish_reason":"stop","message":{"role":"assistant","content":"hi","reasoning_content":"lonely"}}
	]}`)
	collectReasonings(body)
	for _, id := range []string{"call_m1", "call_m2"} {
		if value, ok := getReasoningFor(id); !ok || value != "step-one" {
			t.Fatalf("%s 应记录 reasoning, 得到 %q/%v", id, value, ok)
		}
	}
	cache := reasonings
	cache.mu.Lock()
	count := cache.order.Len()
	cache.mu.Unlock()
	if count != 2 {
		t.Fatalf("无 tool_calls 的消息不应产生条目, 实际 %d 条", count)
	}
}

// TestCollectReasoningsFromDeltaShape 验证兼容尚未累积成 message 的 delta 原始形态。
func TestCollectReasoningsFromDeltaShape(t *testing.T) {
	isolateReasoningCache(t)
	collectReasonings([]byte(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`))
	collectReasonings([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_d"}],"reasoning_content":"frag"}}]}`))
	if value, ok := getReasoningFor("call_d"); !ok || value != "frag" {
		t.Fatalf("delta 形态应尽力记录, 得到 %q/%v", value, ok)
	}
}

// TestCollectReasoningsIgnoresInvalidOrAbsent 验证非法形状静默忽略;
// 空字符串 reasoning 与上游聚合器口径一致按有效值记录。
func TestCollectReasoningsIgnoresInvalidOrAbsent(t *testing.T) {
	isolateReasoningCache(t)
	for _, body := range []string{
		`not-json`,
		`{"choices":{}}`,
		`{"choices":[{"message":{"role":"assistant"}}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_x"}],"reasoning_content":null}}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":""}],"reasoning_content":"orphan"}}]}`,
	} {
		collectReasonings([]byte(body))
	}
	if _, ok := getReasoningFor("call_x"); ok {
		t.Fatal("null reasoning 不应被记录")
	}
	if _, ok := getReasoningFor(""); ok {
		t.Fatal("空 id 不应被记录")
	}
	isolateReasoningCache(t)
	collectReasonings([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_empty"}],"reasoning_content":""}}]}`))
	if value, ok := getReasoningFor("call_empty"); !ok || value != "" {
		t.Fatalf("空串 reasoning 应按有效值记录, 得到 %q/%v", value, ok)
	}
}

// TestInjectMissingReasoningsHit 验证缓存命中的补齐注入与精确落点。
func TestInjectMissingReasoningsHit(t *testing.T) {
	isolateReasoningCache(t)
	setReasonings(map[string]string{"call_a": "think-a"})
	body := []byte(`{"model":"g","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_a","content":"ok"}
	]}`)
	got := injectMissingReasonings(body, llm.APIFormatOpenAIChatCompletion, model.ChannelProviderOpenAI)
	if value := gjson.GetBytes(got, "messages.1.reasoning_content").String(); value != "think-a" {
		t.Fatalf("应向 assistant 消息注入 think-a, 实际 %q", value)
	}
	if gjson.GetBytes(got, "messages.0.reasoning_content").Exists() || gjson.GetBytes(got, "messages.2.reasoning_content").Exists() {
		t.Fatal("非 assistant 消息不得被注入")
	}
}

// TestInjectMissingReasoningsPreservesExisting 验证已有值(哪怕空串)一律不覆盖。
func TestInjectMissingReasoningsPreservesExisting(t *testing.T) {
	isolateReasoningCache(t)
	setReasonings(map[string]string{"call_a": "cached-a", "call_b": "cached-b"})
	body := []byte(`{"model":"g","messages":[
		{"role":"assistant","tool_calls":[{"id":"call_a"}],"reasoning_content":"client-kept"},
		{"role":"assistant","tool_calls":[{"id":"call_b"}],"reasoning_content":""},
		{"role":"assistant","tool_calls":[{"id":"call_c"}],"reasoning_content":null}
	]}`)
	out := injectMissingReasonings(body, llm.APIFormatOpenAIChatCompletion, model.ChannelProviderOpenAI)
	if value := gjson.GetBytes(out, "messages.0.reasoning_content").String(); value != "client-kept" {
		t.Fatalf("消息 0 已有值不得覆盖, 实际 %q", value)
	}
	if raw := gjson.GetBytes(out, "messages.1.reasoning_content").Raw; raw != `""` {
		t.Fatalf("消息 1 的空串已有值不得覆盖, 实际 %s", raw)
	}
	if raw := gjson.GetBytes(out, "messages.2.reasoning_content").Raw; raw != `null` {
		t.Fatalf("消息 2 的 null 已有值不得覆盖, 实际 %s", raw)
	}
}

// TestInjectMissingReasoningsMissNoPlaceholder 验证未命中不注入也不造占位符,
// 且仅以首个 tool_call.id 作查询键。
func TestInjectMissingReasoningsMissNoPlaceholder(t *testing.T) {
	isolateReasoningCache(t)
	setReasonings(map[string]string{"call_second": "only-second"})
	body := []byte(`{"model":"g","messages":[
		{"role":"assistant","tool_calls":[{"id":"call_unknown"},{"id":"call_second"}]}
	]}`)
	got := injectMissingReasonings(body, llm.APIFormatOpenAIChatCompletion, model.ChannelProviderOpenAI)
	if bytes.Contains(got, []byte("reasoning_content")) {
		t.Fatalf("未命中首个 tool_call.id 时不得注入或造占位符, 实际: %s", got)
	}
}

// TestInjectMissingReasoningsIdempotent 验证同一 body 重复调用不产生重复字段。
func TestInjectMissingReasoningsIdempotent(t *testing.T) {
	isolateReasoningCache(t)
	setReasonings(map[string]string{"call_idem": "stable"})
	body := []byte(`{"model":"g","messages":[{"role":"assistant","tool_calls":[{"id":"call_idem"}]}]}`)
	once := injectMissingReasonings(body, llm.APIFormatOpenAIChatCompletion, model.ChannelProviderOpenAI)
	twice := injectMissingReasonings(once, llm.APIFormatOpenAIChatCompletion, model.ChannelProviderOpenAI)
	if !bytes.Equal(once, twice) {
		t.Fatalf("重复注入必须幂等\n一次: %s\n两次: %s", once, twice)
	}
	if got := gjson.GetBytes(twice, "messages.#.reasoning_content"); len(got.Array()) != 1 {
		t.Fatalf("应恰好一个 reasoning_content 字段, 实际 %d 个", len(got.Array()))
	}
}

// TestInjectMissingReasoningsChannelMatrix 验证注入条件的渠道类型矩阵:
// 仅 OpenAI 注入, 其余渠道原样返回。
func TestInjectMissingReasoningsChannelMatrix(t *testing.T) {
	isolateReasoningCache(t)
	setReasonings(map[string]string{"call_matrix": "matrix-think"})
	build := func() []byte {
		return []byte(`{"model":"g","messages":[{"role":"assistant","tool_calls":[{"id":"call_matrix"}]}]}`)
	}
	cases := []struct {
		provider model.ChannelProvider
		inject   bool
	}{
		{model.ChannelProviderOpenAI, true},
		{model.ChannelProviderAnthropic, false},
		{model.ChannelProviderOpenAIResponses, false},
	}
	for _, testCase := range cases {
		body := build()
		got := injectMissingReasonings(body, llm.APIFormatOpenAIChatCompletion, testCase.provider)
		injected := gjson.GetBytes(got, "messages.0.reasoning_content").String() == "matrix-think"
		if injected != testCase.inject {
			t.Fatalf("渠道 %s 注入期望 %v, 实际 %v", testCase.provider, testCase.inject, injected)
		}
		if !testCase.inject && !bytes.Equal(body, got) {
			t.Fatalf("渠道 %s 不应改动请求体", testCase.provider)
		}
	}
	// 非 OpenAI Chat 客户端协议一律跳过, 即便渠道是 OpenAI 系。
	body := build()
	got := injectMissingReasonings(body, llm.APIFormatAnthropicMessage, model.ChannelProviderOpenAI)
	if !bytes.Equal(body, got) {
		t.Fatal("anthropic 客户端协议不应改动请求体")
	}
	got = injectMissingReasonings(body, llm.APIFormatOpenAIResponse, model.ChannelProviderOpenAI)
	if !bytes.Equal(body, got) {
		t.Fatal("responses 客户端协议不应改动请求体")
	}
}

// TestReasoningReplayTwoRoundsIntegration 端到端验证两轮对话回放:
// 第一轮工具调用响应中的 reasoning_content 被网关采集(非流式与流式各一),
// 第二轮客户端剥离该字段后由网关向上游补齐。
func TestReasoningReplayTwoRoundsIntegration(t *testing.T) {
	setupFailoverTest(t)
	isolateReasoningCache(t)

	const (
		idNonStream   = "call_it_replay_nonstream"
		idStream      = "call_it_replay_stream"
		textNonStream = "collected-non-stream-thinking"
		textStream    = "collected-" + "stream-thinking"
	)

	var bodiesMu sync.Mutex
	var upstreamBodies [][]byte
	var replayedNonStream, replayedStream atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		upstreamBodies = append(upstreamBodies, body)
		bodiesMu.Unlock()

		var parsed struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role             string          `json:"role"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				ToolCalls        []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &parsed)

		hasAssistantToolCall := false
		for _, message := range parsed.Messages {
			if message.Role != "assistant" || len(message.ToolCalls) == 0 {
				continue
			}
			hasAssistantToolCall = true
			var reasoning string
			_ = json.Unmarshal(message.ReasoningContent, &reasoning)
			switch message.ToolCalls[0].ID {
			case idNonStream:
				if reasoning == textNonStream {
					replayedNonStream.Store(true)
				}
			case idStream:
				if reasoning == textStream {
					replayedStream.Store(true)
				}
			}
		}

		// 尚无历史工具调用的首轮请求按流式与否分别下发带 reasoning_content 的工具调用响应。
		if !hasAssistantToolCall {
			if !parsed.Stream {
				w.Header().Set("Content-Type", "application/json")
				response := map[string]any{
					"id": "chatcmpl-it-replay-n", "object": "chat.completion", "created": 1700000000, "model": "it-model",
					"choices": []map[string]any{{
						"index":         0,
						"finish_reason": "tool_calls",
						"message": map[string]any{
							"role": "assistant", "content": nil, "reasoning_content": textNonStream,
							"tool_calls": []map[string]any{{
								"id": idNonStream, "type": "function",
								"function": map[string]any{"name": "get_weather", "arguments": `{"city":"sf"}`},
							}},
						},
					}},
					"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 9, "total_tokens": 16},
				}
				data, _ := json.Marshal(response)
				_, _ = w.Write(data)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
			writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"collected-"}}]}`)
			writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"stream-thinking"}}]}`)
			writeUpstreamSSE(t, w, "", fmt.Sprintf(`{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"get_time","arguments":""}}]}}]}`, idStream))
			writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`)
			writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-it-replay-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":6,"completion_tokens":8,"total_tokens":14}}`)
			writeUpstreamSSE(t, w, "", "[DONE]")
			return
		}

		// 第二轮请求: 断言网关已把剥离的 reasoning_content 补齐, 再给出最终答复。
		w.Header().Set("Content-Type", "application/json")
		final := map[string]any{
			"id": "chatcmpl-it-replay-f", "object": "chat.completion", "created": 1700000000, "model": "it-model",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "final-answer"},
			}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 5, "total_tokens": 16},
		}
		data, _ := json.Marshal(final)
		_, _ = w.Write(data)
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-replay-upstream", model.ChannelProviderOpenAI, upstream.URL, "it-model-replay")
	group := createIntegrationGroup(t, "it-reasoning-replay",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-model-replay"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	post := func(messages []map[string]any, streaming bool) *httptest.ResponseRecorder {
		payload, err := json.Marshal(map[string]any{"model": group.Name, "messages": messages, "stream": streaming})
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		return postRelayJSON(t, engine, path, string(payload), "", nil)
	}

	// 第一轮(非流式): 上游产出带 reasoning_content 的工具调用, 网关采集。
	firstTurn := []map[string]any{{"role": "user", "content": "weather?"}}
	recorder := post(firstTurn, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("第一轮应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(idNonStream)) {
		t.Fatalf("第一轮响应应包含工具调用 id, 实际: %s", recorder.Body.String())
	}

	// 第二轮(非流式): 客户端剥离 reasoning_content, 网关须向上游补齐。
	secondTurn := []map[string]any{
		{"role": "user", "content": "weather?"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
			"id": idNonStream, "type": "function",
			"function": map[string]any{"name": "get_weather", "arguments": `{"city":"sf"}`},
		}}},
		{"role": "tool", "tool_call_id": idNonStream, "content": "sunny"},
	}
	recorder = post(secondTurn, false)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte("final-answer")) {
		t.Fatalf("第二轮应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !replayedNonStream.Load() {
		bodiesMu.Lock()
		sent := make([]string, 0, len(upstreamBodies))
		for _, item := range upstreamBodies {
			sent = append(sent, string(item))
		}
		bodiesMu.Unlock()
		t.Fatalf("第二轮上游应收到的 reasoning_content=%q 未被补齐, 上游收到的请求体: %v", textNonStream, sent)
	}

	// 第三轮(流式): 分片 reasoning_content 聚合后被采集。
	recorder = post(firstTurn, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("第三轮应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) == 0 || frames[len(frames)-1].data != "[DONE]" {
		t.Fatalf("第三轮流应以 [DONE] 收尾, 实际末帧: %+v", frames[max(len(frames)-1, 0)])
	}

	// 第四轮(非流式): 验证流式采集结果同样能被回放补齐。
	fourthTurn := []map[string]any{
		{"role": "user", "content": "time?"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
			"id": idStream, "type": "function",
			"function": map[string]any{"name": "get_time", "arguments": "{}"},
		}}},
		{"role": "tool", "tool_call_id": idStream, "content": "noon"},
	}
	recorder = post(fourthTurn, false)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte("final-answer")) {
		t.Fatalf("第四轮应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !replayedStream.Load() {
		t.Fatalf("第四轮上游应收到的 reasoning_content=%q 未被补齐", textStream)
	}
}
