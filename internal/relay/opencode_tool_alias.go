package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
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

// 白名单仅决定客户端工具顺序。
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

// 同名非官方定义仅改名隔离。
func ensureOfficialToolset(body []byte, lane string) ([]byte, map[string]string) {
	templates, err := loadOcProbeTemplates()
	if err != nil || templates[lane] == nil {
		return body, nil
	}
	official := templates[lane].Tools
	officialTools := make(map[string]gjson.Result)
	taken := make(map[string]bool)
	for _, tool := range gjson.ParseBytes(official).Array() {
		name := tool.Get("function.name").String()
		officialTools[name] = tool
		taken[name] = true
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, nil
	}
	for _, tool := range tools.Array() {
		if name := tool.Get("function.name").String(); name != "" {
			taken[name] = true
		}
	}
	presentTools := make(map[string]bool, len(officialTools))
	duplicateOfficial := false
	var whitelisted, rest []gjson.Result
	for _, tool := range tools.Array() {
		name := tool.Get("function.name").String()
		if exact, exists := officialTools[name]; exists && ocToolDefinitionsEqual(tool.Raw, exact.Raw) {
			duplicateOfficial = duplicateOfficial || presentTools[name]
			presentTools[name] = true
			continue
		}
		if ocToolAliasWhitelist[name] {
			whitelisted = append(whitelisted, tool)
		} else {
			rest = append(rest, tool)
		}
	}
	aliases := make(map[string]string)
	clientItems := make([]string, 0, len(whitelisted)+len(rest))
	for _, tool := range append(whitelisted, rest...) {
		name := tool.Get("function.name")
		if tool.Get("type").String() != "function" || name.Type != gjson.String || name.String() == "" || ocToolIsWireName(name.String()) {
			clientItems = append(clientItems, tool.Raw)
			continue
		}
		item, ok := ocToolRename(tool.Raw, taken, aliases)
		if !ok {
			return body, nil
		}
		clientItems = append(clientItems, item)
	}
	if len(aliases) == 0 && len(presentTools) == len(officialTools) && !duplicateOfficial {
		return body, nil
	}
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

func ocToolDefinitionsEqual(left, right string) bool {
	var a, b any
	decode := func(raw string, target *any) error {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	if decode(left, &a) != nil || decode(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func ocToolIsWireName(name string) bool {
	if !strings.HasPrefix(name, ocToolAliasPrefix) {
		return false
	}
	digest := strings.TrimPrefix(name, ocToolAliasPrefix)
	if len(digest) < ocToolDigestLen || strings.Trim(digest[ocToolDigestLen:], "_") != "" {
		return false
	}
	hexPart := digest[:ocToolDigestLen]
	_, err := hex.DecodeString(hexPart)
	return err == nil && strings.ToLower(hexPart) == hexPart
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

func rewriteToolReferences(body []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 {
		return body
	}
	reverse := make(map[string]string, len(aliases))
	for outbound, client := range aliases {
		if prior, exists := reverse[client]; !exists || outbound < prior {
			reverse[client] = outbound
		}
	}
	out := rewriteToolNameAt(body, "tool_choice.function.name", reverse)
	choice := gjson.GetBytes(out, "tool_choice")
	switch choice.Get("type").String() {
	case "function", "custom", "tool":
		out = rewriteToolNameAt(out, "tool_choice.name", reverse)
	case "allowed_tools":
		for i, tool := range choice.Get("tools").Array() {
			if kind := tool.Get("type").String(); kind == "function" || kind == "custom" {
				out = rewriteToolNameAt(out, fmt.Sprintf("tool_choice.tools.%d.name", i), reverse)
			}
		}
		for i, tool := range choice.Get("allowed_tools.tools").Array() {
			if tool.Get("type").String() == "function" {
				out = rewriteToolNameAt(out, fmt.Sprintf("tool_choice.allowed_tools.tools.%d.function.name", i), reverse)
			}
		}
	}
	for i, msg := range gjson.GetBytes(out, "messages").Array() {
		for j := range msg.Get("tool_calls").Array() {
			out = rewriteToolNameAt(out, fmt.Sprintf("messages.%d.tool_calls.%d.function.name", i, j), reverse)
		}
		for j, block := range msg.Get("content").Array() {
			if block.Get("type").String() == "tool_use" {
				out = rewriteToolNameAt(out, fmt.Sprintf("messages.%d.content.%d.name", i, j), reverse)
			}
		}
		if role := msg.Get("role").String(); role == "tool" || role == "function" {
			out = rewriteToolNameAt(out, fmt.Sprintf("messages.%d.name", i), reverse)
		}
	}
	for i, item := range gjson.GetBytes(out, "input").Array() {
		if isOpencodeToolCall(item) {
			out = rewriteToolNameAt(out, fmt.Sprintf("input.%d.name", i), reverse)
		}
	}
	return out
}

func rewriteToolNameAt(body []byte, path string, names map[string]string) []byte {
	name := gjson.GetBytes(body, path)
	if name.Type != gjson.String {
		return body
	}
	replacement, ok := names[name.String()]
	if !ok || replacement == name.String() {
		return body
	}
	next, err := sjson.SetBytes(body, path, replacement)
	if err != nil {
		return body
	}
	return next
}

func isOpencodeToolCall(item gjson.Result) bool {
	switch item.Get("type").String() {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func restoreToolAliasesInBody(body []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 {
		return body
	}
	out := body
	for i, choice := range gjson.GetBytes(out, "choices").Array() {
		for j := range choice.Get("message.tool_calls").Array() {
			out = rewriteToolNameAt(out, fmt.Sprintf("choices.%d.message.tool_calls.%d.function.name", i, j), aliases)
		}
	}
	for i, block := range gjson.GetBytes(out, "content").Array() {
		if block.Get("type").String() == "tool_use" {
			out = rewriteToolNameAt(out, fmt.Sprintf("content.%d.name", i), aliases)
		}
	}
	for i, item := range gjson.GetBytes(out, "output").Array() {
		if isOpencodeToolCall(item) {
			out = rewriteToolNameAt(out, fmt.Sprintf("output.%d.name", i), aliases)
		}
	}
	return out
}

func restoreToolAliasesInEvent(data []byte, aliases map[string]string) []byte {
	if len(aliases) == 0 || len(data) == 0 {
		return data
	}
	out := restoreToolAliasesInBody(data, aliases)
	for i, choice := range gjson.GetBytes(out, "choices").Array() {
		for j := range choice.Get("delta.tool_calls").Array() {
			out = rewriteToolNameAt(out, fmt.Sprintf("choices.%d.delta.tool_calls.%d.function.name", i, j), aliases)
		}
	}
	if gjson.GetBytes(out, "content_block.type").String() == "tool_use" {
		out = rewriteToolNameAt(out, "content_block.name", aliases)
	}
	if isOpencodeToolCall(gjson.GetBytes(out, "item")) {
		out = rewriteToolNameAt(out, "item.name", aliases)
	}
	for i, item := range gjson.GetBytes(out, "response.output").Array() {
		if isOpencodeToolCall(item) {
			out = rewriteToolNameAt(out, fmt.Sprintf("response.output.%d.name", i), aliases)
		}
	}
	return out
}
