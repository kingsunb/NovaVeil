package relay

import (
	"bytes"
	"strings"

	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// opencodeAliasKeywords 是默认替换为 "opencode" 的客户端身份关键词。
// 向上游转发前替换, 响应回来后在内容字段自动还原。
var opencodeAliasKeywords = []string{"DeepSeek Harness", "codex", "claude code", "pi agent"}

const opencodeAliasReplacement = "opencode"

// opencodeAliasState 记录本轮请求命中的首个关键词, 供响应还原。
type opencodeAliasState struct {
	keyword string
}

// applyOpencodeAlias 把请求体中的客户端身份关键词替换为 "opencode"。
// 未命中任何关键词时返回 nil state, 调用方跳过响应还原。
func applyOpencodeAlias(body []byte) ([]byte, *opencodeAliasState) {
	var found string
	for _, kw := range opencodeAliasKeywords {
		if bytes.Contains(body, []byte(kw)) {
			body = bytes.ReplaceAll(body, []byte(kw), []byte(opencodeAliasReplacement))
			if found == "" {
				found = kw
			}
		}
	}
	if found == "" {
		return body, nil
	}
	return body, &opencodeAliasState{keyword: found}
}

// restoreOpencodeAliasNonStream 在非流式响应内容字段把 "opencode" 还原为原关键词。
// 仅替换正文/推理字段, 不触碰模型名等结构字段(model 可能含 "opencode/latest/2.2.1/cli")。
func restoreOpencodeAliasNonStream(body []byte, format llm.APIFormat, state *opencodeAliasState) []byte {
	if state == nil || !bytes.Contains(body, []byte(opencodeAliasReplacement)) {
		return body
	}
	kw := state.keyword
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		body = restoreAliasChatNonStream(body, kw)
	case llm.APIFormatAnthropicMessage:
		body = restoreAliasAnthropicNonStream(body, kw)
	case llm.APIFormatOpenAIResponse:
		body = restoreAliasResponsesNonStream(body, kw)
	}
	return body
}

// restoreOpencodeAliasStream 在流式 SSE 事件内容字段把 "opencode" 还原为原关键词。
// 逐事件替换, 不做跨 chunk 缓冲("opencode" 单词被拆分跨 chunk 的概率极低)。
func restoreOpencodeAliasStream(data []byte, format llm.APIFormat, state *opencodeAliasState) []byte {
	if state == nil {
		return data
	}
	// content delta
	if path := streamContentPath(data, format); path != "" {
		data = replaceAliasInField(data, path, state.keyword)
	}
	// reasoning delta
	if rcPath := streamReasoningPath(data, format); rcPath != "" {
		data = replaceAliasInField(data, rcPath, state.keyword)
	}
	return data
}

// replaceAliasInField 把指定 gjson 路径的字符串值中 "opencode" 替换为 kw, 写回 data。
func replaceAliasInField(data []byte, path, kw string) []byte {
	val := gjson.GetBytes(data, path)
	if val.Type != gjson.String {
		return data
	}
	s := val.String()
	if !strings.Contains(s, opencodeAliasReplacement) {
		return data
	}
	out, _ := sjson.SetBytes(data, path, strings.ReplaceAll(s, opencodeAliasReplacement, kw))
	return out
}

// restoreAliasChatNonStream 还原 OpenAI Chat 非流式响应中的别名。
// content 可为 string 或 content-block 数组; reasoning_content 为 string。
func restoreAliasChatNonStream(body []byte, kw string) []byte {
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}
	for i := range choices.Array() {
		base := "choices." + itoa(i) + ".message"
		// content: string 或 array
		cPath := base + ".content"
		c := gjson.GetBytes(body, cPath)
		if c.Type == gjson.String {
			body = replaceAliasInField(body, cPath, kw)
		} else if c.IsArray() {
			for j := range c.Array() {
				body = replaceAliasInField(body, cPath+"."+itoa(j)+".text", kw)
			}
		}
		// reasoning_content
		body = replaceAliasInField(body, base+".reasoning_content", kw)
	}
	return body
}

// restoreAliasAnthropicNonStream 还原 Anthropic 非流式响应中的别名。
// content 为 content-block 数组, 每块含 text 字段。
func restoreAliasAnthropicNonStream(body []byte, kw string) []byte {
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() {
		return body
	}
	for i := range content.Array() {
		body = replaceAliasInField(body, "content."+itoa(i)+".text", kw)
	}
	return body
}

// restoreAliasResponsesNonStream 还原 OpenAI Responses 非流式响应中的别名。
// output 为数组, 每项含 content 数组, 每块含 text 字段。
func restoreAliasResponsesNonStream(body []byte, kw string) []byte {
	output := gjson.GetBytes(body, "output")
	if !output.IsArray() {
		return body
	}
	for i := range output.Array() {
		cPath := "output." + itoa(i) + ".content"
		c := gjson.GetBytes(body, cPath)
		if !c.IsArray() {
			continue
		}
		for j := range c.Array() {
			body = replaceAliasInField(body, cPath+"."+itoa(j)+".text", kw)
		}
	}
	return body
}
