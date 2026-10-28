package relay

// 思考-only 空回答轮次(errNoAnswerStop)回归测试:
// 上游以 finish_reason=stop 正常收尾但整轮只产出推理内容、没有任何最终回答时,
// 非流式在提交前整轮判无效换目标重试; 已提交的流抑制终止帧并静默截断(不补发任何终止帧),
// 客户端按「SSE 流缺终止事件即断开」感知失败并自动重试整个请求,
// 而不是把"成功结束的空回答"交付给客户端。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/tidwall/gjson"
)

// TestAnalyzeChatStreamEventAnswerSplit 校验 chat 流事件的回答/推理信号拆分与 stop 终止标记:
// 推理增量只算推理不算回答, 文本/工具调用算回答, finish_reason=stop 单独置 stopFinish。
func TestAnalyzeChatStreamEventAnswerSplit(t *testing.T) {
	cases := []struct {
		name         string
		data         string
		hasAnswer    bool
		hasReasoning bool
		stopFinish   bool
	}{
		{"role 帧", `{"choices":[{"delta":{"role":"assistant"}}]}`, false, false, false},
		{"推理增量", `{"choices":[{"delta":{"reasoning_content":"think"}}]}`, false, true, false},
		{"文本增量", `{"choices":[{"delta":{"content":"hi"}}]}`, true, false, false},
		{"工具调用", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}`, true, false, false},
		{"stop 终止", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, false, false, true},
		{"length 终止", `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, false, false, false},
		{"stop 块携带回答", `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, true, false, true},
	}
	for _, tc := range cases {
		verdict := analyzeStreamEvent(llm.APIFormatOpenAIChatCompletion, sseEvent(tc.data))
		if verdict.hasAnswer != tc.hasAnswer || verdict.hasReasoning != tc.hasReasoning || verdict.stopFinish != tc.stopFinish {
			t.Fatalf("%s: 判定不符, 得到 answer=%v reasoning=%v stop=%v", tc.name, verdict.hasAnswer, verdict.hasReasoning, verdict.stopFinish)
		}
	}
}

// TestValidateRoundAnswerReasoningOnly 校验非流式思考-only 判定的边界:
// stop + 仅推理内容判 errNoAnswerStop; 有文本/工具调用、无推理、非 stop 终态、
// 非 OpenAI Chat 客户端协议均放行(由既有判定或零输出保险丝负责)。
func TestValidateRoundAnswerReasoningOnly(t *testing.T) {
	reasoningOnly := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"thinking..."},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":120,"total_tokens":125}}`)
	if err := validateRoundAnswer(llm.APIFormatOpenAIChatCompletion, reasoningOnly); !errors.Is(err, errNoAnswerStop) {
		t.Fatalf("stop+仅推理内容应判 errNoAnswerStop, 得到: %v", err)
	}

	nullContent := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"think"},"finish_reason":"stop"}]}`)
	if err := validateRoundAnswer(llm.APIFormatOpenAIChatCompletion, nullContent); !errors.Is(err, errNoAnswerStop) {
		t.Fatalf("stop+content null+仅推理内容应判 errNoAnswerStop, 得到: %v", err)
	}

	kept := [][]byte{
		// 有最终回答: 文本/分片数组/工具调用, 推理存在也不影响放行。
		[]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"think"},"finish_reason":"stop"}]}`),
		[]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"part"}]},"finish_reason":"stop"}]}`),
		[]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"stop"}]}`),
		// 无推理内容: 空回答交给零输出保险丝与既有判定, 本判定不介入。
		[]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`),
		// 非 stop 终态: length 截断轮次不在本判定范围。
		[]byte(`{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"think"},"finish_reason":"length"}]}`),
		// 无 message/无 choices 的形态放行, 由 validateResponse 负责。
		[]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":3,"total_tokens":4}}`),
	}
	for _, body := range kept {
		if err := validateRoundAnswer(llm.APIFormatOpenAIChatCompletion, body); err != nil {
			t.Fatalf("形态 %s 不应判思考-only, 得到: %v", body, err)
		}
	}

	// 非 OpenAI Chat 客户端协议不校验: Anthropic/Responses 的截断终态由既有异常判定覆盖。
	if err := validateRoundAnswer(llm.APIFormatAnthropicMessage, reasoningOnly); err != nil {
		t.Fatalf("非 chat 客户端协议不应校验, 得到: %v", err)
	}

	wrapped := fmt.Errorf("%w: context", errNoAnswerStop)
	if ClassifyError(wrapped) != ErrClassNoAnswer {
		t.Fatal("ClassifyError 应把思考-only 归入 no_answer 分类")
	}
	if classifyRound(wrapped, context.Background(), context.Background()) != ErrClassNoAnswer {
		t.Fatal("classifyRound 应把思考-only 归入 no_answer 分类")
	}
}

// TestForwardStreamingNoAnswerStopSilentTruncation 验证流式思考-only 轮次:
// 推理增量照常交付, stop 终止帧与 [DONE] 被抑制, 失败收尾静默截断(不补发错误帧);
// 请求按失败定稿归入 no_answer, 聚合用量保留, 成员计入提交后失败并直接进入冷却
// (MemberMaxAttempts=1 下首败即达阈值, 连击表清空转入冷却表, 见 recordPostCommitFailure)。
func TestForwardStreamingNoAnswerStopSilentTruncation(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think-a"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think-b"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":21,"total_tokens":24}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans")
	config := hardeningGroupConfig()
	config.MemberMaxAttempts = 1
	group := createIntegrationGroup(t, hardeningName("it-noans-stream"), config,
		integrationLeafItem(t, channel, "it-model-noans"))
	itemID := itemIDByModelName(t, group, "it-model-noans")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	frames := parseSSEFrames(t, recorder.Body.Bytes())
	sawReasoning, sawFinishStop := false, false
	for _, frame := range frames {
		switch {
		case strings.Contains(frame.data, "think-a"):
			sawReasoning = true
		case strings.Contains(frame.data, `"finish_reason":"stop"`):
			sawFinishStop = true
		}
		if strings.Contains(frame.data, `"error"`) {
			t.Fatalf("提交后的流失败应静默截断, 不补发错误帧, 实际: %s", frame.data)
		}
		if frame.data == "[DONE]" {
			t.Fatalf("静默截断不得以 [DONE] 收尾, 实际帧序列: %+v", frames)
		}
	}
	if !sawReasoning {
		t.Fatal("推理增量应照常交付给客户端")
	}
	if sawFinishStop {
		t.Fatal("思考-only 轮次的 stop 终止帧不得交付给客户端")
	}
	if last := frames[len(frames)-1]; !strings.Contains(last.data, "think-b") {
		t.Fatalf("最后一帧应为最后一个推理增量, 实际: %s", last.data)
	}

	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("思考-only 轮次应以失败终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
	if state.Class != ErrClassNoAnswer {
		t.Fatalf("终态应归入 no_answer 分类, 实际 %q", state.Class)
	}
	if state.Usage.CompletionTokens != 21 {
		t.Fatalf("已产生的 token 计量应保留, 实际: %+v", state.Usage)
	}
	snapshot := routeSnapshot(t, group.ID)
	if strikes := snapshot.PostCommitStrikes[itemID]; strikes != 0 {
		t.Fatalf("达阈值后连击表项应清空转入冷却, 实际 %d", strikes)
	}
	if deadline, cooling := snapshot.Cooldowns[itemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("思考-only 失败轮次达阈值后成员应进入冷却, 得到 %d/%v", deadline, cooling)
	}
}

// TestForwardNonStreamNoAnswerStopFailoverToHealthyMember 验证非流式思考-only 轮次:
// 提交前整轮判无效, 立即换下一优先级成员并成功交付; 失败轮次归入 no_answer 分类。
func TestForwardNonStreamNoAnswerStopFailoverToHealthyMember(t *testing.T) {
	setupFailoverTest(t)

	var badHits, goodHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-noans","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"thinking..."},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":120,"total_tokens":125}}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-ok", "ok", 5, 9))
	}))
	defer good.Close()

	badChannel := createIntegrationChannel(t, hardeningName("it-noans-bad-ch"), model.ChannelProviderOpenAI, bad.URL, "it-model-noans-bad")
	goodChannel := createIntegrationChannel(t, hardeningName("it-noans-good-ch"), model.ChannelProviderOpenAI, good.URL, "it-model-noans-good")
	group := createIntegrationGroup(t, hardeningName("it-noans-failover"), hardeningGroupConfig(),
		integrationLeafItem(t, badChannel, "it-model-noans-bad"),
		integrationLeafItem(t, goodChannel, "it-model-noans-good"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("故障转移后应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ok") {
		t.Fatalf("交付正文应来自健康成员, 实际: %s", recorder.Body.String())
	}
	if got := badHits.Load(); got != 1 {
		t.Fatalf("思考-only 成员应恰好尝试一次, 实际 %d 次", got)
	}
	if got := goodHits.Load(); got != 1 {
		t.Fatalf("健康成员应恰好尝试一次, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
	if len(state.Attempts) != 2 || state.Attempts[0].ErrClass != ErrClassNoAnswer {
		t.Fatalf("首轮尝试应归入 no_answer 分类, 实际: %+v", state.Attempts)
	}
}

// TestForwardStreamingReasoningWithAnswerDelivered 回归守卫:
// 推理与最终回答齐全的轮次不受思考-only 拦截, 照常以正常终止帧交付。
func TestForwardStreamingReasoningWithAnswerDelivered(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"final answer"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":30,"total_tokens":33}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-ok-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-ok")
	group := createIntegrationGroup(t, hardeningName("it-noans-with-answer"), hardeningGroupConfig(),
		integrationLeafItem(t, channel, "it-model-noans-ok"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("推理+回答齐全的轮次应正常交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, frame := range parseSSEFrames(t, recorder.Body.Bytes()) {
		if strings.Contains(frame.data, `"error"`) {
			t.Fatalf("不应合成错误帧, 实际: %s", frame.data)
		}
	}
	if !strings.Contains(recorder.Body.String(), `"finish_reason":"stop"`) {
		t.Fatal("stop 终止帧应照常交付")
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestApplyReasoningOnlyAsToolCalls 校验显式开关只改写思考-only 的 stop 轮次:
// 终态改为 tool_calls 且推理原文保留; 有回答、工具调用、截断及非 Chat 协议均不改写。
func TestApplyReasoningOnlyAsToolCalls(t *testing.T) {
	reasoningOnly := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"think-only"},"finish_reason":"stop"}]}`)
	got, changed := applyReasoningOnlyAsToolCalls(llm.APIFormatOpenAIChatCompletion, reasoningOnly)
	if !changed {
		t.Fatal("思考-only 的 stop 轮次应改写终态")
	}
	if reason := gjson.GetBytes(got, "choices.0.finish_reason").String(); reason != "tool_calls" {
		t.Fatalf("finish_reason 应为 tool_calls, 实际 %q", reason)
	}
	if content := gjson.GetBytes(got, "choices.0.message.content"); content.Type != gjson.Null {
		t.Fatalf("content 应保持 null, 实际 %s", content.Raw)
	}
	if reasoning := gjson.GetBytes(got, "choices.0.message.reasoning_content").String(); reasoning != "think-only" {
		t.Fatalf("reasoning_content 应保留, 实际 %q", reasoning)
	}

	unchanged := []struct {
		name   string
		format llm.APIFormat
		body   string
	}{
		{"已有回答", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning_content":"think"},"finish_reason":"stop"}]}`},
		{"工具调用", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"message":{"role":"assistant","reasoning_content":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"stop"}]}`},
		{"长度截断", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"message":{"role":"assistant","reasoning_content":"think"},"finish_reason":"length"}]}`},
		{"非 Chat 协议", llm.APIFormatAnthropicMessage, string(reasoningOnly)},
	}
	for _, tc := range unchanged {
		if next, ok := applyReasoningOnlyAsToolCalls(tc.format, []byte(tc.body)); ok || string(next) != tc.body {
			t.Fatalf("%s 不应改写", tc.name)
		}
	}
}

// TestForwardNonStreamReasoningOnlyAsToolCalls 校验开关开启时非流式思考-only 轮次
// 以 finish_reason=tool_calls 正常交付, 请求按成功定稿。
func TestForwardNonStreamReasoningOnlyAsToolCalls(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fallback","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"think-only"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`))
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-fb-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-fb")
	config := hardeningGroupConfig()
	config.ReasoningOnlyAsToolCalls = true
	group := createIntegrationGroup(t, hardeningName("it-noans-fb"), config,
		integrationLeafItem(t, channel, "it-model-noans-fb"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关开启时应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if reason := gjson.GetBytes(recorder.Body.Bytes(), "choices.0.finish_reason").String(); reason != "tool_calls" {
		t.Fatalf("交付终态应为 tool_calls, 实际 %q", reason)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestForwardStreamingReasoningOnlyAsToolCalls 校验开关开启时流式思考-only 轮次
// 终止块改写为 finish_reason=tool_calls, 请求按成功定稿。
func TestForwardStreamingReasoningOnlyAsToolCalls(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-fb","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-fb","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think-only"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-fb","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":21,"total_tokens":24}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-fb-stream-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-fb-stream")
	config := hardeningGroupConfig()
	config.ReasoningOnlyAsToolCalls = true
	group := createIntegrationGroup(t, hardeningName("it-noans-fb-stream"), config,
		integrationLeafItem(t, channel, "it-model-noans-fb-stream"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关开启时应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	rewritten := false
	for _, frame := range parseSSEFrames(t, recorder.Body.Bytes()) {
		if strings.Contains(frame.data, `"error"`) {
			t.Fatalf("改写轮次不应合成错误帧, 实际: %s", frame.data)
		}
		if gjson.Get(frame.data, "choices.0.finish_reason").String() == "tool_calls" {
			rewritten = true
		}
	}
	if !rewritten {
		t.Fatal("终止块 finish_reason 应改写为 tool_calls")
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// reasoningOnlyZeroUsageBody 是上游把思考轮次记为 output==0 的响应体:
// 思考内容非空、没有正文与工具调用, 终态仍是 stop。
func reasoningOnlyZeroUsageBody() string {
	return `{"id":"chatcmpl-zero","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"think-only"},"finish_reason":"stop"}],"usage":{"prompt_tokens":45446,"completion_tokens":0,"total_tokens":45446}}`
}

// TestForwardNonStreamReasoningOnlyZeroOutputAsToolCalls 回归: 零输出校验必须排在
// 终态改写之后。否则 0 输出 + stop 的思考轮次先被判 errZeroOutput, 开关永不生效。
func TestForwardNonStreamReasoningOnlyZeroOutputAsToolCalls(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reasoningOnlyZeroUsageBody()))
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-zero-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-zero")
	config := hardeningGroupConfig()
	config.ReasoningOnlyAsToolCalls = true
	group := createIntegrationGroup(t, hardeningName("it-noans-zero"), config,
		integrationLeafItem(t, channel, "it-model-noans-zero"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关开启时零输出思考轮次应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if reason := gjson.GetBytes(recorder.Body.Bytes(), "choices.0.finish_reason").String(); reason != "tool_calls" {
		t.Fatalf("交付终态应为 tool_calls, 实际 %q", reason)
	}
	if reasoning := gjson.GetBytes(recorder.Body.Bytes(), "choices.0.message.reasoning_content").String(); reasoning != "think-only" {
		t.Fatalf("推理原文应原样交付, 实际 %q", reasoning)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestForwardNonStreamReasoningOnlyZeroOutputFailsWhenDisabled 校验同一响应在开关
// 关闭时保持既有口径: 明确上报 output==0 仍判整轮无效, 并换健康成员交付。
func TestForwardNonStreamReasoningOnlyZeroOutputFailsWhenDisabled(t *testing.T) {
	setupFailoverTest(t)

	var badHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reasoningOnlyZeroUsageBody()))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-good", "healthy-answer", 5, 9))
	}))
	defer good.Close()

	badChannel := createIntegrationChannel(t, hardeningName("it-noans-zero-off-ch"), model.ChannelProviderOpenAI, bad.URL, "it-model-noans-zero-off")
	goodChannel := createIntegrationChannel(t, hardeningName("it-noans-zero-off-ok-ch"), model.ChannelProviderOpenAI, good.URL, "it-model-noans-zero-off-ok")
	group := createIntegrationGroup(t, hardeningName("it-noans-zero-off"), hardeningGroupConfig(),
		integrationLeafItem(t, badChannel, "it-model-noans-zero-off"),
		integrationLeafItem(t, goodChannel, "it-model-noans-zero-off-ok"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "healthy-answer") {
		t.Fatalf("应换健康成员交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := badHits.Load(); got != 1 {
		t.Fatalf("零输出成员应恰好尝试一次, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if len(state.Attempts) != 2 || state.Attempts[0].ErrClass != ErrClassZeroOutput {
		t.Fatalf("首轮尝试应归入 zero_output 分类, 实际: %+v", state.Attempts)
	}
}

// TestForwardNonStreamReasoningOnlySwitchKeepsEmptyRoundFailing 守卫硬性边界:
// 开关不得放行没有任何推理内容的空轮次, 否则客户端拿到的是空成功。
func TestForwardNonStreamReasoningOnlySwitchKeepsEmptyRoundFailing(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "纯空白轮次",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`,
		},
		{
			name: "有正文但零输出",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"think"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupFailoverTest(t)

			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer bad.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(chatCompletionBody("chatcmpl-good", "healthy-answer", 5, 9))
			}))
			defer good.Close()

			badChannel := createIntegrationChannel(t, hardeningName("it-noans-empty-ch"), model.ChannelProviderOpenAI, bad.URL, "it-model-noans-empty")
			goodChannel := createIntegrationChannel(t, hardeningName("it-noans-empty-ok-ch"), model.ChannelProviderOpenAI, good.URL, "it-model-noans-empty-ok")
			config := hardeningGroupConfig()
			config.ReasoningOnlyAsToolCalls = true
			group := createIntegrationGroup(t, hardeningName("it-noans-empty"), config,
				integrationLeafItem(t, badChannel, "it-model-noans-empty"),
				integrationLeafItem(t, goodChannel, "it-model-noans-empty-ok"))

			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
			expectedID := nextRequestID()
			recorder := postRelayJSON(t, engine, path, body, "", nil)

			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "healthy-answer") {
				t.Fatalf("该形态不得被开关放行, 应换健康成员交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
			}
			state := requestStateOf(t, expectedID)
			if len(state.Attempts) != 2 || state.Attempts[0].ErrClass != ErrClassZeroOutput {
				t.Fatalf("该形态仍应判 zero_output, 实际: %+v", state.Attempts)
			}
		})
	}
}

// TestForwardStreamingReasoningOnlyZeroOutputAsToolCalls 校验流式零输出思考轮次
// 在开关开启时改成 tool_calls 交付并成功定稿。
func TestForwardStreamingReasoningOnlyZeroOutputAsToolCalls(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-zero","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-zero","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think-only"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-zero","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":45446,"completion_tokens":0,"total_tokens":45446}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-zero-stream-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-zero-stream")
	config := hardeningGroupConfig()
	config.ReasoningOnlyAsToolCalls = true
	group := createIntegrationGroup(t, hardeningName("it-noans-zero-stream"), config,
		integrationLeafItem(t, channel, "it-model-noans-zero-stream"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关开启时流式零输出思考轮次应成功交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	rewritten := false
	for _, frame := range parseSSEFrames(t, recorder.Body.Bytes()) {
		if strings.Contains(frame.data, `"error"`) {
			t.Fatalf("改写轮次不应合成错误帧, 实际: %s", frame.data)
		}
		if gjson.Get(frame.data, "choices.0.finish_reason").String() == "tool_calls" {
			rewritten = true
		}
	}
	if !rewritten {
		t.Fatal("终止块 finish_reason 应改写为 tool_calls")
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestForwardStreamingReasoningOnlyZeroOutputFailsWhenDisabled 校验流式同一形态
// 在开关关闭时保持既有失败口径: 抑制终止帧、静默截断、按 no_answer 定稿。
func TestForwardStreamingReasoningOnlyZeroOutputFailsWhenDisabled(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-zero-off","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-zero-off","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think-only"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-zero-off","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":45446,"completion_tokens":0,"total_tokens":45446}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-noans-zero-off-stream-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-noans-zero-off-stream")
	group := createIntegrationGroup(t, hardeningName("it-noans-zero-off-stream"), hardeningGroupConfig(),
		integrationLeafItem(t, channel, "it-model-noans-zero-off-stream"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	for _, frame := range parseSSEFrames(t, recorder.Body.Bytes()) {
		if gjson.Get(frame.data, "choices.0.finish_reason").String() == "tool_calls" {
			t.Fatalf("开关关闭时不得改写终态, 实际: %s", frame.data)
		}
		if frame.data == "[DONE]" {
			t.Fatalf("思考-only 轮次不得以 [DONE] 收尾, 实际帧序列: %s", recorder.Body.String())
		}
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed || state.Class != ErrClassNoAnswer {
		t.Fatalf("应以 no_answer 失败定稿, 实际 %s/%s(%s)", state.Status, state.Class, state.Error)
	}
}

// TestRejectZeroOutputRewritesReasoningOnlyWindow 校验窗口判定与交付口径一致:
// 开关开启时先改写终态再判零输出, 同时把窗口内的 stop 终止帧改为 tool_calls。
func TestRejectZeroOutputRewritesReasoningOnlyWindow(t *testing.T) {
	newWindow := func() []*httpclient.StreamEvent {
		return []*httpclient.StreamEvent{
			sseEvent(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`),
			sseEvent(`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`),
			sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`),
		}
	}

	window := newWindow()
	if err := rejectZeroOutput(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), window, streamWindowOptions{reasoningOnlyAsToolCalls: true}); err != nil {
		t.Fatalf("开关开启时思考-only 窗口不应判零输出: %v", err)
	}
	if reason := gjson.GetBytes(window[2].Data, "choices.0.finish_reason").String(); reason != "tool_calls" {
		t.Fatalf("窗口内终止帧应同步改写为 tool_calls, 实际 %q", reason)
	}

	window = newWindow()
	if err := rejectZeroOutput(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), window, streamWindowOptions{}); !errors.Is(err, errZeroOutput) {
		t.Fatalf("开关关闭时应仍判 errZeroOutput, 得到: %v", err)
	}
	if reason := gjson.GetBytes(window[2].Data, "choices.0.finish_reason").String(); reason != "stop" {
		t.Fatalf("开关关闭时窗口内终止帧不得改写, 实际 %q", reason)
	}
}
