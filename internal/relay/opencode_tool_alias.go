package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ocToolAliasPrefix 免费档出站客户端自定义工具统一前缀。
// 后接名字摘要, 免费层按客户端词汇拒绝请求。
// 参考 opencode2api 2026-10-24 fd4c1c6d 的同名方案。
const ocToolAliasPrefix = "clint_"

// ocToolDigestLen 是出站名携带的名字摘要 hex 位数。
const ocToolDigestLen = 12

// ocToolWireName 由客户端名派生不透明出站名。
// 摘要跨请求稳定, 原始名不出站。
func ocToolWireName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return ocToolAliasPrefix + hex.EncodeToString(sum[:])[:ocToolDigestLen]
}

// ocToolAliasWhitelist 是优先占位的客户端工具名, 按客户端分组维护。
// 每个请求只携带单一客户端的工具集, 跨组重名(如 read/Agent)对匹配无影响。
// 只保留核心工具; 与官方同名的条目(read/write/edit 等)不列——它们在官方
// 名字集合内, 本就不会作为自定义工具改名。无名额上限后仅决定数组内顺序。
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
//   - 客户端自定义工具改为摘要出站名, 定义原样保留;
//   - 官方全集(体检模板的完整工具定义, 逐字节官方)替换/补齐工具数组前段;
//   - 自定义工具白名单优先排序, 全部摘要改名进数组, 不设数量上限;
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
	clientItems := make([]string, 0, len(tools.Array()))
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
	// 白名单优先排序, 全部自定义工具摘要改名进数组。
	ordered := append(whitelisted, rest...)
	for _, t := range ordered {
		item, ok := ocToolRename(t.Raw, taken, aliases)
		if !ok {
			return body, nil
		}
		clientItems = append(clientItems, item)
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

// ocToolRename 把单个客户端工具改为摘要出站名并登记别名。
// 冲突时追加下划线消解。
func ocToolRename(raw string, taken map[string]bool, aliases map[string]string) (string, bool) {
	name := gjson.Get(raw, "function.name").String()
	renamed := ocToolWireName(name)
	for taken[renamed] {
		renamed += "_"
	}
	taken[renamed] = true
	next, err := sjson.SetBytes([]byte(raw), "function.name", renamed)
	if err != nil {
		return "", false
	}
	aliases[renamed] = name
	return string(next), true
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
