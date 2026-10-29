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
// 实测总数 62 稳过, 63 抖动, 64 起必拒; 23 对应总数 63。
const ocToolAliasMaxTools = 23

// extraToolDescLimit 是溢出工具描述的最大字符数, 控制文本块体积。
const extraToolDescLimit = 80

// extraToolCompactThreshold 是切换精简渲染的溢出数量阈值。
// 少量溢出保留完整 schema(模型能构造正确 arguments); 大量溢出(Pi 类上百工具)只列
// 名称与参数名, 避免 system 膨胀上百 KB。
const extraToolCompactThreshold = 30

// ocToolAliasWhitelist 是优先占位的客户端工具名, 按客户端分组维护。
// 每个请求只携带单一客户端的工具集, 跨组重名(如 read/Agent)对匹配无影响。
// 只保留核心工具; 与官方同名的条目(read/write/edit 等)不列——它们在官方
// 名字集合内, 本就不会作为自定义工具改名。被裁条目按普通自定义参与顺序
// 补位或溢出转文本。
var ocToolAliasWhitelist = map[string]bool{
	// DSH(核心 13)
	"pwsh": true, "read_image": true,
	"subagent": true, "subagent_fork": true, "send_message": true,
	"list_agents": true, "interrupt_agent": true,
	"todo_write": true, "web_fetch": true, "web_search": true,
	"ask_user_question": true, "exit_plan_mode": true, "working_directory": true,

	// LiveAgent(核心 17, 管理器/浏览器类转溢出)
	"Image": true, "Write": true, "Edit": true, "Delete": true, "List": true,
	"Glob": true, "Grep": true, "Bash": true, "ProcessWait": true, "ProcessStop": true,
	"ManagedProcess": true, "SendMessage": true, "TaskCreate": true, "TaskUpdate": true,
	"TaskList": true, "AskUserQuestion": true, "Agent": true,

	// Codex(核心 9, TUI 线程管理与 MCP 资源类转溢出)
	"exec_command": true, "write_stdin": true, "request_user_input": true,
	"view_image": true,
	"multi_agent_v1__close_agent": true, "multi_agent_v1__resume_agent": true,
	"multi_agent_v1__send_input": true, "multi_agent_v1__spawn_agent": true,
	"multi_agent_v1__wait_agent": true,

	// Claude Code(核心 11, Cron/报表类转溢出)
	"EnterPlanMode": true, "ExitPlanMode": true, "EnterWorktree": true, "ExitWorktree": true,
	"ListAgents": true, "NotebookEdit": true, "TaskStop": true,
	"WebFetch": true, "WebSearch": true, "Workflow": true, "Skill": true,

	// Pi(核心 9, MCP 工具集不占名额)
	"bash": true, "find": true, "ls": true, "todo": true,
	"plan_mode_question": true, "plan_mode_complete": true,
	"source_check": true, "fetch_content": true, "get_search_content": true,
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
// 溢出数量不超过 extraToolCompactThreshold 时保留完整 schema(模型能构造正确参数);
// 超过时转为精简格式(描述截断 + 参数名), 防止 Pi 这类上百 MCP 工具的客户端撑爆 system。
func appendExtraToolsNote(body []byte, tools []string) []byte {
	content := gjson.GetBytes(body, "messages.0.content")
	if content.Type != gjson.String {
		return body
	}
	var b strings.Builder
	b.WriteString("\n\n<additional_tools>\n")
	b.WriteString("The following tools are not available as function calls. ")
	b.WriteString(`If one is needed, reply with a JSON object: {"tool": "<name>", "arguments": {...}}`)
	compact := len(tools) > extraToolCompactThreshold
	for _, raw := range tools {
		t := gjson.Parse(raw)
		if !compact {
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
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(t.Get("function.name").String())
		if d := t.Get("function.description").String(); d != "" {
			d = strings.ReplaceAll(d, "\n", " ")
			if r := []rune(d); len(r) > extraToolDescLimit {
				d = string(r[:extraToolDescLimit]) + "…"
			}
			b.WriteString(": ")
			b.WriteString(d)
		}
		var keys []string
		t.Get("function.parameters.properties").ForEach(func(k, _ gjson.Result) bool {
			keys = append(keys, k.String())
			return true
		})
		if len(keys) > 0 {
			b.WriteString(" [params: ")
			b.WriteString(strings.Join(keys, ", "))
			b.WriteString("]")
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
