package relay

import (
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// echoNonStreamResponseModel 把非流式响应体里的模型名改写为客户端请求的分组名,
// 避免聚合分组把真实上游模型名(如 space-bunny-free)透传给下游。仅在分组开启
// EchoResponseModel 时由 handler 调用。OpenAI Chat / Anthropic Messages /
// OpenAI Responses / Embedding 的非流式响应均用顶层 model 字段; 不含 model 字段的
// 响应体(图片/音频等)原样返回, 不凭空注入 model。
func echoNonStreamResponseModel(body []byte, format llm.APIFormat, echoModel string) []byte {
	if len(body) == 0 || echoModel == "" {
		return body
	}
	switch format {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatAnthropicMessage,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIEmbedding:
	default:
		return body
	}
	if !gjson.GetBytes(body, "model").Exists() {
		return body
	}
	out, err := sjson.SetBytes(body, "model", echoModel)
	if err != nil {
		return body
	}
	return out
}

// echoStreamResponseModel 把流式 SSE 事件正文里的模型名改写为客户端请求的分组名。
//   - OpenAI Chat: 每个 chunk 的顶层 model;
//   - Anthropic: message_start 事件里的 message.model;
//   - Responses: response.* 事件里的 response.model;
//   - 其余格式(图片/音频; Embedding 不流式)原样返回。
//
// 只改写已存在的字段, 不为没有 model 的事件(如 [DONE]/ping/content_block_delta)注入。
func echoStreamResponseModel(data []byte, format llm.APIFormat, echoModel string) []byte {
	if len(data) == 0 || echoModel == "" {
		return data
	}
	var path string
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		path = "model"
	case llm.APIFormatAnthropicMessage:
		path = "message.model"
	case llm.APIFormatOpenAIResponse:
		path = "response.model"
	default:
		return data
	}
	if !gjson.GetBytes(data, path).Exists() {
		return data
	}
	out, err := sjson.SetBytes(data, path, echoModel)
	if err != nil {
		return data
	}
	return out
}
