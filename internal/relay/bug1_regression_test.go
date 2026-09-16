package relay

// Bug 1 回归测试: 转换 pipeline 的 outbound transformer 在上游返回 0 字节 SSE 时
// 会向流追加合成终止事件(如 Anthropic outbound 的 llm.DoneStreamEvent), 使空流
// 形态上看似正常结束。readStreamWindow 必须识别"终止事件前无任何内容信号"的形态
// 并返回 errStreamEarlyEof, 而不是把空响应当成功交付给客户端。
//
// 覆盖三种客户端协议格式下合成终止事件 + 无内容增量的场景, 以及
// "终止事件本身携带内容"的正常流不受影响。

import (
	"context"
	"errors"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

// TestReadStreamWindowTerminalWithoutContent 验证 Bug 1 修复:
// 合成终止事件([DONE] / message_stop)成为流中唯一或首个事件时,
// readStreamWindow 应返回 errStreamEarlyEof 而非成功终态。
func TestReadStreamWindowTerminalWithoutContent(t *testing.T) {
	// --- OpenAI Chat 格式: [DONE]-only 流 ---
	// 模拟 Anthropic outbound transformer 对 0 字节上游的合成行为
	doneOnly := &fakeStream{events: []*httpclient.StreamEvent{
		&llm.DoneStreamEvent,
	}}
	window, ended, terminated, err := readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIChatCompletion,
		openai.NewInboundTransformer(),
		doneOnly,
	)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("[DONE]-only 流(OpenAI Chat)应返回 errStreamEarlyEof, 得到: %v (window=%v, ended=%v, terminated=%v)", err, window, ended, terminated)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}

	// --- OpenAI Chat 格式: finish_reason-only 流(无内容增量) ---
	// 部分上游在 0 字节响应时可能只发终止块, 不发 [DONE] 哨兵
	finishReasonOnly := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIChatCompletion,
		openai.NewInboundTransformer(),
		finishReasonOnly,
	)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("finish_reason-only 流(OpenAI Chat)应返回 errStreamEarlyEof, 得到: %v (window=%v, ended=%v, terminated=%v)", err, window, ended, terminated)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}

	// --- Anthropic 格式: message_stop-only 流 ---
	// 模拟转换 pipeline 合成的 message_stop 事件成为流中唯一事件
	messageStopOnly := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"message_stop"}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatAnthropicMessage,
		anthropic.NewInboundTransformer(),
		messageStopOnly,
	)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("message_stop-only 流(Anthropic)应返回 errStreamEarlyEof, 得到: %v (window=%v, ended=%v, terminated=%v)", err, window, ended, terminated)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}

	// --- Anthropic 格式: 结构帧 + message_stop(无内容增量, 无 stop_reason, 无用量) ---
	// message_start + message_stop 但没有 content_block_delta 也没有 message_delta.stop_reason
	// message_start 不带 usage 以排除 errZeroOutput 路径, 验证 !sawContent 拦截
	structuralThenStop := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"union-alpha","stop_reason":null}}`),
		sseEvent(`{"type":"message_stop"}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatAnthropicMessage,
		anthropic.NewInboundTransformer(),
		structuralThenStop,
	)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("结构帧+message_stop 流(Anthropic, 无 stop_reason)应返回 errStreamEarlyEof, 得到: %v (window=%v, ended=%v, terminated=%v)", err, window, ended, terminated)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}

	// --- Anthropic 格式: message_delta.stop_reason + message_stop(无内容, 有 stop_reason, 无用量) ---
	// 上游声明了白名单内的非空 stop_reason, 即使无内容也视为合法空完成, 不应拦截
	emptyAnthropicCompletion := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		sseEvent(`{"type":"message_stop"}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatAnthropicMessage,
		anthropic.NewInboundTransformer(),
		emptyAnthropicCompletion,
	)
	if err != nil {
		t.Fatalf("合法空完成(Anthropic, stop_reason=end_turn)不应返回错误: %v", err)
	}
	if !ended || !terminated {
		t.Fatal("流已在窗口内耗尽且收到终止事件")
	}

	// --- OpenAI Response 格式: response.completed-only 流(无内容增量) ---
	// outbound transformer 对 0 字节上游会合成 response.completed, 成为流中唯一事件。
	// Response 格式的 analyzeStreamEvent 不对 response.completed 设置 finishSeen,
	// 故 !sawContent && !sawFinishSeen 命中, 返回 errStreamEarlyEof。
	completedOnly := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"response.completed","response":{"status":"completed"}}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIResponse,
		responses.NewInboundTransformer(),
		completedOnly,
	)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("response.completed-only 流(Response)应返回 errStreamEarlyEof, 得到: %v (window=%v, ended=%v, terminated=%v)", err, window, ended, terminated)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}
}

// TestReadStreamWindowContentBeforeTerminal 验证正常流不受 Bug 1 修复影响:
// 内容事件出现在终止事件之前时, readStreamWindow 在首个内容信号处提交,
// 终止事件不会进入窗口(由后续转发循环处理)。
func TestReadStreamWindowContentBeforeTerminal(t *testing.T) {
	// 正常流: 内容增量 + [DONE]
	normalStream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"content":"hello"}}]}`),
		&llm.DoneStreamEvent,
	}}
	window, ended, terminated, err := readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIChatCompletion,
		openai.NewInboundTransformer(),
		normalStream,
	)
	if err != nil {
		t.Fatalf("正常流不应返回错误: %v", err)
	}
	if ended || terminated {
		t.Fatal("首个内容信号处应返回 ended=false, terminated=false")
	}
	if len(window) != 1 {
		t.Fatalf("窗口应包含 1 个事件(内容增量), 得到 %d 个", len(window))
	}

	// 正常 Anthropic 流: content_block_delta + message_stop
	normalAnthropic := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`),
		sseEvent(`{"type":"message_stop"}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatAnthropicMessage,
		anthropic.NewInboundTransformer(),
		normalAnthropic,
	)
	if err != nil {
		t.Fatalf("正常 Anthropic 流不应返回错误: %v", err)
	}
	if ended || terminated {
		t.Fatal("首个内容信号处应返回 ended=false, terminated=false")
	}
	if len(window) != 1 {
		t.Fatalf("窗口应包含 1 个事件(内容增量), 得到 %d 个", len(window))
	}

	// 合法空完成: finish_reason="stop" + [DONE] 但无内容增量
	// 上游声明了白名单内的非空终止原因, 即使无内容也视为合法空完成, 不应拦截
	emptyCompletion := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		&llm.DoneStreamEvent,
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIChatCompletion,
		openai.NewInboundTransformer(),
		emptyCompletion,
	)
	if err != nil {
		t.Fatalf("合法空完成(finish_reason=stop)不应返回错误: %v", err)
	}
	if !ended || !terminated {
		t.Fatal("流已在窗口内耗尽且收到终止事件")
	}
	if len(window) != 3 {
		t.Fatalf("窗口应包含 3 个事件, 得到 %d 个", len(window))
	}

	// 正常 Response 流: output_text.delta + response.completed
	// 内容增量出现后 readStreamWindow 在首个内容信号处提交, 终止事件不进入窗口。
	normalResponse := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"type":"response.output_text.delta","delta":"hi"}`),
		sseEvent(`{"type":"response.completed","response":{"status":"completed"}}`),
	}}
	window, ended, terminated, err = readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIResponse,
		responses.NewInboundTransformer(),
		normalResponse,
	)
	if err != nil {
		t.Fatalf("正常 Response 流不应返回错误: %v", err)
	}
	if ended || terminated {
		t.Fatal("首个内容信号处应返回 ended=false, terminated=false")
	}
	if len(window) != 1 {
		t.Fatalf("窗口应包含 1 个事件(内容增量), 得到 %d 个", len(window))
	}
}

// TestValidChatFinishReasonOther 验证 Bug 3 修复:
// finish_reason "other" 在白名单内, 不再触发 abnormalErr。
func TestValidChatFinishReasonOther(t *testing.T) {
	if !validChatFinishReason("other") {
		t.Fatal("finish_reason \"other\" 应在白名单内")
	}
	// 确保异常值仍在白名单外
	if validChatFinishReason("network_error") {
		t.Fatal("finish_reason \"network_error\" 不应在白名单内")
	}
	if validChatFinishReason("error") {
		t.Fatal("finish_reason \"error\" 不应在白名单内")
	}
	// 确保原有白名单值不受影响
	for _, r := range []string{"", "stop", "length", "tool_calls", "function_call"} {
		if !validChatFinishReason(r) {
			t.Fatalf("原有白名单值 %q 不应被移除", r)
		}
	}
	// content_filter 仍判异常(拒答式终态按设计换目标重试)
	if validChatFinishReason("content_filter") {
		t.Fatal("finish_reason \"content_filter\" 应保持异常(拒答式终态)")
	}
}

// TestReadStreamWindowOtherFinishReasonExempt 验证 Bug 1 × Bug 3 交互:
// finish_reason="other"(Bug 3 新增白名单值) + [DONE] 但无内容增量时,
// 因 verdict.finishSeen=true 豁免早期 EOF 判定, 视为合法空完成交付。
// 该测试守护两个修复的交互路径——如果未来误删 "other" 白名单项,
// Bug 1 的早期 EOF 测试不会捕获该回归, 此测试会。
func TestReadStreamWindowOtherFinishReasonExempt(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
		sseEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"other"}]}`),
		&llm.DoneStreamEvent,
	}}
	window, ended, terminated, err := readStreamWindow(
		context.Background(),
		llm.APIFormatOpenAIChatCompletion,
		openai.NewInboundTransformer(),
		stream,
	)
	if err != nil {
		t.Fatalf("finish_reason=other 的合法空完成不应返回错误: %v", err)
	}
	if !ended || !terminated {
		t.Fatal("流已在窗口内耗尽且收到终止事件")
	}
	if len(window) != 3 {
		t.Fatalf("窗口应包含 3 个事件, 得到 %d 个", len(window))
	}
}
