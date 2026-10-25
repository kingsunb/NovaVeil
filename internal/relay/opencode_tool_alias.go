package relay

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ocToolAliasPrefix 是免费档 v2 出站时客户端自定义工具的统一改名前缀:
// 上游按官方工具集形态校验, 客户端自带名与定义留在出站正文里会被识别为
// 非官方会话; 改名后出站工具集 = 官方全集 + clint_ 前缀的客户端工具,
// 响应侧按别名表还原回客户端声明名(未映射名透传)。
// 参考 opencode2api 2026-10-24 fd4c1c6d 的同名方案。
const ocToolAliasPrefix = "clint_"

// ensureOfficialV2Toolset 把 v2 免费档正文的工具集规范成官方会话形态:
//   - 客户端自定义工具(非官方核心名)改名为 clint_ 前缀的无冲突名, 定义原样保留;
//   - 官方全集(体检模板的完整工具定义, 逐字节官方)替换/补齐工具数组前段;
//   - tool_choice 与历史 tool_calls 中的客户端名同步改写。
//
// 返回改写后正文与别名表(出站名 → 客户端名); 无需改写时原样返回空表。
func ensureOfficialV2Toolset(body []byte) ([]byte, map[string]string) {
	templates, err := loadOcProbeTemplates()
	if err != nil || templates["v2"] == nil {
		return body, nil
	}
	official := templates["v2"].Tools
	officialNames := make(map[string]bool)
	for _, t := range gjson.ParseBytes(official).Array() {
		officialNames[t.Get("function.name").String()] = true
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, nil
	}
	aliases := make(map[string]string)
	clientItems := make([]string, 0, len(tools.Array()))
	taken := make(map[string]bool, len(tools.Array()))
	presentTools := make(map[string]bool, len(officialNames))
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" || officialNames[name] {
			presentTools[name] = true
			continue
		}
		renamed := ocToolAliasPrefix + sanitizeToolAliasName(name)
		for taken[renamed] {
			renamed += "_"
		}
		taken[renamed] = true
		next, err := sjson.SetBytes([]byte(t.Raw), "function.name", renamed)
		if err != nil {
			return body, nil
		}
		aliases[renamed] = name
		clientItems = append(clientItems, string(next))
	}
	if len(aliases) == 0 && tools.Exists() {
		missing := false
		for name := range officialNames {
			if !presentTools[name] {
				missing = true
				break
			}
		}
		if !missing {
			// 已是官方全集且无自定义工具, 幂等跳过。
			return body, nil
		}
	}
	// official 是裸数组 "[...]": 剥掉外层括号后与客户端工具平级拼接。
	inner := strings.TrimSpace(string(official))
	inner = strings.TrimPrefix(strings.TrimSuffix(inner, "]"), "[")
	items := make([]string, 0, len(clientItems)+1)
	items = append(items, inner)
	items = append(items, clientItems...)
	out, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, nil
	}
	out = rewriteToolReferences(out, aliases)
	return out, aliases
}

// sanitizeToolAliasName 把客户端工具名规范为出站名的安全字符段。
func sanitizeToolAliasName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" {
		return "tool"
	}
	return s
}

// rewriteToolReferences 把 tool_choice 与历史 tool_calls 里的客户端工具名换成出站别名。
func rewriteToolReferences(body []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 {
		return body
	}
	// 别名表是出站名→客户端名; 请求侧改写需要反查客户端名→出站名。
	reverse := make(map[string]string, len(aliases))
	for outbound, client := range aliases {
		reverse[client] = outbound
	}
	out := body
	if name := gjson.GetBytes(out, "tool_choice.function.name").String(); name != "" {
		if renamed, ok := reverse[name]; ok {
			if next, err := sjson.SetBytes(out, "tool_choice.function.name", renamed); err == nil {
				out = next
			}
		}
	}
	messages := gjson.GetBytes(out, "messages")
	if !messages.IsArray() {
		return out
	}
	for i, msg := range messages.Array() {
		calls := msg.Get("tool_calls")
		if !calls.IsArray() {
			continue
		}
		for j, call := range calls.Array() {
			name := call.Get("function.name").String()
			renamed, ok := reverse[name]
			if !ok {
				continue
			}
			path := fmt.Sprintf("messages.%d.tool_calls.%d.function.name", i, j)
			if next, err := sjson.SetBytes(out, path, renamed); err == nil {
				out = next
			}
		}
	}
	return out
}

// restoreToolAliasesInBody 把非流式正文里的出站工具别名还原为客户端声明名。
func restoreToolAliasesInBody(body []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 {
		return body
	}
	out := body
	choices := gjson.GetBytes(out, "choices")
	for i, choice := range choices.Array() {
		calls := choice.Get("message.tool_calls")
		if !calls.IsArray() {
			continue
		}
		for j, call := range calls.Array() {
			name := call.Get("function.name").String()
			client, ok := aliases[name]
			if !ok {
				continue
			}
			path := fmt.Sprintf("choices.%d.message.tool_calls.%d.function.name", i, j)
			if next, err := sjson.SetBytes(out, path, client); err == nil {
				out = next
			}
		}
	}
	return out
}

// restoreToolAliasesInEvent 把流式事件增量里的出站工具别名还原为客户端声明名。
func restoreToolAliasesInEvent(data []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 || len(data) == 0 {
		return data
	}
	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() {
		return data
	}
	out := data
	for i, choice := range choices.Array() {
		calls := choice.Get("delta.tool_calls")
		if !calls.IsArray() {
			continue
		}
		for j, call := range calls.Array() {
			name := call.Get("function.name").String()
			client, ok := aliases[name]
			if !ok {
				continue
			}
			path := fmt.Sprintf("choices.%d.delta.tool_calls.%d.function.name", i, j)
			if next, err := sjson.SetBytes(out, path, client); err == nil {
				out = next
			}
		}
	}
	return out
}
