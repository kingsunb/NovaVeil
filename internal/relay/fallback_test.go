package relay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/tidwall/gjson"
)

// fakeStream 用固定事件序列构造 streams.Stream, 指针越界后 Next 返回 false。
type fakeStream struct {
	events []*httpclient.StreamEvent
	index  int
}

func (f *fakeStream) Next() bool {
	f.index++
	return f.index <= len(f.events)
}

func (f *fakeStream) Current() *httpclient.StreamEvent {
	if f.index < 1 || f.index > len(f.events) {
		return nil
	}
	return f.events[f.index-1]
}

func (f *fakeStream) Err() error { return nil }

func (f *fakeStream) Close() error { return nil }

func sseEvent(data string) *httpclient.StreamEvent {
	return &httpclient.StreamEvent{Data: []byte(data)}
}

func TestValidateRoundUsage(t *testing.T) {
	if err := validateRoundUsage(nil, ""); err != nil {
		t.Fatalf("usage 缺失不应触发重试: %v", err)
	}
	if err := validateRoundUsage(&llm.Usage{CompletionTokens: 3}, ""); err != nil {
		t.Fatalf("正常用量不应触发重试: %v", err)
	}
	err := validateRoundUsage(&llm.Usage{PromptTokens: 5}, "")
	if !errors.Is(err, errZeroOutput) {
		t.Fatalf("明确上报输出为 0 应返回 errZeroOutput, 得到: %v", err)
	}
	// 合法空输出终态放行交付, 防止脏数据误杀 max_tokens=1 连通性 ping。
	for _, terminal := range []string{"length", "tool_calls", "function_call", "max_tokens", "tool_use"} {
		if err := validateRoundUsage(&llm.Usage{PromptTokens: 5}, terminal); err != nil {
			t.Fatalf("终态 %q 下 output==0 应放行, 得到: %v", terminal, err)
		}
	}
	// 拿不到终态或终态不在白名单内时维持原判定。
	for _, terminal := range []string{"", "stop", "network_error"} {
		if err := validateRoundUsage(&llm.Usage{PromptTokens: 5}, terminal); !errors.Is(err, errZeroOutput) {
			t.Fatalf("终态 %q 下 output==0 应仍判 errZeroOutput, 得到: %v", terminal, err)
		}
	}
}

func TestStreamEventHasContent(t *testing.T) {
	cases := []struct {
		name   string
		format llm.APIFormat
		data   string
		want   bool
	}{
		{"chat 文本增量", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"delta":{"content":"hi"}}]}`, true},
		{"chat 推理增量", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"delta":{"reasoning_content":"think"}}]}`, true},
		{"chat 工具调用", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, true},
		{"chat 仅 role 帧", llm.APIFormatOpenAIChatCompletion, `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"chat 纯用量帧", llm.APIFormatOpenAIChatCompletion, `{"choices":[],"usage":{"total_tokens":9}}`, false},
		{"anthropic 块开始", llm.APIFormatAnthropicMessage, `{"type":"content_block_start"}`, true},
		{"anthropic 块增量", llm.APIFormatAnthropicMessage, `{"type":"content_block_delta","delta":{"text":"a"}}`, true},
		{"anthropic message_start", llm.APIFormatAnthropicMessage, `{"type":"message_start"}`, false},
		{"anthropic ping", llm.APIFormatAnthropicMessage, `{"type":"ping"}`, false},
		{"responses 文本增量", llm.APIFormatOpenAIResponse, `{"type":"response.output_text.delta","delta":"x"}`, true},
		{"responses item added", llm.APIFormatOpenAIResponse, `{"type":"output_item.added","item":{}}`, true},
		{"responses created", llm.APIFormatOpenAIResponse, `{"type":"response.created"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := streamEventHasContent(tc.format, sseEvent(tc.data))
			if got != tc.want {
				t.Fatalf("streamEventHasContent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsTerminalStreamEvent(t *testing.T) {
	if !isTerminalStreamEvent(llm.APIFormatOpenAIChatCompletion, sseEvent("[DONE]")) {
		t.Fatal("chat [DONE] 应识别为终止")
	}
	if isTerminalStreamEvent(llm.APIFormatOpenAIChatCompletion, sseEvent(`{"choices":[{"delta":{"content":"[DONE]"}}]}`)) {
		t.Fatal("正文中的 [DONE] 不是终止")
	}
	if !isTerminalStreamEvent(llm.APIFormatAnthropicMessage, sseEvent(`{"type":"message_stop"}`)) {
		t.Fatal("anthropic message_stop 应识别为终止")
	}
	for _, typ := range []string{"response.completed", "response.failed", "response.incomplete", "response.cancelled"} {
		if !isTerminalStreamEvent(llm.APIFormatOpenAIResponse, sseEvent(`{"type":"`+typ+`"}`)) {
			t.Fatalf("responses %s 应识别为终止", typ)
		}
	}
	if isTerminalStreamEvent(llm.APIFormatOpenAIResponse, sseEvent(`{"type":"response.output_text.delta","delta":"done"}`)) {
		t.Fatal("responses 增量不是终止")
	}
}

func TestTerminalStreamFrames(t *testing.T) {
	// 缺 [DONE] 哨兵的完整流仍合成 finish chunk + [DONE]。污染流不再走这条收尾。
	openaiOk := terminalStreamFrames(llm.APIFormatOpenAIChatCompletion)
	if len(openaiOk) != 2 || gjson.GetBytes(openaiOk[0].Data, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("chat 正常收尾应为 stop finish chunk + [DONE], 得到 %d 帧", len(openaiOk))
	}

	anthropicFrames := terminalStreamFrames(llm.APIFormatAnthropicMessage)
	if len(anthropicFrames) != 2 ||
		anthropicFrames[0].Type != "message_delta" || anthropicFrames[1].Type != "message_stop" {
		t.Fatalf("anthropic 终止序列应为 message_delta + message_stop, 得到 %+v", anthropicFrames)
	}

	okFrames := terminalStreamFrames(llm.APIFormatOpenAIResponse)
	if len(okFrames) != 1 || okFrames[0].Type != "response.completed" {
		t.Fatalf("responses 收尾应为 response.completed, 得到 %v", okFrames)
	}
	if gjson.GetBytes(okFrames[0].Data, "response.status").String() != "completed" {
		t.Fatalf("response.status 应为 completed, 得到 %s", okFrames[0].Data)
	}
}

func TestReadStreamWindowCommitsAtFirstContent(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"created":1,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"he"}}]}`),
		sseEvent(`{"created":1,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":"stop"}]}`),
		sseEvent("[DONE]"),
	}}
	window, ended, terminated, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), stream)
	if err != nil {
		t.Fatalf("有效流不应报错: %v", err)
	}
	if ended || terminated {
		t.Fatal("出现内容信号后窗口应立即关闭且不标记终止")
	}
	if len(window) != 2 {
		t.Fatalf("窗口应包含 role 帧和首个内容帧, 得到 %d 帧", len(window))
	}
	// 剩余事件仍可从原流继续读取。
	if !stream.Next() {
		t.Fatal("内容信号之后的事件应留在原流中继续转发")
	}
}

func TestReadStreamWindowRejectsExplicitZeroOutput(t *testing.T) {
	usageChunk := `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(usageChunk),
		sseEvent("[DONE]"),
	}}
	_, _, _, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), stream)
	if !errors.Is(err, errZeroOutput) {
		t.Fatalf("明确上报输出为 0 应返回 errZeroOutput, 得到: %v", err)
	}
}

func TestReadStreamWindowDeliversWhenUsageMissing(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		sseEvent("[DONE]"),
	}}
	window, ended, terminated, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), stream)
	if err != nil {
		t.Fatalf("未上报用量的空响应不应触发重试: %v", err)
	}
	if !ended || !terminated {
		t.Fatal("流已在窗口内耗尽且收到终止事件")
	}
	if len(window) == 0 {
		t.Fatal("窗口事件应保留以便交付客户端")
	}
	if strings.Contains(string(window[len(window)-1].Data), "novaveil") {
		t.Fatal("交付路径不应混入合成帧")
	}
}
