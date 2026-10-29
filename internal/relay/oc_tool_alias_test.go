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
// 客户端原始工具名不得出现在 tools 数组与 system 文本(含溢出块)。
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
	if !strings.Contains(system, "additional_tools") {
		t.Fatal("溢出文本块应存在")
	}
	if !strings.Contains(system, ocExpectWireName("leak_probe_24")) {
		t.Fatal("溢出块应用摘要名指代工具")
	}
}

// TestRestoreToolAliasesInEventAndBody 锁定响应还原: 非流式与流式增量均还原客户端名。
func TestRestoreToolAliasesInEventAndBody(t *testing.T) {
	aliases := map[string]string{"clint_patcher": "patcher"}
	body := restoreToolAliasesInBody([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"clint_patcher"}}]}}]}`), aliases)
	if got := gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String(); got != "patcher" {
		t.Fatalf("非流式未还原: %q", got)
	}
	event := restoreToolAliasesInEvent([]byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"clint_patcher"}}]}}]}`), aliases)
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
		if strings.HasPrefix(name, "clint_") && orig == "mytool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("v1 应把客户端工具改名为 clint_ 前缀: %v", aliases)
	}
}
