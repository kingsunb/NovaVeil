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

// ocToolAliasMaxTools 是随 tools 数组出站的自定义工具数量上限。
// 白名单优先占位, 余下名额按客户端顺序补足; 超出部分转文本注入 system。
// 上游对工具总量敏感: 官方全集 40 + 自定义 30 可过, 再多即失败, 预留 3 个余量。
const ocToolAliasMaxTools = 33

// ocToolAliasWhitelist 是优先占位的客户端工具名(按功能重要性挑选)。
var ocToolAliasWhitelist = map[string]bool{
	"pwsh": true, "read": true, "write": true, "edit": true,
	"glob": true, "grep": true, "read_image": true,

	"acp_cache": true, "acp_read_original_image": true, "acp_status": true,
	"compress": true, "decompress": true, "search_context": true,

	"subagent": true, "subagent_fork": true, "send_message": true,
	"list_agents": true, "interrupt_agent": true,

	"job_list": true, "job_output": true, "job_kill": true,

	"workflow": true,

	"create_goal": true, "get_goal": true, "update_goal": true, "todo_write": true,

	"web_fetch": true, "web_search": true,

	"ask_user_question": true, "exit_plan_mode": true, "present": true,
	"skill": true, "working_directory": true,
}

// ensureOfficialToolset 把免费档正文的工具集规范成官方会话形态(lane 取 v1/v2):
//   - 客户端自定义工具(非官方核心名)改名为 clint_ 前缀的无冲突名, 定义原样保留;
//   - 官方全集(体检模板的完整工具定义, 逐字节官方)替换/补齐工具数组前段;
//   - 自定义工具白名单优先占位, 补足到上限, 其余渲染成文本追加 system;
//   - tool_choice 与历史 tool_calls 中的客户端名同步改写。
//
// 返回改写后正文与别名表(出站名 → 客户端名); 无需改写时原样返回空表。
func ensureOfficialToolset(body []byte, lane string) ([]byte, map[string]string) {
	templates, err := loadOcProbeTemplates()
	if err != nil || templates[lane] == nil {
		return body, nil
	}
	official := templates[lane].Tools
	officialNames := make(map[string]bool)
	for _, t := range gjson.ParseBytes(official).Array() {
		officialNames[t.Get("function.name").String()] = true
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, nil
	}
	aliases := make(map[string]string)
	clientItems := make([]string, 0, ocToolAliasMaxTools)
	overflow := make([]string, 0)
	taken := make(map[string]bool, len(tools.Array()))
	presentTools := make(map[string]bool, len(officialNames))
	var whitelisted, rest []gjson.Result
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" || officialNames[name] {
			presentTools[name] = true
			continue
		}
		if ocToolAliasWhitelist[name] {
			whitelisted = append(whitelisted, t)
			continue
		}
		rest = append(rest, t)
	}
	// 白名单优先, 余下名额按客户端顺序补足到上限。
	ordered := append(whitelisted, rest...)
	for i, t := range ordered {
		if i >= ocToolAliasMaxTools {
			overflow = append(overflow, t.Raw)
			continue
		}
		name := t.Get("function.name").String()
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
	if len(aliases) == 0 && len(overflow) == 0 && tools.Exists() {
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
	if len(overflow) > 0 {
		out = appendExtraToolsNote(out, overflow)
	}
	return out, aliases
}

// appendExtraToolsNote 把超出上限的客户端工具渲染成文本追加到 system 消息。
// 这些工具不在 tools 数组内, 模型无法发起结构化 tool_calls, 只能作为能力说明保留。
func appendExtraToolsNote(body []byte, tools []string) []byte {
	content := gjson.GetBytes(body, "messages.0.content")
	if content.Type != gjson.String {
		return body
	}
	var b strings.Builder
	b.WriteString("\n\n<additional_tools>\n")
	b.WriteString("The following tools are not available as function calls. ")
	b.WriteString(`If one is needed, reply with a JSON object: {"tool": "<name>", "arguments": {...}}`)
	for _, raw := range tools {
		t := gjson.Parse(raw)
		b.WriteString("\n\n- name: ")
		b.WriteString(t.Get("function.name").String())
		if d := t.Get("function.description").String(); d != "" {
			b.WriteString("\n  description: ")
			b.WriteString(d)
		}
		if p := t.Get("function.parameters"); p.Exists() {
			b.WriteString("\n  parameters: ")
			b.WriteString(p.Raw)
		}
	}
	b.WriteString("\n</additional_tools>")
	next, err := sjson.SetBytes(body, "messages.0.content", content.String()+b.String())
	if err != nil {
		return body
	}
	return next
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
