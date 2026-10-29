package relay

import (
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"

	"github.com/tidwall/gjson"
)

// TestEnsureOfficialV2ToolsetRenamesAndMerges 锁定 v2 出站工具形态: 客户端
// 自定义工具改名 clint_ 前缀, 官方全集在前, 别名表齐全。
func TestEnsureOfficialV2ToolsetRenamesAndMerges(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"my_tool.x","description":"c","parameters":{"type":"object","properties":{}}}}]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	if aliases["clint_my_tool_x"] != "my_tool.x" {
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
	if len(names) != officialCount+1 || names[officialCount] != "clint_my_tool_x" {
		t.Fatalf("出站应为官方全集+clint_my_tool_x, 实际 %v", names)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got == "my_tool.x" {
		t.Fatal("客户端名不应出现在出站")
	}
}

// TestEnsureOfficialV2ToolsetRewritesToolCalls 锁定 tool_choice 与历史 tool_calls 的同步改名。
func TestEnsureOfficialV2ToolsetRewritesToolCalls(t *testing.T) {
	body := []byte(`{"model":"m","tool_choice":{"type":"function","function":{"name":"patcher"}},
		"tools":[{"type":"function","function":{"name":"patcher","description":"c","parameters":{"type":"object","properties":{}}}}],
		"messages":[{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"patcher","arguments":"{}"}}]}]}`)
	out, aliases := ensureOfficialToolset(body, "v2")
	if aliases["clint_patcher"] != "patcher" {
		t.Fatalf("别名表异常: %v", aliases)
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "clint_patcher" {
		t.Fatalf("tool_choice 未改名: %q", got)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String(); got != "clint_patcher" {
		t.Fatalf("历史 tool_calls 未改名: %q", got)
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
