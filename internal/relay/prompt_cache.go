package relay

import (
	"bytes"

	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// maxPromptCacheKeyBytes 是回写 prompt_cache_key 的字节上限。
// 超限、空串或非 JSON 字符串直接忽略, 不截断。
const maxPromptCacheKeyBytes = 256

// promptCacheKeyFromClient 从已经脱敏的客户端 JSON 读取 prompt_cache_key。
// 只接受非空 JSON 字符串, 且解码后的字节数不超过 maxPromptCacheKeyBytes。
// 调用方不得传入脱敏前原文。本函数不记录该值。
func promptCacheKeyFromClient(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	value := gjson.GetBytes(body, "prompt_cache_key")
	if value.Type != gjson.String {
		return ""
	}
	if value.Str == "" || len(value.Str) > maxPromptCacheKeyBytes {
		return ""
	}
	return value.Str
}

// restorePromptCacheKey 在转换完成、出站 JSON 还没有 prompt_cache_key 时把客户端原字符串写回去。
// 目标只限 OpenAI Chat 与 Responses。Anthropic 及其他协议不新增该字段, 也不映射成 cache_control。
// key 为空、出站不是 JSON 对象、或字段已经存在时原样返回。不写日志。
func restorePromptCacheKey(outbound []byte, key string, target llm.APIFormat) []byte {
	if key == "" {
		return outbound
	}
	switch target {
	case llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponse:
	default:
		return outbound
	}
	trimmed := bytes.TrimSpace(outbound)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return outbound
	}
	if gjson.GetBytes(outbound, "prompt_cache_key").Exists() {
		return outbound
	}
	next, err := sjson.SetBytes(outbound, "prompt_cache_key", key)
	if err != nil {
		return outbound
	}
	return next
}
