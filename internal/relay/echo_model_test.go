package relay

import (
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestEchoNonStreamResponseModel(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		format    llm.APIFormat
		echoModel string
		wantModel string // 期望改写后的 model 值; 空串表示字段应不存在(未注入)
	}{
		{
			name:      "openai chat 替换 model",
			body:      `{"id":"x","object":"chat.completion","model":"space-bunny-free","choices":[]}`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "auto",
			wantModel: "auto",
		},
		{
			name:      "anthropic 替换顶层 model",
			body:      `{"id":"m","type":"message","role":"assistant","model":"space-bunny-free","content":[],"stop_reason":"end_turn"}`,
			format:    llm.APIFormatAnthropicMessage,
			echoModel: "auto",
			wantModel: "auto",
		},
		{
			name:      "responses 替换顶层 model",
			body:      `{"id":"r","object":"response","model":"space-bunny-free","output":[],"status":"completed"}`,
			format:    llm.APIFormatOpenAIResponse,
			echoModel: "auto",
			wantModel: "auto",
		},
		{
			name:      "embedding 替换 model",
			body:      `{"object":"list","data":[{"embedding":[0.1],"index":0}],"model":"space-bunny-free"}`,
			format:    llm.APIFormatOpenAIEmbedding,
			echoModel: "auto",
			wantModel: "auto",
		},
		{
			name:      "图片格式不带 model 字段不改写",
			body:      `{"created":0,"data":[{"url":"http://x"}]}`,
			format:    llm.APIFormatOpenAIImageGeneration,
			echoModel: "auto",
			wantModel: "",
		},
		{
			name:      "空 echoModel 原样返回",
			body:      `{"model":"space-bunny-free"}`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "",
			wantModel: "space-bunny-free",
		},
		{
			name:      "响应体无 model 字段不注入",
			body:      `{"choices":[]}`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "auto",
			wantModel: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := echoNonStreamResponseModel([]byte(tt.body), tt.format, tt.echoModel)
			got := gjson.GetBytes(out, "model")
			if tt.wantModel == "" {
				assert.False(t, got.Exists(), "不应注入 model 字段, 实际: %s", out)
			} else {
				assert.True(t, got.Exists(), "应存在 model 字段, 实际: %s", out)
				assert.Equal(t, tt.wantModel, got.String())
			}
		})
	}
}

func TestEchoStreamResponseModel(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		format    llm.APIFormat
		echoModel string
		path      string // 期望被改写的字段路径; 空串表示不应改写任何字段
		want      string // 改写后该路径的期望值; path 为空时此字段无意义
	}{
		{
			name:      "openai chat chunk 替换 model",
			data:      `{"id":"x","object":"chat.completion.chunk","model":"space-bunny-free","choices":[{"delta":{"content":"hi"}}]}`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "auto",
			path:      "model",
			want:      "auto",
		},
		{
			name:      "openai chat [DONE] 不改写",
			data:      `[DONE]`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "auto",
			path:      "",
		},
		{
			name:      "anthropic message_start 替换 message.model",
			data:      `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"space-bunny-free","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			format:    llm.APIFormatAnthropicMessage,
			echoModel: "auto",
			path:      "message.model",
			want:      "auto",
		},
		{
			name:      "anthropic content_block_delta 无 model 不改写",
			data:      `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			format:    llm.APIFormatAnthropicMessage,
			echoModel: "auto",
			path:      "",
		},
		{
			name:      "responses response.created 替换 response.model",
			data:      `{"type":"response.created","response":{"id":"r","object":"response","status":"in_progress","model":"space-bunny-free"}}`,
			format:    llm.APIFormatOpenAIResponse,
			echoModel: "auto",
			path:      "response.model",
			want:      "auto",
		},
		{
			name:      "responses output_text.delta 无 response.model 不改写",
			data:      `{"type":"response.output_text.delta","delta":"hi"}`,
			format:    llm.APIFormatOpenAIResponse,
			echoModel: "auto",
			path:      "",
		},
		{
			name:      "图片格式不改写",
			data:      `{"model":"space-bunny-free"}`,
			format:    llm.APIFormatOpenAIImageGeneration,
			echoModel: "auto",
			path:      "",
		},
		{
			name:      "空 echoModel 原样返回",
			data:      `{"model":"space-bunny-free"}`,
			format:    llm.APIFormatOpenAIChatCompletion,
			echoModel: "",
			path:      "model",
			want:      "space-bunny-free",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := echoStreamResponseModel([]byte(tt.data), tt.format, tt.echoModel)
			if tt.path == "" {
				// 不改写: 输出应与输入一致
				assert.Equal(t, tt.data, string(out))
				return
			}
			got := gjson.GetBytes(out, tt.path)
			assert.True(t, got.Exists(), "应存在字段 %s, 实际: %s", tt.path, out)
			assert.Equal(t, tt.want, got.String())
		})
	}
}
