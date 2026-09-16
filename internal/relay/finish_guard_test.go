package relay

// 异常终止原因防护测试: 上游在普通数据帧中下发白名单之外的 finish_reason/stop_reason
// (如 "network_error")时, 三条拦截路径必须保证该取值不出现在下游可见字节流中:
//  1. 窗口期: readStreamWindow 逐事件检查, 整轮按失败返回并换目标重试;
//  2. 转发期: 已提交后抑制污染块, 合成干净协议终止帧收尾, 本轮记失败并计入成员失败;
//  3. 非流式: validateResponse 统一白名单校验, 异常响应整轮判无效。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

func TestValidChatFinishReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"", true},
		{"stop", true},
		{"length", true},
		{"tool_calls", true},
		{"function_call", true},
		{"other", true},
		{"network_error", false},
		{"error", false},
		{"aborted", false},
		{"content_filter", false},
		{"stop ", false},
		{"STOP", false},
	}
	for _, tc := range cases {
		if got := validChatFinishReason(tc.reason); got != tc.want {
			t.Fatalf("validChatFinishReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestValidAnthropicStopReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"", true},
		{"end_turn", true},
		{"max_tokens", true},
		{"stop_sequence", true},
		{"tool_use", true},
		// 文档化良性终态, 明确放行。
		{"pause_turn", true},
		{"refusal", true},
		{"network_error", false},
		{"error", false},
		{"aborted", false},
		{"end_turn,", false},
	}
	for _, tc := range cases {
		if got := validAnthropicStopReason(tc.reason); got != tc.want {
			t.Fatalf("validAnthropicStopReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestStreamEventAbnormalFinish(t *testing.T) {
	cases := []struct {
		name     string
		format   llm.APIFormat
		data     string
		abnormal bool
	}{
		// OpenAI Chat: 白名单内放行, 缺失/空/null 放行。
		{"chat stop", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, false},
		{"chat tool_calls", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, false},
		{"chat 空原因", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":""}]}`, false},
		{"chat null 原因", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":null}]}`, false},
		{"chat 无 choices", llm.APIFormatOpenAIChatCompletion, `{"choices":[],"usage":{"total_tokens":1}}`, false},
		{"chat network_error", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":"network_error"}]}`, true},
		{"chat aborted", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"delta":{},"finish_reason":"aborted"}]}`, true},
		{"chat DONE", llm.APIFormatOpenAIChatCompletion, `[DONE]`, false},
		{"chat 多 choice 混合", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":"network_error"}]}`, true},

		// Anthropic: message_delta 的 stop_reason 白名单。
		{"anthropic end_turn", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`, false},
		{"anthropic max_tokens", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, false},
		{"anthropic 无 stop_reason", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_sequence":null}}`, false},
		{"anthropic pause_turn 放行", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_reason":"pause_turn"}}`, false},
		{"anthropic refusal 放行", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_reason":"refusal"}}`, false},
		{"anthropic network_error", llm.APIFormatAnthropicMessage, `{"type":"message_delta","delta":{"stop_reason":"network_error"}}`, true},
		{"anthropic 非终止事件", llm.APIFormatAnthropicMessage, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`, false},

		// Responses: 仅 completed 且 status==completed 正常。
		{"responses completed", llm.APIFormatOpenAIResponse, `{"type":"response.completed","response":{"status":"completed"}}`, false},
		{"responses created", llm.APIFormatOpenAIResponse, `{"type":"response.created","response":{"status":"in_progress"}}`, false},
		{"responses failed", llm.APIFormatOpenAIResponse, `{"type":"response.failed","response":{"status":"failed"}}`, true},
		{"responses incomplete", llm.APIFormatOpenAIResponse, `{"type":"response.incomplete","response":{"status":"incomplete"}}`, true},
		{"responses cancelled", llm.APIFormatOpenAIResponse, `{"type":"response.cancelled","response":{"status":"cancelled"}}`, true},
		{"responses error 事件", llm.APIFormatOpenAIResponse, `{"type":"error","code":"x","message":"boom"}`, true},
		{"responses completed 但 status 异常", llm.APIFormatOpenAIResponse, `{"type":"response.completed","response":{"status":"network_error"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := streamEventAbnormalFinish(tc.format, &httpclient.StreamEvent{Data: []byte(tc.data)})
			if tc.abnormal && !errors.Is(err, errAbnormalFinish) {
				t.Fatalf("应判定为异常终止, 得到: %v", err)
			}
			if !tc.abnormal && err != nil {
				t.Fatalf("不应判定为异常终止, 得到: %v", err)
			}
		})
	}
}

func TestValidateResponseFinishWhitelist(t *testing.T) {
	choice := func(reason string) []byte {
		payload := map[string]any{
			"id":      "resp_x",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": reason}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	// 三种格式的统一 FinishReason 共用同一白名单。
	for _, format := range []llm.APIFormat{llm.APIFormatOpenAIChatCompletion, llm.APIFormatAnthropicMessage, llm.APIFormatOpenAIResponse} {
		for _, reason := range []string{"stop", "tool_calls", "function_call", "other"} {
			var response llm.Response
			if err := json.Unmarshal(choice(reason), &response); err != nil {
				t.Fatal(err)
			}
			if err := validateResponse(format, &response); err != nil {
				t.Fatalf("%s: 白名单内 finish_reason %q 不应失败, 得到: %v", format, reason, err)
			}
		}
		// length 对 Chat/Anthropic 是正常截断终态; Responses 协议把它编码为 incomplete 终态,
		// 保持既有的类型化失败口径。
		var lengthResponse llm.Response
		if err := json.Unmarshal(choice("length"), &lengthResponse); err != nil {
			t.Fatal(err)
		}
		err := validateResponse(format, &lengthResponse)
		if format == llm.APIFormatOpenAIResponse {
			var respErr *llm.ResponseError
			if !errors.As(err, &respErr) || respErr.Detail.Type != "response_incomplete" {
				t.Fatalf("responses length 应保持 response_incomplete 失败, 得到: %v", err)
			}
		} else if err != nil {
			t.Fatalf("%s: finish_reason length 不应失败, 得到: %v", format, err)
		}
		for _, reason := range []string{"network_error", "aborted", "cancelled"} {
			var response llm.Response
			if err := json.Unmarshal(choice(reason), &response); err != nil {
				t.Fatal(err)
			}
			err := validateResponse(format, &response)
			if format == llm.APIFormatOpenAIResponse && reason == "cancelled" {
				// Responses 协议以正常 200 响应携带取消终态, 保持既有类型化失败。
				var respErr *llm.ResponseError
				if !errors.As(err, &respErr) || respErr.Detail.Type != "response_cancelled" {
					t.Fatalf("responses cancelled 应保持类型化失败, 得到: %v", err)
				}
				continue
			}
			if !errors.Is(err, errAbnormalFinish) {
				t.Fatalf("%s: finish_reason %q 应判异常终止, 得到: %v", format, reason, err)
			}
		}
	}

	// 无 finish_reason 的响应照常交付; 空 choices 且无 usage 的脏响应判失败;
	// 空 choices 但携带用量的响应仍放行, 交由后续用量校验判定。
	var plain llm.Response
	if err := validateResponse(llm.APIFormatOpenAIChatCompletion, &plain); err == nil {
		t.Fatal("既无 choices 也无 usage 的空响应应判失败")
	}
	withUsage := llm.Response{Usage: &llm.Usage{PromptTokens: 1, TotalTokens: 1}}
	if err := validateResponse(llm.APIFormatOpenAIChatCompletion, &withUsage); err != nil {
		t.Fatalf("携带用量且无 choices 的响应不应在此判失败: %v", err)
	}
	noReason := llm.Response{Choices: []llm.Choice{{Index: 0}}}
	if err := validateResponse(llm.APIFormatOpenAIChatCompletion, &noReason); err != nil {
		t.Fatalf("有 choices 无 finish_reason 的响应不应失败: %v", err)
	}
	if err := validateResponse(llm.APIFormatOpenAIChatCompletion, nil); err == nil {
		t.Fatal("nil 响应应报错")
	}
}

// TestReadStreamWindowRejectsAbnormalFinishChunk 验证窗口期拦截:
// 内容信号出现前收到携带异常 finish_reason 的 chunk 时, 整轮按 errAbnormalFinish 失败返回。
func TestReadStreamWindowRejectsAbnormalFinishChunk(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"network_error"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}`),
		sseEvent("[DONE]"),
	}}
	window, ended, terminated, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), stream)
	if !errors.Is(err, errAbnormalFinish) {
		t.Fatalf("窗口内异常 finish chunk 应回包装 errAbnormalFinish 的错误, 得到: %v", err)
	}
	if window != nil || ended || terminated {
		t.Fatal("整轮失败时不得交付任何窗口事件")
	}
}

// TestReadStreamWindowClosesAtContentBeforeAbnormalChunk 验证内容信号优先提交:
// 提交点之后出现的异常 chunk 属于转发期拦截职责, 窗口阶段不重复处理。
func TestReadStreamWindowClosesAtContentBeforeAbnormalChunk(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{"content":"he"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"network_error"}]}`),
		sseEvent("[DONE]"),
	}}
	window, ended, terminated, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), stream)
	if err != nil {
		t.Fatalf("内容提交后窗口应正常关闭, 得到: %v", err)
	}
	if ended || terminated {
		t.Fatal("出现内容信号后窗口应立即关闭且不标记终止")
	}
	if len(window) != 2 {
		t.Fatalf("窗口应止步于首个内容帧, 实际 %d 帧", len(window))
	}
}

// TestForwardingSuppressesAbnormalFinishChunkOpenAI 验证转发期拦截(OpenAI Chat):
// 已提交后上游下发 finish_reason=network_error 的块, 该块与其后所有帧都被抑制,
// 客户端字节流不含该值并以合成 finish chunk + [DONE] 规范收尾; 成员计一次失败进入冷却,
// 且已提交的轮次不再触发故障转移。
func TestForwardingSuppressesAbnormalFinishChunkOpenAI(t *testing.T) {
	setupFailoverTest(t)

	var badHits, okHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-bad-a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-bad-a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial-answer"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-bad-a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"network_error"}],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-bad-a","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer bad.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-guard-b", "full-answer", 3, 7))
	}))
	defer ok.Close()

	badChannel := createIntegrationChannel(t, "it-finish-bad", model.ChannelProviderOpenAI, bad.URL, "it-model-finish-bad")
	okChannel := createIntegrationChannel(t, "it-finish-ok", model.ChannelProviderOpenAI, ok.URL, "it-model-finish-ok")
	group := createIntegrationGroup(t, "it-finish-forward",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			MemberAffinitySeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, badChannel, "it-model-finish-bad"),
		integrationLeafItem(t, okChannel, "it-model-finish-ok"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, "network_error") {
		t.Fatalf("异常终止原因不得出现在下游字节流中, 实际: %s", responseBody)
	}
	if !strings.Contains(responseBody, "partial-answer") {
		t.Fatalf("污染前的已转发内容应保留, 实际: %s", responseBody)
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) < 3 {
		t.Fatalf("客户端应收到的帧数不足, 实际 %d 帧: %+v", len(frames), frames)
	}
	if frames[len(frames)-1].data != "[DONE]" {
		t.Fatalf("流应以 [DONE] 规范收尾, 实际末帧: %+v", frames[len(frames)-1])
	}
	var finish struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(frames[len(frames)-2].data), &finish); err != nil {
		t.Fatalf("解析倒数第二帧失败: %v (data: %s)", err, frames[len(frames)-2].data)
	}
	if len(finish.Choices) == 0 || finish.Choices[0].FinishReason != "stop" {
		t.Fatalf("合成收尾应以 stop 终止, 实际: %s", frames[len(frames)-2].data)
	}

	if got := badHits.Load(); got != 1 {
		t.Fatalf("污染成员应只被尝试一次(已提交不再换目标), 实际 %d 次", got)
	}
	if got := okHits.Load(); got != 0 {
		t.Fatalf("健康成员不应被触碰, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("污染轮次应以失败终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
	if state.Usage.CompletionTokens != 7 {
		t.Fatalf("已产生的 token 计量应保留, 实际: %+v", state.Usage)
	}
}

// TestForwardingSuppressesAbnormalStopReasonAnthropic 验证转发期拦截(Anthropic):
// message_delta 携带异常 stop_reason 时该帧与 message_stop 被抑制,
// 客户端收到合成 message_delta(end_turn) + message_stop 干净收尾。
func TestForwardingSuppressesAbnormalStopReasonAnthropic(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_bad","type":"message","role":"assistant","content":[],"model":"it-anthropic-bad","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
		writeUpstreamSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
		writeUpstreamSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"network_error","stop_sequence":null},"usage":{"output_tokens":4}}`)
		writeUpstreamSSE(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-anthropic-finish-bad", model.ChannelProviderAnthropic, upstream.URL, "it-model-anthropic-bad")
	group := createIntegrationGroup(t, "it-finish-anthropic",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-model-anthropic-bad"))

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, "network_error") {
		t.Fatalf("异常 stop_reason 不得出现在下游字节流中, 实际: %s", responseBody)
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	wantTypes := []string{"message_start", "content_block_delta", "message_delta", "message_stop"}
	if len(frames) != len(wantTypes) {
		t.Fatalf("污染帧被抑制后应恰好收到 %d 帧, 实际 %d 帧: %+v", len(wantTypes), len(frames), frames)
	}
	for i, want := range wantTypes {
		if got := frames[i].frameType(); got != want {
			t.Fatalf("第 %d 帧类型 = %q, 期望 %q (data: %s)", i+1, got, want, frames[i].data)
		}
	}
	var delta struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(frames[len(frames)-2].data), &delta); err != nil {
		t.Fatalf("解析合成 message_delta 失败: %v", err)
	}
	if delta.Delta.StopReason != "end_turn" {
		t.Fatalf("合成 message_delta stop_reason 应为 end_turn, 实际 %q", delta.Delta.StopReason)
	}
}

// TestNonStreamAbnormalFinishFailsOverToNextMember 验证非流式拦截:
// 高优成员以 200 返回携带异常 finish_reason 的完整响应, 整轮判无效并换低优成员成功交付,
// 全程客户端无感知且看不到异常值。
func TestNonStreamAbnormalFinishFailsOverToNextMember(t *testing.T) {
	setupFailoverTest(t)

	var badHits, okHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bad-ns","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","content":"broken-upstream"},"finish_reason":"network_error"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`))
	}))
	defer bad.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-ok-ns", "healthy-answer", 3, 5))
	}))
	defer ok.Close()

	badChannel := createIntegrationChannel(t, "it-ns-finish-bad", model.ChannelProviderOpenAI, bad.URL, "it-model-ns-bad")
	okChannel := createIntegrationChannel(t, "it-ns-finish-ok", model.ChannelProviderOpenAI, ok.URL, "it-model-ns-ok")
	group := createIntegrationGroup(t, "it-finish-non-stream",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, badChannel, "it-model-ns-bad"),
		integrationLeafItem(t, okChannel, "it-model-ns-ok"))
	badItemID := itemIDByModelName(t, group, "it-model-ns-bad")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到成功响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, "network_error") || strings.Contains(responseBody, "broken-upstream") {
		t.Fatalf("异常成员的响应不得透传给客户端, 实际: %s", responseBody)
	}
	if !strings.Contains(responseBody, "healthy-answer") {
		t.Fatalf("响应应由健康成员承载, 实际: %s", responseBody)
	}
	if got := badHits.Load(); got != 2 {
		t.Fatalf("异常成员应重试至 MemberMaxAttempts=2 次, 实际 %d 次", got)
	}
	if got := okHits.Load(); got != 1 {
		t.Fatalf("健康成员应恰好承载一次, 实际 %d 次", got)
	}
	snapshot := routeSnapshot(t, group.ID)
	if deadline, cooling := snapshot.Cooldowns[badItemID]; !cooling || deadline <= 0 {
		t.Fatalf("异常 finish 成员应计入失败并冷却, 得到 %d/%v", deadline, cooling)
	}
}

// TestWindowAbnormalFinishFailsOverBeforeCommit 验证窗口期拦截端到端:
// 零内容流在窗口内即携带异常 finish chunk, 整轮在提交前失败并换健康成员完整承载,
// 异常成员的任何帧都不应到达客户端。
func TestWindowAbnormalFinishFailsOverBeforeCommit(t *testing.T) {
	setupFailoverTest(t)

	var badHits, okHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-wbad","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-wbad","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"network_error"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer bad.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-wok","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-wok","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"window-safe"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-wok","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer ok.Close()

	badChannel := createIntegrationChannel(t, "it-window-bad", model.ChannelProviderOpenAI, bad.URL, "it-model-window-bad")
	okChannel := createIntegrationChannel(t, "it-window-ok", model.ChannelProviderOpenAI, ok.URL, "it-model-window-ok")
	group := createIntegrationGroup(t, "it-finish-window",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, badChannel, "it-model-window-bad"),
		integrationLeafItem(t, okChannel, "it-model-window-ok"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, "network_error") || strings.Contains(responseBody, "chatcmpl-wbad") {
		t.Fatalf("窗口期拦截后异常成员的任何帧都不得交付客户端, 实际: %s", responseBody)
	}
	if !strings.Contains(responseBody, "window-safe") {
		t.Fatalf("客户端应收到健康成员内容, 实际: %s", responseBody)
	}
	if got := badHits.Load(); got != 1 {
		t.Fatalf("异常成员应在首轮后被放弃, 实际尝试 %d 次", got)
	}
	if got := okHits.Load(); got != 1 {
		t.Fatalf("健康成员应恰好承载一次, 实际 %d 次", got)
	}
}

// TestResponsesCompletedWithBadStatusSuppressedInForwarding 验证 Responses 转发期兜底:
// 已提交后收到 status 非 completed 的 response.completed 事件时抑制并合成干净 completed 收尾。
func TestResponsesCompletedWithBadStatusSuppressedInForwarding(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "response.created", `{"type":"response.created","response":{"id":"resp_guard","status":"in_progress"}}`)
		writeUpstreamSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","item_id":"i1","delta":"partial"}`)
		writeUpstreamSSE(t, w, "response.completed", `{"type":"response.completed","response":{"id":"resp_guard","status":"network_error"}}`)
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-resp-finish-bad", model.ChannelProviderOpenAIResponses, upstream.URL, "it-model-resp-bad")
	group := createIntegrationGroup(t, "it-finish-responses",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-model-resp-bad"))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/responses", Forward(llm.APIFormatOpenAIResponse))
	body := fmt.Sprintf(`{"model":%q,"input":"hi","stream":true}`, group.Name)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, `"network_error"`) {
		t.Fatalf("异常 response.status 不得出现在下游字节流中, 实际: %s", responseBody)
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	last := frames[len(frames)-1]
	if last.frameType() != "response.completed" {
		t.Fatalf("流应以合成的 response.completed 收尾, 实际末帧: %+v", last)
	}
	var completed struct {
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(last.data), &completed); err != nil {
		t.Fatalf("解析合成 response.completed 失败: %v", err)
	}
	if completed.Response.Status != "completed" {
		t.Fatalf("合成 response.completed status 应为 completed, 实际 %q", completed.Response.Status)
	}
}
