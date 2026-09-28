package relay

// 渠道测试模拟对应生效协议的下游请求, 覆盖路径、透传判定、日志标签和回复提取。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// testPathOpenAIChatResponse 是一条完整的 OpenAI Chat 非流式响应, 含非零用量与非空回答,
// 供 openai/volcengine 渠道的假上游返回。完全透传渠道也用此响应(extractMessageContent
// 走 choices.0.message.content 路径提取)。
const testPathOpenAIChatResponse = `{"id":"chatcmpl-path","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// testPathAnthropicResponse 是一条完整的 Anthropic Messages 非流式响应, 供 anthropic
// 渠道的假上游返回, 原生文本由 extractMessageContent 提取。
const testPathAnthropicResponse = `{"id":"msg_path","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"m","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// testPathOpenAIResponsesResponse 是一条完整的 OpenAI Responses 非流式响应, 供
// openai_responses 渠道的假上游返回。
const testPathOpenAIResponsesResponse = `{"id":"resp_path","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`

// testPathGeminiResponse 是一条完整的 Gemini 非流式响应, 供 gemini 渠道的假上游返回。
const testPathGeminiResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`

// cleanupProbeRequests 删除 id > beforeID 的所有探针请求, 供测试清理日志流条目,
// 避免探针写入的 RequestState 污染其余测试。
func cleanupProbeRequests(beforeID uint64) {
	mu.Lock()
	defer mu.Unlock()
	for id := range requests {
		if id > beforeID {
			delete(requests, id)
		}
	}
	kept := finishedRequestQueue[:0]
	for _, id := range finishedRequestQueue {
		if id <= beforeID {
			kept = append(kept, id)
		}
	}
	finishedRequestQueue = kept
}

// newTestPathUpstream 构造一个假上游: 记录收到的请求路径到 capturedPath, 并按 responseType
// 返回对应协议的响应体。供路径一致性测试捕获探针实际请求 URL。
func newTestPathUpstream(t *testing.T, capturedPath *string, responseBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
}

// TestSendChannelTestRequestPathConsistency 验证单模型测试探针的上游请求路径与
// 真实转发(客户端协议匹配渠道)一致:
//   - openai 渠道透传 /v1/chat/completions
//   - anthropic 渠道透传 /v1/messages
//   - openai_responses 渠道透传 /v1/responses
//   - gemini 渠道经转换走 generateContent 路径
//   - volcengine 渠道经转换走 chat/completions 路径
func TestSendChannelTestRequestPathConsistency(t *testing.T) {
	tests := []struct {
		name         string
		channelType  model.ChannelProvider
		wantPath     string // 精确断言: 路径等于此值
		pathContains string // 宽松断言: 路径包含此子串
		responseBody string
	}{
		{
			name:         "openai 渠道透传 /v1/chat/completions",
			channelType:  model.ChannelProviderOpenAI,
			wantPath:     "/v1/chat/completions",
			responseBody: testPathOpenAIChatResponse,
		},
		{
			name:         "anthropic 渠道透传 /v1/messages",
			channelType:  model.ChannelProviderAnthropic,
			wantPath:     "/v1/messages",
			responseBody: testPathAnthropicResponse,
		},
		{
			name:         "openai_responses 渠道透传 /v1/responses",
			channelType:  model.ChannelProviderOpenAIResponses,
			wantPath:     "/v1/responses",
			responseBody: testPathOpenAIResponsesResponse,
		},
		{
			name:         "gemini 渠道转换路径包含 generateContent",
			channelType:  model.ChannelProviderGemini,
			pathContains: "generateContent",
			responseBody: testPathGeminiResponse,
		},
		{
			name:         "volcengine 渠道转换路径包含 chat/completions",
			channelType:  model.ChannelProviderVolcengine,
			pathContains: "chat/completions",
			responseBody: testPathOpenAIChatResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedPath string
			upstream := newTestPathUpstream(t, &capturedPath, tt.responseBody)
			defer upstream.Close()

			channel := model.Channel{
				ID:      1,
				Name:    "test-path-" + string(tt.channelType),
				Type:    tt.channelType,
				Enabled: true,
				BaseURL: upstream.URL,
				Key:     "test-key",
				Models:  []model.ChannelModel{{Name: "test-model", Source: model.ChannelModelSourceManual}},
			}

			beforeID := idSeq.Load()
			defer cleanupProbeRequests(beforeID)

			result, err := sendChannelTestRequest(context.Background(), channel, "test-model", "ping", "", testPanelMaxTokens)
			if err != nil {
				t.Fatalf("测试请求失败: %v", err)
			}
			if result.Content != "ok" {
				t.Fatalf("应提取正文, 实际: %+v", result)
			}

			if capturedPath == "" {
				t.Fatalf("探针未发出请求, capturedPath 为空")
			}
			if tt.wantPath != "" && capturedPath != tt.wantPath {
				t.Fatalf("路径不符: 期望 %s, 实际 %s", tt.wantPath, capturedPath)
			}
			if tt.pathContains != "" && !strings.Contains(capturedPath, tt.pathContains) {
				t.Fatalf("路径应包含 %q, 实际 %q", tt.pathContains, capturedPath)
			}
		})
	}
}

// TestBuildOutboundPassthroughConsistency 验证原生入站渠道透传, 其余类型走转换。
func TestBuildOutboundPassthroughConsistency(t *testing.T) {
	tests := []struct {
		name        string
		channelType model.ChannelProvider
		passthrough bool
	}{
		{"openai 渠道透传", model.ChannelProviderOpenAI, true},
		{"anthropic 渠道透传", model.ChannelProviderAnthropic, true},
		{"openai_responses 渠道透传", model.ChannelProviderOpenAIResponses, true},
		{"gemini 渠道转换", model.ChannelProviderGemini, false},
		{"volcengine 渠道转换", model.ChannelProviderVolcengine, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := model.Channel{
				Type:    tt.channelType,
				BaseURL: "http://localhost:12345",
				Key:     "test-key",
			}
			_, passthrough, err := buildOutbound(channel, nil, channelNativeFormat(channel.Type))
			if err != nil {
				t.Fatalf("buildOutbound 失败: %v", err)
			}
			if passthrough != tt.passthrough {
				t.Fatalf("passthrough 判定不符: 期望 %v, 实际 %v", tt.passthrough, passthrough)
			}
		})
	}

	// 完全透传渠道(PassThroughBodyEnabled=true): 任意渠道类型(非 custom)passthrough 均为 true。
	t.Run("完全透传渠道 passthrough 为 true", func(t *testing.T) {
		channel := model.Channel{
			Type:                   model.ChannelProviderAnthropic,
			BaseURL:                "http://localhost:12345",
			Key:                    "test-key",
			PassThroughBodyEnabled: true,
		}
		_, passthrough, err := buildOutbound(channel, nil, channelNativeFormat(channel.Type))
		if err != nil {
			t.Fatalf("buildOutbound 失败: %v", err)
		}
		if !passthrough {
			t.Fatalf("完全透传渠道 passthrough 应为 true, 实际 false")
		}
	})
}

// TestSendChannelTestRequestPassthroughPath 验证完全透传渠道(PassThroughBodyEnabled=true)
// 的探针上游路径沿用模拟下游的原生协议路径。
func TestSendChannelTestRequestPassthroughPath(t *testing.T) {
	var capturedPath string
	upstream := newTestPathUpstream(t, &capturedPath, testPathAnthropicResponse)
	defer upstream.Close()

	channel := model.Channel{
		ID:                     1,
		Name:                   "test-passthrough-anthropic",
		Type:                   model.ChannelProviderAnthropic,
		Enabled:                true,
		BaseURL:                upstream.URL,
		Key:                    "test-key",
		PassThroughBodyEnabled: true,
		Models:                 []model.ChannelModel{{Name: "m", Source: model.ChannelModelSourceManual}},
	}

	beforeID := idSeq.Load()
	defer cleanupProbeRequests(beforeID)

	if _, err := sendChannelTestRequest(context.Background(), channel, "m", "ping", "", testPanelMaxTokens); err != nil {
		t.Fatalf("完全透传测试失败: %v", err)
	}

	if capturedPath != "/v1/messages" {
		t.Fatalf("完全透传渠道探针路径应为 /v1/messages, 实际 %s", capturedPath)
	}
}

// TestSendChannelTestRequestRawURLPath 验证 BaseURL 以 ## 结尾时, 探针 URL 直接使用
// 原始地址, 不追加 /v1 与协议路径, 与真实转发一致。
func TestSendChannelTestRequestRawURLPath(t *testing.T) {
	var capturedPath string
	upstream := newTestPathUpstream(t, &capturedPath, testPathOpenAIChatResponse)
	defer upstream.Close()

	channel := model.Channel{
		ID:      1,
		Name:    "test-raw-url",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		// ## 标记: 地址已完整, 不再追加版本号与协议路径。
		BaseURL: upstream.URL + "/custom/path##",
		Key:     "test-key",
		Models:  []model.ChannelModel{{Name: "m", Source: model.ChannelModelSourceManual}},
	}

	beforeID := idSeq.Load()
	defer cleanupProbeRequests(beforeID)

	_, _ = sendChannelTestRequest(context.Background(), channel, "m", "ping", "", testPanelMaxTokens)

	if capturedPath != "/custom/path" {
		t.Fatalf("## 标记渠道探针路径应为 /custom/path(直接使用原始地址), 实际 %s", capturedPath)
	}
}

// TestSendChannelTestRequestClientFormat 验证 sendChannelTestRequest 触发的
// RequestState.ClientFormat 与渠道协议一致, RelayMode 与实际路径一致。
func TestSendChannelTestRequestClientFormat(t *testing.T) {
	tests := []struct {
		name          string
		channelType   model.ChannelProvider
		wantRelayMode string
		wantFormat    string
		responseBody  string
	}{
		{
			name:          "openai 渠道透传 passthrough",
			channelType:   model.ChannelProviderOpenAI,
			wantRelayMode: "passthrough",
			wantFormat:    "openai_chat",
			responseBody:  testPathOpenAIChatResponse,
		},
		{
			name:          "anthropic 渠道透传 passthrough",
			channelType:   model.ChannelProviderAnthropic,
			wantRelayMode: "passthrough",
			wantFormat:    "anthropic",
			responseBody:  testPathAnthropicResponse,
		},
		{
			name:          "responses 渠道透传 passthrough",
			channelType:   model.ChannelProviderOpenAIResponses,
			wantRelayMode: "passthrough",
			wantFormat:    "openai_responses",
			responseBody:  testPathOpenAIResponsesResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedPath string
			upstream := newTestPathUpstream(t, &capturedPath, tt.responseBody)
			defer upstream.Close()

			channel := model.Channel{
				ID:      1,
				Name:    "test-client-format-" + string(tt.channelType),
				Type:    tt.channelType,
				Enabled: true,
				BaseURL: upstream.URL,
				Key:     "test-key",
				Models:  []model.ChannelModel{{Name: "m", Source: model.ChannelModelSourceManual}},
			}

			beforeID := idSeq.Load()
			defer cleanupProbeRequests(beforeID)

			if _, err := sendChannelTestRequest(context.Background(), channel, "m", "ping", "", testPanelMaxTokens); err != nil {
				t.Fatalf("测试请求失败: %v", err)
			}

			afterID := idSeq.Load()
			mu.Lock()
			var state *RequestState
			for id := beforeID + 1; id <= afterID; id++ {
				if req, ok := requests[id]; ok {
					state = req
					break
				}
			}
			mu.Unlock()

			if state == nil {
				t.Fatalf("日志流应登记探针请求(beforeID=%d, afterID=%d)", beforeID, afterID)
			}
			if state.ClientFormat != tt.wantFormat {
				t.Fatalf("ClientFormat 应为 %s, 实际 %q", tt.wantFormat, state.ClientFormat)
			}
			if state.RelayMode != tt.wantRelayMode {
				t.Fatalf("RelayMode 应为 %q, 实际 %q", tt.wantRelayMode, state.RelayMode)
			}
		})
	}
}

// TestExtractMessageContentProtocolCompatibility 验证 extractMessageContent 兼容
// openai_chat、anthropic、openai_responses 三种协议响应结构, 提取结果非空且正确。
// 覆盖 spec 5.2.1 规则 4 的三条验收条件。
func TestExtractMessageContentProtocolCompatibility(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "openai_chat 字符串 content",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":"走路"},"finish_reason":"stop"}]}`,
			want: "走路",
		},
		{
			name: "openai_chat 分片数组 content 拼接",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"走路"},{"type":"text","text":"坐车"}]}}]}`,
			want: "走路坐车",
		},
		{
			name: "anthropic content.0.text",
			body: `{"id":"msg_x","type":"message","role":"assistant","content":[{"type":"text","text":"走路"}],"model":"m","stop_reason":"end_turn"}`,
			want: "走路",
		},
		{
			name: "openai_responses output.0.content.0.text",
			body: `{"id":"resp_x","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"走路"}]}]}`,
			want: "走路",
		},
		{
			name: "anthropic 跳过开头推理块并拼接全部文本",
			body: `{"content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"第一段"},{"type":"tool_use","name":"tool"},{"type":"text","text":"第二段"}]}`,
			want: "第一段第二段",
		},
		{
			name: "responses 跳过推理工具项并拼接所有消息",
			body: `{"output":[{"type":"reasoning","summary":[]},{"type":"function_call","name":"tool"},{"type":"message","content":[{"type":"output_text","text":"第一段"},{"type":"output_text","text":"第二段"}]},{"type":"message","content":[{"type":"output_text","text":"第三段"}]}]}`,
			want: "第一段第二段第三段",
		},
		{
			name: "三种格式均不命中返回空串",
			body: `{"unknown":"format"}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageContent([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("提取结果不符: 期望 %q, 实际 %q", tt.want, got)
			}
		})
	}
}
