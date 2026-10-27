package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/tidwall/gjson"
)

func officialPrefix(t *testing.T, lane string) string {
	t.Helper()
	templates, err := loadOcProbeTemplates()
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}
	s := templates[lane].System
	if len(s) > ocOfficialSystemPrefixLen {
		s = s[:ocOfficialSystemPrefixLen]
	}
	return s
}

func TestEnsureOfficialOpencodeSystemInjectsWhenMissing(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	out, changed := ensureOfficialOpencodeSystem(body, "v1", "m1")
	if !changed {
		t.Fatal("expected injection")
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 || messages[0].Get("role").String() != "system" {
		t.Fatalf("unexpected messages: %s", out)
	}
	content := messages[0].Get("content").String()
	if !strings.HasPrefix(content, officialPrefix(t, "v1")) {
		t.Fatal("injected system lacks official prefix")
	}
	if messages[1].Get("content").String() != "hi" {
		t.Fatal("user message not preserved")
	}
	if strings.Contains(content, "{{") {
		t.Fatal("unresolved template marker")
	}
	if !strings.Contains(content, time.Now().Format("2006")) {
		t.Fatal("date not substituted")
	}
}

func TestEnsureOfficialOpencodeSystemMergesClientSystem(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`)
	out, changed := ensureOfficialOpencodeSystem(body, "v2", "m1")
	if !changed {
		t.Fatal("expected merge")
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 || messages[0].Get("role").String() != "system" {
		t.Fatalf("unexpected messages: %s", out)
	}
	content := messages[0].Get("content").String()
	if !strings.HasPrefix(content, officialPrefix(t, "v2")) {
		t.Fatal("merged system lacks official prefix")
	}
	if !strings.HasSuffix(content, "\n\nbe brief") {
		t.Fatal("client system not appended")
	}
}

func TestEnsureOfficialOpencodeSystemIdempotent(t *testing.T) {
	injected, _ := ensureOfficialOpencodeSystem(
		[]byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`), "v1", "m1")
	again, changed := ensureOfficialOpencodeSystem(injected, "v1", "m1")
	if changed {
		t.Fatal("second pass should be idempotent")
	}
	if gjson.GetBytes(again, "messages.#").Int() != 2 {
		t.Fatal("message count changed on second pass")
	}
}

func TestEnsureOfficialOpencodeSystemLeavesProbeTemplateAlone(t *testing.T) {
	body, err := ocProbeClientBody("v1", "m1", "m1", defaultTestMessage, 0)
	if err != nil {
		t.Fatalf("build probe body: %v", err)
	}
	if _, changed := ensureOfficialOpencodeSystem(body, "v1", "m1"); changed {
		t.Fatal("official probe template must pass through unchanged")
	}
}

func TestShapeOpencodeFreeBodyInjectsOfficialSystem(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"max_tokens":100}`)
	out, changed, _ := shapeOpencodeFreeBody(body, true, model.UpstreamProtocolChat)
	if !changed {
		t.Fatal("expected shaping")
	}
	content := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.HasPrefix(content, officialPrefix(t, "v1")) || !strings.HasSuffix(content, "\n\nbe brief") {
		t.Fatalf("shaped system wrong: %.80s", content)
	}
	if keys := bodyTopLevelKeys(out); keys[0] != "model" {
		t.Fatalf("v1 key order not normalized: %v", keys)
	}
}

func bodyTopLevelKeys(body []byte) []string {
	var keys []string
	gjson.ParseBytes(body).ForEach(func(key, _ gjson.Result) bool {
		keys = append(keys, key.String())
		return true
	})
	return keys
}

// TestEnsureOfficialOpencodeSystemUsesRuntimeWorkdir 验证系统提示写运行时工作目录,
// 不再固定为抓包机器路径; 官方客户端写自身 cwd, 网关同理。
func TestEnsureOfficialOpencodeSystemUsesRuntimeWorkdir(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	out, changed := ensureOfficialOpencodeSystem(body, "v1", "m1")
	if !changed {
		t.Fatal("expected injection")
	}
	content := gjson.GetBytes(out, "messages.0.content").String()
	want := "Working directory: " + ocRuntimeWorkdir()
	if !strings.Contains(content, want) {
		t.Fatalf("system prompt missing runtime workdir %q: %.200s", want, content)
	}
	if strings.Contains(content, "{{") {
		t.Fatalf("unresolved template marker: %.200s", content)
	}
}

// TestOcProbeClientBodyUsesRuntimeWorkdir 验证体检正文的系统提示同样写运行时工作目录。
func TestOcProbeClientBodyUsesRuntimeWorkdir(t *testing.T) {
	body, err := ocProbeClientBody("v1", "m1", "m1", defaultTestMessage, 0)
	if err != nil {
		t.Fatalf("build probe body: %v", err)
	}
	content := gjson.GetBytes(body, "messages.0.content").String()
	want := "Working directory: " + ocRuntimeWorkdir()
	if !strings.Contains(content, want) {
		t.Fatalf("probe system missing runtime workdir %q: %.200s", want, content)
	}
	if strings.Contains(content, "{{") {
		t.Fatalf("unresolved template marker: %.200s", content)
	}
}

// TestEnsureOfficialOpencodeSystemKeepsContentParts 验证客户端 system 为 parts 数组时,
// 官方提示作为首个 text part 前置, 原 parts 逐条保留且重复整形幂等。
func TestEnsureOfficialOpencodeSystemKeepsContentParts(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"system","content":[{"type":"text","text":"be brief"},{"type":"text","text":"no markdown"}]},{"role":"user","content":"hi"}]}`)
	out, changed := ensureOfficialOpencodeSystem(body, "v2", "m1")
	if !changed {
		t.Fatal("expected injection")
	}
	parts := gjson.GetBytes(out, "messages.0.content").Array()
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d: %s", len(parts), out)
	}
	if !strings.HasPrefix(parts[0].Get("text").String(), officialPrefix(t, "v2")) {
		t.Fatal("official part not prepended")
	}
	if parts[1].Get("text").String() != "be brief" || parts[2].Get("text").String() != "no markdown" {
		t.Fatalf("client parts lost: %s", out)
	}
	if gjson.GetBytes(out, "messages.1.content").String() != "hi" {
		t.Fatal("later messages changed")
	}
	again, changedAgain := ensureOfficialOpencodeSystem(out, "v2", "m1")
	if changedAgain {
		t.Fatal("second pass should be idempotent")
	}
	if len(gjson.GetBytes(again, "messages.0.content").Array()) != 3 {
		t.Fatal("parts count changed on second pass")
	}
}

// TestEnsureOfficialOpencodeInstructionsInjectsWhenMissing 验证 Responses 正文无 instructions 时写入官方模板。
func TestEnsureOfficialOpencodeInstructionsInjectsWhenMissing(t *testing.T) {
	body := []byte(`{"model":"m1","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, changed := ensureOfficialOpencodeInstructions(body, "v2", "m1")
	if !changed {
		t.Fatal("expected injection")
	}
	instructions := gjson.GetBytes(out, "instructions").String()
	if !strings.HasPrefix(instructions, officialPrefix(t, "v2")) {
		t.Fatalf("instructions lacks official prefix: %.120s", instructions)
	}
	if !strings.Contains(instructions, "Working directory: "+ocRuntimeWorkdir()) {
		t.Fatal("instructions missing runtime workdir")
	}
	if got := gjson.GetBytes(out, "input.0.role").String(); got != "user" {
		t.Fatalf("input changed: %q", got)
	}
}

// TestEnsureOfficialOpencodeInstructionsMergesClient 验证客户端自带 instructions 时前置官方模板并幂等。
func TestEnsureOfficialOpencodeInstructionsMergesClient(t *testing.T) {
	body := []byte(`{"model":"m1","instructions":"be brief","input":[]}`)
	out, changed := ensureOfficialOpencodeInstructions(body, "v1", "m1")
	if !changed {
		t.Fatal("expected merge")
	}
	instructions := gjson.GetBytes(out, "instructions").String()
	if !strings.HasPrefix(instructions, officialPrefix(t, "v1")) {
		t.Fatal("merged instructions lacks official prefix")
	}
	if !strings.HasSuffix(instructions, "\n\nbe brief") {
		t.Fatal("client instructions not appended")
	}
	again, changedAgain := ensureOfficialOpencodeInstructions(out, "v1", "m1")
	if changedAgain {
		t.Fatal("second pass should be idempotent")
	}
	if gjson.GetBytes(again, "instructions").String() != instructions {
		t.Fatal("instructions changed on second pass")
	}
}
