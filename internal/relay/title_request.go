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

// errTitleRequestRejected 分组策略拒绝标题类请求的终态。
var errTitleRequestRejected = errors.New("title generation request rejected by group policy")

// isTitleGenerationRequest 判定是否为客户端生成的标题类轻请求。
// 三个条件同时成立: 消息不超过两条、未声明工具、提示词含标题标记。
// 免费档整形会注入官方系统提示与工具, 故必须用客户端原始正文判定。
func isTitleGenerationRequest(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 || len(items) > titleRequestMaxMessages {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return false
	}
	for _, msg := range items {
		if containsTitleMarker(messageText(msg)) {
			return true
		}
	}
	return false
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

// containsTitleMarker 报告文本是否含任一标题标记。
func containsTitleMarker(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range titleRequestMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
