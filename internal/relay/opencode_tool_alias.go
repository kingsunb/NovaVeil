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

// ocOfficialToolCandidates 是官方工具名到客户端等价名的候选, 按优先级排列。
var ocOfficialToolCandidates = map[string][]string{
	"bash":      []string{"Bash", "bash", "pwsh"},
	"shell":     []string{"Bash", "bash", "pwsh", "shell"},
	"edit":      []string{"Edit", "edit"},
	"read":      []string{"Read", "read"},
	"write":     []string{"Write", "write"},
	"glob":      []string{"Glob", "glob"},
	"grep":      []string{"Grep", "grep"},
	"question":  []string{"AskUserQuestion", "ask_user_question"},
	"task":      []string{"Agent", "Task", "task", "subagent"},
	"subagent":  []string{"Agent", "Task", "subagent"},
	"todowrite": []string{"TodoWrite", "todo_write", "todo"},
	"webfetch":  []string{"WebFetch", "web_fetch", "fetch_content"},
	"websearch": []string{"WebSearch", "web_search"},
	"skill":     []string{"Skill", "skill"},
}

// ocOfficialToolTarget 保存客户端声明名及其参数白名单。
type ocOfficialToolTarget struct {
	clientName string
	schema     gjson.Result
	properties map[string]struct{}
	required   map[string]struct{}
	hasSchema  bool
}

// buildOfficialToolTargets 从已转成 Chat 的客户端工具声明建官方名映射。
// 只登记客户端确实声明的等价名; 未声明的官方名保持透传。
func buildOfficialToolTargets(body []byte) map[string]ocOfficialToolTarget {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}
	client := make(map[string]ocOfficialToolTarget)
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		name := tool.Get("function.name").String()
		if name == "" || ocToolIsWireName(name) {
			continue
		}
		schema := tool.Get("function.parameters")
		target := ocOfficialToolTarget{clientName: name, schema: schema}
		if schema.IsObject() {
			target.hasSchema = true
			target.properties = make(map[string]struct{})
			schema.Get("properties").ForEach(func(key, _ gjson.Result) bool {
				target.properties[key.String()] = struct{}{}
				return true
			})
			target.required = make(map[string]struct{})
			for _, required := range schema.Get("required").Array() {
				target.required[required.String()] = struct{}{}
			}
		}
		client[name] = target
	}
	if len(client) == 0 {
		return nil
	}
	targets := make(map[string]ocOfficialToolTarget)
	for official, candidates := range ocOfficialToolCandidates {
		for _, candidate := range candidates {
			if target, ok := client[candidate]; ok {
				targets[official] = target
				break
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return targets
}

const ocWebFetchDefaultPrompt = "Return the page content."

// ocOfficialArgumentAliases 是官方参数键到客户端常见键的候选。
var ocOfficialArgumentAliases = map[string][]string{
	"filePath":   []string{"file_path", "path"},
	"path":       []string{"file_path", "filePath"},
	"oldString":  []string{"old_string"},
	"newString":  []string{"new_string"},
	"replaceAll": []string{"replace_all"},
	"include":    []string{"glob"},
	"background": []string{"run_in_background"},
	"agent":      []string{"subagent_type"},
	"name":       []string{"skill"},
	"id":         []string{"skill"},
	"command":    []string{"script", "cmd"},
	"workdir":    []string{"cwd"},
}

// adaptOfficialToolArguments 按客户端 schema 改参数键并递归丢弃未声明键。
func adaptOfficialToolArguments(official, args string, target ocOfficialToolTarget) string {
	// 无 properties 时无从判定白名单, 裁剪会清空全部入参。
	if !target.hasSchema || len(target.properties) == 0 {
		return args
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	parsed := gjson.Parse(args)
	if !parsed.IsObject() {
		return args
	}
	out := make(map[string]json.RawMessage)
	parsed.ForEach(func(key, value gjson.Result) bool {
		mapped := ocMapOfficialArgument(key.String(), target.properties)
		if mapped == "" {
			return true
		}
		property := target.schema.Get("properties." + ocSJSONPath(mapped))
		pruned, ok := ocPruneOfficialValue(value, property)
		if !ok {
			return true
		}
		out[mapped] = pruned
		return true
	})
	if official == "webfetch" {
		if _, required := target.required["prompt"]; required {
			if _, exists := out["prompt"]; !exists {
				prompt, err := json.Marshal(ocWebFetchDefaultPrompt)
				if err == nil {
					out["prompt"] = json.RawMessage(prompt)
				}
			}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return args
	}
	return string(data)
}

func ocPruneOfficialValue(value, schema gjson.Result) (json.RawMessage, bool) {
	if value.Raw == "" {
		return nil, false
	}
	if !schema.Exists() {
		return json.RawMessage(value.Raw), true
	}
	kind := schema.Get("type").String()
	if kind == "" {
		if schema.Get("properties").Exists() {
			kind = "object"
		} else if schema.Get("items").Exists() {
			kind = "array"
		}
	}
	switch kind {
	case "object":
		properties := schema.Get("properties")
		if !value.IsObject() || !properties.IsObject() {
			return json.RawMessage(value.Raw), true
		}
		out := make(map[string]json.RawMessage)
		value.ForEach(func(key, nested gjson.Result) bool {
			property := properties.Get(ocSJSONPath(key.String()))
			if !property.Exists() {
				return true
			}
			pruned, ok := ocPruneOfficialValue(nested, property)
			if ok {
				out[key.String()] = pruned
			}
			return true
		})
		data, err := json.Marshal(out)
		if err != nil {
			return json.RawMessage(value.Raw), true
		}
		return data, true
	case "array":
		items := schema.Get("items")
		if !value.IsArray() || !items.Exists() {
			return json.RawMessage(value.Raw), true
		}
		out := make([]json.RawMessage, 0, len(value.Array()))
		for _, item := range value.Array() {
			pruned, ok := ocPruneOfficialValue(item, items)
			if ok {
				out = append(out, pruned)
			}
		}
		data, err := json.Marshal(out)
		if err != nil {
			return json.RawMessage(value.Raw), true
		}
		return data, true
	default:
		return json.RawMessage(value.Raw), true
	}
}

func ocMapOfficialArgument(key string, properties map[string]struct{}) string {
	if _, ok := properties[key]; ok {
		return key
	}
	for _, alias := range ocOfficialArgumentAliases[key] {
		if _, ok := properties[alias]; ok {
			return alias
		}
	}
	return ""
}

func ocSJSONPath(key string) string {
	key = strings.ReplaceAll(key, `\`, `\\`)
	return strings.ReplaceAll(key, ".", `\.`)
}

// restoreOfficialToolCallsInBody 把 Chat 非流式官方工具名与参数改为客户端形态。
func restoreOfficialToolCallsInBody(body []byte, targets map[string]ocOfficialToolTarget) []byte {
	if len(targets) == 0 || len(body) == 0 {
		return body
	}
	out := body
	for i, choice := range gjson.GetBytes(out, "choices").Array() {
		for j, call := range choice.Get("message.tool_calls").Array() {
			function := call.Get("function")
			official := function.Get("name").String()
			target, ok := targets[official]
			if !ok || target.clientName == "" {
				continue
			}
			base := fmt.Sprintf("choices.%d.message.tool_calls.%d.function", i, j)
			if args := function.Get("arguments").String(); args != "" && target.hasSchema {
				next, err := sjson.SetBytes(out, base+".arguments", adaptOfficialToolArguments(official, args, target))
				if err != nil {
					continue
				}
				out = next
			}
			next, err := sjson.SetBytes(out, base+".name", target.clientName)
			if err != nil {
				continue
			}
			out = next
		}
	}
	return out
}
