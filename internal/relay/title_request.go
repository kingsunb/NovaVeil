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
}

// 指令型标记, 系统角色命中即判定
var titleInstructionMarkers = []string{
	"you are a title generator",
	"output only a thread title",
	"generate a title for this conversation",
	"generate the session title",
	"create a concise title",
}

// errTitleRequestRejected 分组策略拒绝标题类请求的终态。
var errTitleRequestRejected = errors.New("title generation request rejected by group policy")

// isTitleGenerationRequest 判定是否为客户端生成的标题类轻请求。
// 前置条件: messages 非空且客户端未声明工具。
// 系统角色的指令型标记直接成立, 泛用标记需消息不超过两条。
// 免费档整形会注入官方系统提示与工具, 故必须用客户端原始正文判定。
func isTitleGenerationRequest(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return false
	}
	light := len(items) <= titleRequestMaxMessages
	for _, msg := range items {
		text := messageText(msg)
		// 系统提示声明生成标题, 不看条数
		if isInstructionRole(msg.Get("role").String()) && containsTitleMarker(text, titleInstructionMarkers) {
			return true
		}
		if light && containsTitleMarker(text, titleRequestMarkers) {
			return true
		}
	}
	return false
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
