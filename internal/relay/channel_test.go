package relay

// 渠道模型限制注入测试: OpenAI 系渠道按 thinking_level 写入或删除 reasoning_effort,
// off 必须移除转换层(如 Anthropic 自适应思考)预先写入的取值, 而不是仅跳过注入。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestApplyChannelModelLimitsReasoningEffort(t *testing.T) {
	maxOut := 1024
	tests := []struct {
		name          string
		limits        map[string]model.ChannelModelLimit
		body          string
		wantEffort    string // 期望的 reasoning_effort 取值, 空串表示字段必须不存在
		wantMaxTokens int
	}{
		{
			name:       "off 删除转换层写入的 reasoning_effort",
			limits:     map[string]model.ChannelModelLimit{"test-model": {MaxOutput: &maxOut, ThinkingLevel: "off"}},
			body:       `{"model":"test-model","reasoning_effort":"xhigh","messages":[]}`,
			wantEffort: "",
		},
		{
			name:          "off 同时保留 max_tokens 注入",
			limits:        map[string]model.ChannelModelLimit{"test-model": {MaxOutput: &maxOut, ThinkingLevel: "off"}},
			body:          `{"model":"test-model","reasoning_effort":"max","max_tokens":99,"messages":[]}`,
			wantEffort:    "",
			wantMaxTokens: maxOut,
		},
		{
			name:       "合法等级覆盖既有取值",
			limits:     map[string]model.ChannelModelLimit{"test-model": {ThinkingLevel: "high"}},
			body:       `{"model":"test-model","reasoning_effort":"xhigh","messages":[]}`,
			wantEffort: "high",
		},
		{
			name:       "未配置等级时不动 reasoning_effort",
			limits:     map[string]model.ChannelModelLimit{"other-model": {ThinkingLevel: "max"}},
			body:       `{"model":"test-model","reasoning_effort":"xhigh","messages":[]}`,
			wantEffort: "xhigh",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			channel := model.Channel{Type: model.ChannelProviderOpenAI, ModelLimits: tc.limits}
			request := &httpclient.Request{Body: []byte(tc.body)}
			require.NoError(t, applyChannelModelLimits(channel, request))

			got := gjson.GetBytes(request.Body, "reasoning_effort")
			if tc.wantEffort == "" {
				assert.False(t, got.Exists(), "reasoning_effort 应被删除, 实际: %s", request.Body)
			} else {
				require.True(t, got.Exists(), "reasoning_effort 应存在, 实际: %s", request.Body)
				assert.Equal(t, tc.wantEffort, got.String())
			}
			if tc.wantMaxTokens > 0 {
				assert.Equal(t, float64(tc.wantMaxTokens), gjson.GetBytes(request.Body, "max_tokens").Float())
			}
		})
	}
}

func TestUpstreamProtocolSelection(t *testing.T) {
	opencode := model.Channel{
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://opencode.ai/zen",
		Key:            "public",
		OpencodeCompat: true,
	}
	tests := []struct {
		name        string
		channel     model.Channel
		protocol    string
		format      llm.APIFormat
		passthrough bool
		apiFormat   llm.APIFormat
		label       string
	}{
		{
			name:        "anthropic 协议且客户端为 Messages 时透传",
			channel:     opencode,
			protocol:    model.UpstreamProtocolAnthropic,
			format:      llm.APIFormatAnthropicMessage,
			passthrough: true,
			apiFormat:   llm.APIFormatAnthropicMessage,
			label:       "anthropic",
		},
		{
			name:        "anthropic 协议且客户端为 Chat 时走 Anthropic 转换器",
			channel:     opencode,
			protocol:    model.UpstreamProtocolAnthropic,
			format:      llm.APIFormatOpenAIChatCompletion,
			passthrough: false,
			apiFormat:   llm.APIFormatAnthropicMessage,
			label:       "anthropic",
		},
		{
			name:        "responses 协议且客户端为 Responses 时透传",
			channel:     opencode,
			protocol:    model.UpstreamProtocolResponses,
			format:      llm.APIFormatOpenAIResponse,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIResponse,
			label:       "openai_responses",
		},
		{
			name:        "responses 协议且客户端为 Chat 时走 Responses 转换器",
			channel:     opencode,
			protocol:    model.UpstreamProtocolResponses,
			format:      llm.APIFormatOpenAIChatCompletion,
			passthrough: false,
			apiFormat:   llm.APIFormatOpenAIResponse,
			label:       "openai_responses",
		},
		{
			name:        "chat 协议保持 OpenAI 对话透传",
			channel:     opencode,
			protocol:    model.UpstreamProtocolChat,
			format:      llm.APIFormatOpenAIChatCompletion,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
		{
			name:        "chat 协议下图片仍按 OpenAI 原生格式透传",
			channel:     opencode,
			protocol:    model.UpstreamProtocolChat,
			format:      llm.APIFormatOpenAIImageGeneration,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
		{
			name:        "空协议的 OpenCode 模型与 openai 渠道矩阵一致",
			channel:     opencode,
			protocol:    "",
			format:      llm.APIFormatOpenAIEmbedding,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
		{
			name:        "空协议不把 Responses 当成原生格式",
			channel:     opencode,
			protocol:    "",
			format:      llm.APIFormatOpenAIResponse,
			passthrough: false,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
		{
			name: "非 OpenCode 渠道忽略模型协议",
			channel: model.Channel{
				Type:    model.ChannelProviderOpenAI,
				BaseURL: "https://api.openai.com",
				Key:     "sk-test",
			},
			protocol:    model.UpstreamProtocolAnthropic,
			format:      llm.APIFormatOpenAIChatCompletion,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
		{
			name: "自定义渠道即使写了协议也忽略",
			channel: model.Channel{
				Type:           model.ChannelProviderCustom,
				OpencodeCompat: true,
				FixedReply:     "ok",
			},
			protocol:    model.UpstreamProtocolAnthropic,
			format:      llm.APIFormatAnthropicMessage,
			passthrough: false,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "自定义固定回复",
		},
		{
			name: "完全透传优先于模型协议",
			channel: model.Channel{
				Type:                   model.ChannelProviderOpenAI,
				BaseURL:                "https://opencode.ai/zen",
				Key:                    "public",
				OpencodeCompat:         true,
				PassThroughBodyEnabled: true,
			},
			protocol:    model.UpstreamProtocolAnthropic,
			format:      llm.APIFormatOpenAIChatCompletion,
			passthrough: true,
			apiFormat:   llm.APIFormatOpenAIChatCompletion,
			label:       "openai_chat",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			channelModel := &model.ChannelModel{Name: "same-model", UpstreamProtocol: tc.protocol}
			outbound, passthrough, err := buildOutbound(tc.channel, channelModel, tc.format)
			require.NoError(t, err)
			assert.Equal(t, tc.passthrough, passthrough)
			assert.Equal(t, tc.passthrough, supportsNativeFormat(tc.channel, channelModel, tc.format))
			require.NotNil(t, outbound)
			assert.Equal(t, tc.apiFormat, outbound.APIFormat())
			assert.Equal(t, tc.label, upstreamTypeLabel(tc.channel, channelModel))
		})
	}

	t.Run("同一模型名在两个档上可以选择不同协议", func(t *testing.T) {
		free := opencode
		paid := opencode
		paid.BaseURL = "https://opencode.ai/zen/go"
		freeModel := &model.ChannelModel{Name: "same-model", UpstreamProtocol: model.UpstreamProtocolChat}
		paidModel := &model.ChannelModel{Name: "same-model", UpstreamProtocol: model.UpstreamProtocolAnthropic}
		_, freePass, err := buildOutbound(free, freeModel, llm.APIFormatAnthropicMessage)
		require.NoError(t, err)
		_, paidPass, err := buildOutbound(paid, paidModel, llm.APIFormatAnthropicMessage)
		require.NoError(t, err)
		assert.False(t, freePass)
		assert.True(t, paidPass)
		assert.Equal(t, "openai_chat", upstreamTypeLabel(free, freeModel))
		assert.Equal(t, "anthropic", upstreamTypeLabel(paid, paidModel))
	})
}

func TestOpencodeBaseURLVersionPrefix(t *testing.T) {
	bases := []string{"https://opencode.ai/zen", "https://opencode.ai/zen/go"}
	cases := []struct {
		protocol string
		format   llm.APIFormat
		suffix   string
		native   bool
	}{
		{model.UpstreamProtocolChat, llm.APIFormatOpenAIChatCompletion, "/chat/completions", true},
		{model.UpstreamProtocolResponses, llm.APIFormatOpenAIResponse, "/responses", true},
		{model.UpstreamProtocolAnthropic, llm.APIFormatAnthropicMessage, "/messages", true},
		{model.UpstreamProtocolChat, llm.APIFormatOpenAIResponse, "/chat/completions", false},
		{model.UpstreamProtocolResponses, llm.APIFormatOpenAIChatCompletion, "/responses", false},
		{model.UpstreamProtocolAnthropic, llm.APIFormatOpenAIChatCompletion, "/messages", false},
	}
	for _, base := range bases {
		for _, tc := range cases {
			t.Run(base+" "+tc.protocol+" "+tc.suffix, func(t *testing.T) {
				channel := model.Channel{
					Type:           model.ChannelProviderOpenAI,
					BaseURL:        base,
					Key:            "public",
					OpencodeCompat: true,
				}
				channelModel := &model.ChannelModel{Name: "m", UpstreamProtocol: tc.protocol}
				outbound, passthrough, err := buildOutbound(channel, channelModel, tc.format)
				require.NoError(t, err)
				require.Equal(t, tc.native, passthrough)

				var got string
				if tc.native {
					raw := &httpclient.Request{
						Method:  http.MethodPost,
						Headers: http.Header{"Content-Type": []string{"application/json"}},
						Body:    []byte(`{"model":"m"}`),
					}
					req, err := buildPassthroughRequest(tc.format, raw, channel, "")
					require.NoError(t, err)
					got = req.URL
				} else {
					text := "hi"
					llmReq := &llm.Request{
						Model: "m",
						Messages: []llm.Message{{
							Role:    "user",
							Content: llm.MessageContent{Content: &text},
						}},
					}
					req, err := outbound.TransformRequest(context.Background(), llmReq)
					require.NoError(t, err)
					got = req.URL
				}
				want := strings.TrimRight(base, "/") + "/v1" + tc.suffix
				assert.Equal(t, want, got)
				assert.NotContains(t, got, "/v1/v1")
				assert.NotContains(t, got, "opencode.ai/v1/")
			})
		}
	}
}
