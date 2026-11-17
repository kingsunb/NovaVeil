package relay

import (
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// 标题类请求最多两条消息
const titleRequestMaxMessages = 2

// 标题提示词标记, 小写匹配
var titleRequestMarkers = []string{
	"session title",
	"concise title",
	"title generator",
	"you name conversations",
}

// 指令型标记, 系统角色命中即判定
var titleInstructionMarkers = []string{
	"you are a title generator",
	"output only a thread title",
	"generate a title for this conversation",
	"generate the session title",
	"create a concise title",
	"you generate a descriptive title",
	"reply with a title and nothing else",
	"reply with a title for it and nothing else",
}

// errTitleRequestRejected 分组策略拒绝标题类请求的终态。
var errTitleRequestRejected = errors.New("title generation request rejected by group policy")

// titleRequestMessage 是归一化后的标题判定输入。
type titleRequestMessage struct {
	role string
	text string
}

// isTitleGenerationRequest 判定是否为客户端生成的标题类轻请求。
// 前置条件: 消息非空且客户端未声明工具。
// 系统角色的指令型标记直接成立, 泛用标记需消息不超过两条。
// 免费档整形会注入官方系统提示与工具, 故必须用客户端原始正文判定。
func isTitleGenerationRequest(body []byte) bool {
	items, ok := titleRequestMessages(body)
	if !ok || len(items) == 0 {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return false
	}
	// 已出现对话轮次说明是 agent 流量, 非全新标题请求
	for _, item := range items {
		switch item.role {
		case "assistant", "tool", "function":
			return false
		}
	}
	light := len(items) <= titleRequestMaxMessages
	for _, item := range items {
		// 系统提示声明生成标题, 不看条数
		if isInstructionRole(item.role) && containsTitleMarker(item.text, titleInstructionMarkers) {
			return true
		}
		if light && containsTitleMarker(item.text, titleRequestMarkers) {
			return true
		}
	}
	return false
}

// titleRequestMessages 归一化 Chat messages 与 Responses input。
// Responses 的标题指令可能在 instructions, 一并纳入判定。
func titleRequestMessages(body []byte) ([]titleRequestMessage, bool) {
	if messages := gjson.GetBytes(body, "messages"); messages.IsArray() {
		out := make([]titleRequestMessage, 0, len(messages.Array()))
		for _, message := range messages.Array() {
			out = append(out, titleRequestMessage{role: message.Get("role").String(), text: messageText(message)})
		}
		return out, true
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil, false
	}
	out := make([]titleRequestMessage, 0, len(input.Array())+1)
	if instructions := gjson.GetBytes(body, "instructions"); instructions.Type == gjson.String && instructions.String() != "" {
		out = append(out, titleRequestMessage{role: "system", text: instructions.String()})
	}
	for _, item := range input.Array() {
		out = append(out, titleRequestMessage{role: item.Get("role").String(), text: messageText(item)})
	}
	return out, true
}

// isInstructionRole 报告角色是否承载系统级指令。
func isInstructionRole(role string) bool {
	return role == "system" || role == "developer"
}

// messageText 拼接单条消息文本, 兼容字符串与 parts 两种正文。
func messageText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var builder strings.Builder
	for _, part := range content.Array() {
		if text := part.Get("text").String(); text != "" {
			builder.WriteString(text)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// containsTitleMarker 报告文本是否含给定标记之一。
func containsTitleMarker(text string, markers []string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
