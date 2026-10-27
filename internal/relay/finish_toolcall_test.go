package relay

// 工具轮次终态下发回归测试:
// 上游已产出工具调用却以自然结束收尾(或缺 [DONE] 哨兵由网关补帧)时,
// 下发给客户端的终止原因必须是 tool_calls, 否则 Agent CLI 把整轮当作完成,
// 不再派发工具调用。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// TestResolveTerminalReason 校验终态校正规则:
// 有工具调用时自然结束一律升级为工具终态, 其余终态与纯文本轮次原样保留。
func TestResolveTerminalReason(t *testing.T) {
	cases := []struct {
		name        string
		format      llm.APIFormat
		reason      string
		sawToolCall bool
		want        string
	}{
		{"chat 空终态无工具", llm.APIFormatOpenAIChatCompletion, "", false, "stop"},
		{"chat 空终态有工具", llm.APIFormatOpenAIChatCompletion, "", true, "tool_calls"},
		{"chat stop 有工具", llm.APIFormatOpenAIChatCompletion, "stop", true, "tool_calls"},
		{"chat stop 无工具", llm.APIFormatOpenAIChatCompletion, "stop", false, "stop"},
		{"chat tool_calls 保持", llm.APIFormatOpenAIChatCompletion, "tool_calls", true, "tool_calls"},
		{"chat length 不被改写", llm.APIFormatOpenAIChatCompletion, "length", true, "length"},
		{"anthropic 空终态无工具", llm.APIFormatAnthropicMessage, "", false, "end_turn"},
		{"anthropic 空终态有工具", llm.APIFormatAnthropicMessage, "", true, "tool_use"},
		{"anthropic end_turn 有工具", llm.APIFormatAnthropicMessage, "end_turn", true, "tool_use"},
		{"anthropic tool_use 保持", llm.APIFormatAnthropicMessage, "tool_use", true, "tool_use"},
		{"responses 不改写", llm.APIFormatOpenAIResponse, "", true, ""},
	}
	for _, tc := range cases {
		if got := resolveTerminalReason(tc.format, tc.reason, tc.sawToolCall); got != tc.want {
			t.Fatalf("%s: 期望 %q, 得到 %q", tc.name, tc.want, got)
		}
	}
}

// TestTerminalStreamFramesCarryReason 校验合成收尾帧使用传入终态而非固定 stop。
func TestTerminalStreamFramesCarryReason(t *testing.T) {
	chat := terminalStreamFrames(llm.APIFormatOpenAIChatCompletion, "tool_calls")
	if len(chat) != 2 {
		t.Fatalf("chat 收尾应为 finish chunk + [DONE], 得到 %d 帧", len(chat))
	}
	if got := gjson.GetBytes(chat[0].Data, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("chat 收尾帧应携带 tool_calls, 实际 %q: %s", got, chat[0].Data)
	}

	anthropic := terminalStreamFrames(llm.APIFormatAnthropicMessage, "tool_use")
	if len(anthropic) != 2 {
		t.Fatalf("anthropic 收尾应为 message_delta + message_stop, 得到 %d 帧", len(anthropic))
	}
	if got := gjson.GetBytes(anthropic[0].Data, "delta.stop_reason").String(); got != "tool_use" {
		t.Fatalf("anthropic 收尾帧应携带 tool_use, 实际 %q: %s", got, anthropic[0].Data)
	}
}

// finalFinishReason 返回下游帧序列中最后一个非空 finish_reason。
func finalFinishReason(frames []sseFrame) string {
	final := ""
	for _, frame := range frames {
		if value := gjson.Get(frame.data, "choices.0.finish_reason"); value.Exists() {
			final = value.String()
		}
	}
	return final
}

// TestForwardStreamingToolCallWithoutDoneKeepsToolCalls 核心回归:
// 上游发出工具调用与 tool_calls 终态但不发 [DONE], 网关补发收尾帧时不得降级为 stop。
func TestForwardStreamingToolCallWithoutDoneKeepsToolCalls(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`)
		// 不发 [DONE]: 复现上游缺终止哨兵的完整流
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-toolcall-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-toolcall")
	group := createIntegrationGroup(t, hardeningName("it-toolcall-nodone"), hardeningGroupConfig(),
		integrationLeafItem(t, channel, "it-model-toolcall"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if got := finalFinishReason(frames); got != "tool_calls" {
		t.Fatalf("工具轮次收尾必须为 tool_calls, 实际 %q, 帧序列: %+v", got, frames)
	}
	if last := frames[len(frames)-1]; last.data != "[DONE]" {
		t.Fatalf("合成收尾应以 [DONE] 结束, 实际: %s", last.data)
	}
}

// TestForwardStreamingToolCallReportedAsStopCorrected 上游错报 stop:
// 本轮已出现工具调用增量时, 终止帧在下发前改写为 tool_calls。
func TestForwardStreamingToolCallReportedAsStopCorrected(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-stop","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"pwsh","arguments":"{}"}}]}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-stop","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-toolstop-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-toolstop")
	group := createIntegrationGroup(t, hardeningName("it-toolstop"), hardeningGroupConfig(),
		integrationLeafItem(t, channel, "it-model-toolstop"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if got := finalFinishReason(frames); got != "tool_calls" {
		t.Fatalf("含工具调用的轮次不得以 stop 下发, 实际 %q, 帧序列: %+v", got, frames)
	}
}

// TestForwardStreamingTextStopUnchanged 回归守卫:
// 纯文本轮次的 stop 终态不得被误改为 tool_calls。
func TestForwardStreamingTextStopUnchanged(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c-text","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"done"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c-text","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-textstop-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-textstop")
	group := createIntegrationGroup(t, hardeningName("it-textstop"), hardeningGroupConfig(),
		integrationLeafItem(t, channel, "it-model-textstop"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if got := finalFinishReason(frames); got != "stop" {
		t.Fatalf("纯文本轮次应保持 stop, 实际 %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "done") {
		t.Fatal("正文应照常交付")
	}
}
