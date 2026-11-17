package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"

	"github.com/tidwall/gjson"
)

// ocExpectWireName 测试自算摘要名, 不复用生产实现。
func ocExpectWireName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return ocToolAliasPrefix + hex.EncodeToString(sum[:])[:ocToolDigestLen]
}

// TestEnsureOfficialV2ToolsetRenamesAndMerges 锁定 v2 出站工具形态: 客户端
// 自定义工具改名前缀+摘要的不透明名, 官方全集在前, 别名表齐全。
func TestEnsureOfficialV2ToolsetRenamesAndMerges(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"my_tool.x","description":"c","parameters":{"type":"object","properties":{}}}}]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	wire := ocExpectWireName("my_tool.x")
	if aliases[wire] != "my_tool.x" {
		t.Fatalf("别名表异常: %v", aliases)
	}
	names := make([]string, 0, 16)
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	templates, err := loadOcProbeTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var officialCount int
	for range gjson.ParseBytes(templates["v2"].Tools).Array() {
		officialCount++
	}
	if len(names) != officialCount+1 || names[officialCount] != wire {
		t.Fatalf("出站应为官方全集+%s, 实际 %v", wire, names)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got == "my_tool.x" {
		t.Fatal("客户端名不应出现在出站")
	}
}

// TestEnsureOfficialV2ToolsetRewritesToolCalls 锁定 tool_choice 与历史 tool_calls 的同步改名。
func TestEnsureOfficialV2ToolsetRewritesToolCalls(t *testing.T) {
	wire := ocExpectWireName("patcher")
	body := []byte(`{"model":"m","tool_choice":{"type":"function","function":{"name":"patcher"}},
		"tools":[{"type":"function","function":{"name":"patcher","description":"c","parameters":{"type":"object","properties":{}}}}],
		"messages":[{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"patcher","arguments":"{}"}}]}]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	if aliases[wire] != "patcher" {
		t.Fatalf("别名表异常: %v", aliases)
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != wire {
		t.Fatalf("tool_choice 未改名: %q", got)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String(); got != wire {
		t.Fatalf("历史 tool_calls 未改名: %q", got)
	}
}

// TestEnsureOfficialToolsetNoClientVocabularyLeak 锁定出站零词汇泄漏:
// 客户端原始工具名不得出现在 tools 数组与 system 文本, 全部进数组。
func TestEnsureOfficialToolsetNoClientVocabularyLeak(t *testing.T) {
	var raws []string
	names := []string{"ReportFindings", "ScheduleWakeup"}
	for i := 0; i < 25; i++ {
		names = append(names, fmt.Sprintf("leak_probe_%02d", i))
	}
	for _, name := range names {
		raws = append(raws, fmt.Sprintf(`{"type":"function","function":{"name":%q,"description":"d","parameters":{"type":"object","properties":{}}}}`, name))
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` + strings.Join(raws, ",") + `]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	if len(aliases) != len(names) {
		t.Fatalf("别名表应覆盖全部客户端工具: %d != %d", len(aliases), len(names))
	}
	toolsRaw := gjson.GetBytes(out, "tools").Raw
	system := gjson.GetBytes(out, "messages.0.content").String()
	array := gjson.GetBytes(out, "tools").Array()
	templates, err := loadOcProbeTemplates()
	if err != nil {
		t.Fatal(err)
	}
	officialCount := len(gjson.ParseBytes(templates["v2"].Tools).Array())
	if len(array) != officialCount+len(names) {
		t.Fatalf("出站数组应为官方全集+全部客户端工具: %d", len(array))
	}
	for _, name := range names {
		if strings.Contains(toolsRaw, name) {
			t.Fatalf("tools 数组泄漏客户端名 %q", name)
		}
		if strings.Contains(system, name) {
			t.Fatalf("system 文本泄漏客户端名 %q", name)
		}
		if aliases[ocExpectWireName(name)] != name {
			t.Fatalf("别名表缺少 %q 的摘要映射", name)
		}
	}
	if strings.Contains(system, "additional_tools") {
		t.Fatal("上限已移除, 不应再出现溢出文本块")
	}
}

// TestRestoreToolAliasesInEventAndBody 锁定响应还原: 非流式与流式增量均还原客户端名。
func TestRestoreToolAliasesInEventAndBody(t *testing.T) {
	aliases := map[string]string{ocToolAliasPrefix + "patcher": "patcher"}
	body := restoreToolAliasesInBody([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"`+ocToolAliasPrefix+`patcher"}}]}}]}`), aliases)
	if got := gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String(); got != "patcher" {
		t.Fatalf("非流式未还原: %q", got)
	}
	event := restoreToolAliasesInEvent([]byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"`+ocToolAliasPrefix+`patcher"}}]}}]}`), aliases)
	if got := gjson.GetBytes(event, "choices.0.delta.tool_calls.0.function.name").String(); got != "patcher" {
		t.Fatalf("流式未还原: %q", got)
	}
	if string(restoreToolAliasesInBody([]byte(`{"choices":[]}`), nil)) != `{"choices":[]}` {
		t.Fatal("空别名表应原样返回")
	}
}

// TestShapeV1AlsoAlignsToolset 锁定 v1 档同样做官方工具集对齐(2026-10-30 起)。
// 此前 v1 只补 read 占位不改名, 与 v2 行为不一致; 实抓证明 v1 客户端工具集形态
// 与 v2 同源, 因此两档统一走 ensureOfficialToolset。
func TestShapeV1AlsoAlignsToolset(t *testing.T) {
	out, changed, aliases := shapeOpencodeFreeBody(
		[]byte(`{"model":"x-free","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"mytool","description":"c","parameters":{"type":"object","properties":{}}}}]}`),
		true, model.UpstreamProtocolChat)
	_ = out
	if !changed {
		t.Fatal("v1 应改写工具集")
	}
	found := false
	for name, orig := range aliases {
		if strings.HasPrefix(name, ocToolAliasPrefix) && orig == "mytool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("v1 应把客户端工具改名为摘要前缀: %v", aliases)
	}
}

// TestBuildOfficialToolTargetsCodexCandidates 锁定 Codex 客户端候选映射。
func TestBuildOfficialToolTargetsCodexCandidates(t *testing.T) {
	targets := buildOfficialToolTargets([]byte(`{"tools":[
		{"type":"function","function":{"name":"exec_command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"update_plan","parameters":{"type":"object","properties":{"plan":{"type":"array"}},"required":["plan"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}}}
	]}`))
	if targets["bash"].clientName != "exec_command" {
		t.Fatalf("bash 应映射到 exec_command: %+v", targets["bash"])
	}
	if targets["shell"].clientName != "exec_command" {
		t.Fatalf("shell 应映射到 exec_command: %+v", targets["shell"])
	}
	if targets["todowrite"].clientName != "update_plan" {
		t.Fatalf("todowrite 应映射到 update_plan: %+v", targets["todowrite"])
	}
	if targets["todowrite"].transform == nil {
		t.Fatal("todowrite→update_plan 应注册 transform")
	}
	if targets["patch"].clientName != "apply_patch" {
		t.Fatalf("patch 应映射到 apply_patch: %+v", targets["patch"])
	}
}

// TestTodoWriteToUpdatePlanArgs 锁定 todos→plan 变换: 丢弃 cancelled,
// content 映射为 step, status 归一。
func TestTodoWriteToUpdatePlanArgs(t *testing.T) {
	args := `{"todos":[{"content":"step1","status":"in_progress"},{"content":"step2","status":"completed"},{"content":"step3","status":"cancelled"},{"content":"step4","status":"pending"}]}`
	got := todoWriteToUpdatePlanArgs("todowrite", args)
	plan := gjson.Get(got, "plan")
	if !plan.IsArray() || len(plan.Array()) != 3 {
		t.Fatalf("应丢弃 cancelled 保留 3 条: %s", got)
	}
	if gjson.Get(got, "plan.0.step").String() != "step1" {
		t.Fatalf("content 应映射为 step: %s", got)
	}
	if gjson.Get(got, "plan.0.status").String() != "in_progress" {
		t.Fatalf("in_progress 应保留: %s", got)
	}
	if gjson.Get(got, "plan.2.status").String() != "pending" {
		t.Fatalf("pending 应保留: %s", got)
	}
	// 非法 JSON 原样返回
	bad := `not json`
	if todoWriteToUpdatePlanArgs("todowrite", bad) != bad {
		t.Fatal("非法 JSON 应原样返回")
	}
}

// TestObfuscateToolDescription 锁定描述混淆: 插零宽空格, 幂等, 无空格不变。
func TestObfuscateToolDescription(t *testing.T) {
	got := obfuscateToolDescription("hello world")
	if !strings.ContainsRune(got, '\u200b') {
		t.Fatal("应插入零宽空格")
	}
	if strings.Contains(got, "hello world") {
		t.Fatal("原文不应直接出现")
	}
	if obfuscateToolDescription(got) != got {
		t.Fatal("应幂等")
	}
	if obfuscateToolDescription("nospace") != "nospace" {
		t.Fatal("无空格描述不应改变")
	}
	if obfuscateToolDescription("") != "" {
		t.Fatal("空串不应改变")
	}
}

// TestSanitizeSchemaDescriptions 锁定嵌套 description 混淆与无描述时不变。
func TestSanitizeSchemaDescriptions(t *testing.T) {
	raw := []byte(`{"type":"object","properties":{"x":{"type":"string","description":"hello world"}}}`)
	got := sanitizeSchemaDescriptions(raw)
	desc := gjson.GetBytes(got, "properties.x.description").String()
	if !strings.ContainsRune(desc, '\u200b') {
		t.Fatalf("嵌套 description 应被混淆: %s", got)
	}
	raw2 := []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	if string(sanitizeSchemaDescriptions(raw2)) != string(raw2) {
		t.Fatal("无 description 的 schema 不应改变")
	}
}

// TestEnsureOfficialToolsetConvertsLegacyFunctions 锁定旧版 functions 只编码一次。
func TestEnsureOfficialToolsetConvertsLegacyFunctions(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"functions":[{"name":"legacy_tool","description":"d","parameters":{"type":"object","properties":{}}}]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	if gjson.GetBytes(out, "functions").Exists() {
		t.Fatal("functions 应被移除")
	}
	wire := ocExpectWireName("legacy_tool")
	if aliases[wire] != "legacy_tool" {
		t.Fatalf("别名表缺少旧版工具映射: %v", aliases)
	}
	found := false
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		if tool.Get("function.name").String() == wire {
			found = true
			if tool.Get("type").String() != "function" {
				t.Fatalf("转换后应带 function 类型: %s", tool.Raw)
			}
		}
	}
	if !found {
		t.Fatal("旧版工具应转换为 tools 条目")
	}
}

// TestBuildOfficialToolTargetsLegacyFunctions 旧版 functions 同样登记官方名映射。
func TestBuildOfficialToolTargetsLegacyFunctions(t *testing.T) {
	body := []byte(`{"model":"m","functions":[{"name":"Bash","description":"d","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}]}`)
	targets := buildOfficialToolTargets(body)
	target, ok := targets["bash"]
	if !ok {
		t.Fatalf("bash 应映射到旧版声明的 Bash: %v", targets)
	}
	if target.clientName != "Bash" || !target.hasSchema {
		t.Fatalf("target 异常: %+v", target)
	}
}
