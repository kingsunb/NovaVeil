package relay

// OnInboundLlmRequest 补齐 AxonHub llm 19a3c27 引入的 DefaultMaxTokens 语义:
// 只有 Anthropic 中转且客户端未显式指定输出上限时, 才从渠道模型卡输出上限取值。

import (
	"context"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestOnInboundLlmRequestDefaultMaxTokens(t *testing.T) {
	maxOut := 4096
	zero := 0

	tests := []struct {
		name           string
		format         llm.APIFormat
		limits         map[string]model.ChannelModelLimit
		requestModel   string
		existingTokens *int64
		wantTokens     *int64
	}{
		{
			name:         "Anthropic 中转且模型卡配置输出上限时写入",
			format:       llm.APIFormatAnthropicMessage,
			limits:       map[string]model.ChannelModelLimit{"claude-a": {MaxOutput: &maxOut}},
			requestModel: "claude-a",
			wantTokens:   toInt64Ptr(maxOut),
		},
		{
			name:           "客户端已显式指定变换层默认值时保持原值",
			format:         llm.APIFormatAnthropicMessage,
			limits:         map[string]model.ChannelModelLimit{"claude-a": {MaxOutput: &maxOut}},
			requestModel:   "claude-a",
			existingTokens: toInt64Ptr(1024),
			wantTokens:     toInt64Ptr(1024),
		},
		{
			name:         "非 Anthropic 上游不做改写",
			format:       llm.APIFormatOpenAIChatCompletion,
			limits:       map[string]model.ChannelModelLimit{"gpt-a": {MaxOutput: &maxOut}},
			requestModel: "gpt-a",
			wantTokens:   nil,
		},
		{
			name:         "模型未配置输出上限时保持空",
			format:       llm.APIFormatAnthropicMessage,
			limits:       map[string]model.ChannelModelLimit{"claude-a": {ThinkingLevel: "high"}},
			requestModel: "claude-a",
			wantTokens:   nil,
		},
		{
			name:         "输出上限为 0 视为未配置",
			format:       llm.APIFormatAnthropicMessage,
			limits:       map[string]model.ChannelModelLimit{"claude-a": {MaxOutput: &zero}},
			requestModel: "claude-a",
			wantTokens:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &conversionMiddleware{
				format:  tt.format,
				channel: model.Channel{ModelLimits: tt.limits},
			}
			req := &llm.Request{Model: tt.requestModel}
			if tt.existingTokens != nil {
				req.TransformOptions.DefaultMaxTokens = tt.existingTokens
			}

			got, err := m.OnInboundLlmRequest(context.Background(), req)
			require.NoError(t, err)
			require.Same(t, req, got)

			if tt.wantTokens == nil {
				assert.Nil(t, req.TransformOptions.DefaultMaxTokens)
				return
			}
			require.NotNil(t, req.TransformOptions.DefaultMaxTokens)
			assert.Equal(t, *tt.wantTokens, *req.TransformOptions.DefaultMaxTokens)
		})
	}
}

func toInt64Ptr(v int) *int64 {
	out := int64(v)
	return &out
}
