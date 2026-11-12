package mask

import (
	"encoding/json"
	"strconv"
)

// RestoreString 把 body 中的占位符还原为原文(非流式, 一次性全量替换)。
// 未登记的占位符原样保留: 绝不做模糊匹配、不猜、不推算。
// 模型自造的占位符从未登记, 还原它在信息论上不可能, 正确行为是原样保留让用户可见。
func RestoreString(body string, mapping *Mapping) string {
	if mapping == nil || body == "" {
		return body
	}
	return PlaceholderRe.ReplaceAllStringFunc(body, func(token string) string {
		orig, ok := mapping.Lookup(token)
		if !ok {
			return token
		}
		return orig
	})
}

// RestoreBytes 是 RestoreString 的 []byte 封装。
func RestoreBytes(body []byte, mapping *Mapping) []byte {
	return []byte(RestoreString(string(body), mapping))
}

// RestoreJSONBytes 对合法 JSON 响应按字符串值还原, 自动转义原文中的引号、反斜杠与换行。
// 解码后无占位符时原样返回; 非 JSON 输入回退整段文本替换, 兼容纯文本与二进制响应。
func RestoreJSONBytes(body []byte, mapping *Mapping) []byte {
	if mapping == nil || len(body) == 0 {
		return body
	}
	if !json.Valid(body) {
		return RestoreBytes(body, mapping)
	}
	changed := false
	restored := mapJSONStringValues(string(body), func(s string) string {
		out := RestoreString(s, mapping)
		if out != s {
			changed = true
		}
		return out
	})
	if !changed {
		return body // 无占位符时保持原字节
	}
	return []byte(restored)
}

// RestoreJSONString 用于还原 JSON 字符串值内部的占位符(如工具调用 arguments 字段)。
// 还原出的原文需做 JSON 字符串转义, 否则原文含引号/反斜杠会破坏客户端工具参数解析。
// 用 strconv.Quote 转义后剥去首尾引号, 得到可嵌入 JSON 字符串的转义内容。
func RestoreJSONString(s string, mapping *Mapping) string {
	if mapping == nil || s == "" {
		return s
	}
	return PlaceholderRe.ReplaceAllStringFunc(s, func(token string) string {
		orig, ok := mapping.Lookup(token)
		if !ok {
			return token
		}
		q := strconv.Quote(orig)
		return q[1 : len(q)-1] // 剥首尾引号
	})
}
