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
