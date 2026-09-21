package relay

// 本文件验证渠道模型测试探针的路径决定与真实转发(客户端发 openai_chat)一致:
//   - 5.1 各渠道类型探针上游请求路径与真实转发路径一致
//   - 5.2 buildOutbound 的 passthrough 判定与真实转发一致
//   - 5.3 完全透传渠道沿用客户端原始路径; ## 标记渠道直接使用原始地址
//   - 5.5 日志流 client_format 固定为 openai_chat, relay_mode 与实际路径一致
//   - 5.6 extractMessageContent 兼容三种协议响应结构
//
// 探针以 testProbeClientFormat(= openai_chat) 作为代表客户端协议, 与真实转发在
// 客户端发 openai_chat 时的路径决定逻辑对齐。转换路径(anthropic/openai_responses/
// gemini/volcengine)的响应格式由对应出站转换器决定, 即使响应校验失败, 请求已发出,
// 路径已被捕获, 断言仍可成立。

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
// 渠道的假上游返回。转换路径经 inbound 转回 openai_chat 后由 extractMessageContent 提取。
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
// 真实转发(客户端发 openai_chat)一致:
//   - openai 渠道透传 /v1/chat/completions
//   - anthropic 渠道经转换走 /v1/messages
//   - openai_responses 渠道经转换走 /v1/responses
//   - gemini 渠道经转换走 generateContent 路径
//   - volcengine 渠道经转换走 chat/completions 路径
//
// 转换路径(anthropic/openai_responses/gemini/volcengine)即使响应校验失败,
// 请求已发出, 路径已被捕获, 断言仍可成立。
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
			name:         "anthropic 渠道转换 /v1/messages",
			channelType:  model.ChannelProviderAnthropic,
			wantPath:     "/v1/messages",
			responseBody: testPathAnthropicResponse,
		},
		{
			name:         "openai_responses 渠道转换 /v1/responses",
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

			// 调用 sendChannelTestRequest, 忽略 error: 转换路径可能因响应格式不完全匹配
			// 而校验失败, 但请求已发出, 路径已被捕获。
			_, _ = sendChannelTestRequest(context.Background(), channel, "test-model", "ping", "", testPanelMaxTokens)

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

// TestBuildOutboundPassthroughConsistency 验证 buildOutbound(channel, testProbeClientFormat)
// 返回的 passthrough 值与真实转发(客户端发 openai_chat)一致:
//   - openai 渠道与完全透传渠道为 true
//   - anthropic / openai_responses / gemini / volcengine 渠道为 false
func TestBuildOutboundPassthroughConsistency(t *testing.T) {
	tests := []struct {
		name        string
		channelType model.ChannelProvider
		passthrough bool
	}{
		{"openai 渠道透传", model.ChannelProviderOpenAI, true},
		{"anthropic 渠道转换", model.ChannelProviderAnthropic, false},
		{"openai_responses 渠道转换", model.ChannelProviderOpenAIResponses, false},
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
			_, passthrough, err := buildOutbound(channel, nil, testProbeClientFormat)
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
		_, passthrough, err := buildOutbound(channel, nil, testProbeClientFormat)
		if err != nil {
			t.Fatalf("buildOutbound 失败: %v", err)
		}
		if !passthrough {
			t.Fatalf("完全透传渠道 passthrough 应为 true, 实际 false")
		}
	})
}

// TestSendChannelTestRequestPassthroughPath 验证完全透传渠道(PassThroughBodyEnabled=true)
// 的探针上游路径沿用客户端原始路径 /v1/chat/completions, 而非回退到渠道原生格式路径。
//
// 构造 PassThroughBodyEnabled=true 的 anthropic 渠道: 修复前探针 format=anthropic,
// raw.Path 为空, 走透传时回退到 /v1/messages; 修复后 format=openai_chat, raw.Path
// 设为 /v1/chat/completions, 完全透传时 TrimPrefix 得到 /chat/completions, 最终
// 路径为 /v1/chat/completions, 与真实转发一致。
func TestSendChannelTestRequestPassthroughPath(t *testing.T) {
	var capturedPath string
	upstream := newTestPathUpstream(t, &capturedPath, testPathOpenAIChatResponse)
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

	_, _ = sendChannelTestRequest(context.Background(), channel, "m", "ping", "", testPanelMaxTokens)

	if capturedPath != "/v1/chat/completions" {
		t.Fatalf("完全透传渠道探针路径应为 /v1/chat/completions(沿用客户端原始路径), 实际 %s", capturedPath)
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
// recordTestRequest 写入的 RequestState.ClientFormat 为 openai_chat, RelayMode
// 与实际所走路径一致。覆盖 openai(透传)与 anthropic(转换)两种渠道。
func TestSendChannelTestRequestClientFormat(t *testing.T) {
	tests := []struct {
		name          string
		channelType   model.ChannelProvider
		wantRelayMode string
		responseBody  string
	}{
		{
			name:          "openai 渠道透传 passthrough",
			channelType:   model.ChannelProviderOpenAI,
			wantRelayMode: "passthrough",
			responseBody:  testPathOpenAIChatResponse,
		},
		{
			name:          "anthropic 渠道转换 converted",
			channelType:   model.ChannelProviderAnthropic,
			wantRelayMode: "converted",
			responseBody:  testPathAnthropicResponse,
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

			// 无论探针成败, recordTestRequest 都会写入日志流。
			_, _ = sendChannelTestRequest(context.Background(), channel, "m", "ping", "", testPanelMaxTokens)

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
			if state.ClientFormat != "openai_chat" {
				t.Fatalf("ClientFormat 应为 openai_chat, 实际 %q", state.ClientFormat)
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
