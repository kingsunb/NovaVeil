package relay

import (
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/tidwall/gjson"
)

// TestIsOpencodeFreeModel 验证免费档判定的三层门: Zen 档 + (内置免费渠道 或名字含 free 子串)。
func TestIsOpencodeFreeModel(t *testing.T) {
	cases := []struct {
		name      string
		baseURL   string
		isFree    bool
		modelName string
		want      bool
	}{
		{"内置免费渠道 zen", "https://opencode.ai/zen", true, "gpt-5.3-codex", true},
		{"zen 上 -free 后缀模型名", "https://opencode.ai/zen", false, "deepseek-v4-flash-free", true},
		{"zen 上名字中部含 free", "https://opencode.ai/zen", false, "gpt-5.3-codex-free-turbo", true},
		{"zen 上 free 前缀模型名", "https://opencode.ai/zen", false, "free-gpt-5.3-codex", true},
		{"zen 上非 free 模型名且非免费渠道", "https://opencode.ai/zen", false, "gpt-5.3-codex", false},
		{"go 档不判定免费", "https://opencode.ai/zen/go", true, "gpt-5.3-codex-free", false},
		{"非 opencode 渠道不判定免费", "https://api.openai.com/v1", true, "gpt-x-free", false},
		{"空 baseURL", "", true, "x-free", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channel := model.Channel{BaseURL: tc.baseURL, IsFree: tc.isFree}
			if got := isOpencodeFreeModel(channel, tc.modelName); got != tc.want {
				t.Fatalf("isOpencodeFreeModel(%q, model=%q) = %v, 期望 %v", tc.baseURL, tc.modelName, got, tc.want)
			}
		})
	}
}

// TestShapeOpencodeFreeBodyForcesAgentShape 验证非流式无工具正文被规范成 Agent 形态:
// stream=true、include_usage=true、注入全部五个核心工具。
func TestShapeOpencodeFreeBodyForcesAgentShape(t *testing.T) {
	body, changed := shapeOpencodeFreeBody([]byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`))
	if !changed {
		t.Fatalf("应判定发生改写")
	}
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("应强制 stream=true")
	}
	if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("应补齐 stream_options.include_usage=true")
	}
	var names []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if len(names) != len(opencodeFreeCoreTools) {
		t.Fatalf("应注入 %d 个核心工具, 实际 %d: %v", len(opencodeFreeCoreTools), len(names), names)
	}
	for _, core := range opencodeFreeCoreTools {
		found := false
		for _, n := range names {
			if n == core {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺失核心工具 %q: %v", core, names)
		}
	}
}

// TestShapeOpencodeFreeBodyIdempotent 验证已满足 Agent 形态的请求不再改写。
func TestShapeOpencodeFreeBodyIdempotent(t *testing.T) {
	body := []byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"tools":[` +
		func() string {
			var parts []string
			for _, core := range opencodeFreeCoreTools {
				parts = append(parts, `{"type":"function","function":{"name":"`+core+`"}}`)
			}
			return strings.Join(parts, ",")
		}() + `]}`)
	_, changed := shapeOpencodeFreeBody(body)
	if changed {
		t.Fatalf("已完备的 Agent 形态应无改写")
	}
}

// TestShapeOpencodeFreeBodyMergesMissingTools 验证只追加缺失工具、既有工具原始字节原样保留。
func TestShapeOpencodeFreeBodyMergesMissingTools(t *testing.T) {
	body, _ := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":[{"type":"function","function":{"name":"bash","description":"custom","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}]}`))
	raw := gjson.GetBytes(body, "tools").Raw
	// 既有的 bash 工具字节完全保留。
	if !strings.Contains(raw, `"description":"custom"`) {
		t.Fatalf("既有工具应原样保留: %s", raw)
	}
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names[tool.Get("function.name").String()] = true
	}
	for _, core := range opencodeFreeCoreTools {
		if !names[core] {
			t.Fatalf("缺失核心工具 %q: %v", core, names)
		}
	}
}

// TestShapeOpencodeFreeBodyNonArrayToolsUntouched 验证 tools 非数组(客户端异常)时不改写。
func TestShapeOpencodeFreeBodyNonArrayToolsUntouched(t *testing.T) {
	body, changed := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":"nope"}`))
	if gjson.GetBytes(body, "tools").String() != "nope" {
		t.Fatalf("tools 非数组时不应篡改")
	}
	// stream/include_usage 仍属规范步骤, 与工具注入互不牵连; 这里只确保未注入工具覆盖原值。
	if !strings.Contains(string(body), `"tools":"nope"`) {
		t.Fatalf("原 tools 值应保留: %s", body)
	}
	_ = changed
}