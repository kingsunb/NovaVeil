package relay

import (
	"bytes"
	"testing"

	"github.com/looplj/axonhub/llm"
)

func TestApplyOpencodeAlias_NoMatch(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello world"}]}`)
	out, state := applyOpencodeAlias(body)
	if state != nil {
		t.Fatalf("expected nil state for no match, got %+v", state)
	}
	if string(out) != string(body) {
		t.Fatalf("body should be unchanged, got %s", out)
	}
}

func TestApplyOpencodeAlias_SingleKeyword(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"You are DeepSeek Harness"},{"role":"user","content":"hi"}]}`)
	out, state := applyOpencodeAlias(body)
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.keyword != "DeepSeek Harness" {
		t.Fatalf("expected keyword 'DeepSeek Harness', got %q", state.keyword)
	}
	expected := `{"messages":[{"role":"system","content":"You are opencode"},{"role":"user","content":"hi"}]}`
	if string(out) != expected {
		t.Fatalf("expected %s, got %s", expected, out)
	}
}

func TestApplyOpencodeAlias_MultipleKeywords(t *testing.T) {
	body := []byte(`{"content":"codex and claude code are tools"}`)
	out, state := applyOpencodeAlias(body)
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	// First keyword found wins for restoration.
	if state.keyword != "codex" {
		t.Fatalf("expected first keyword 'codex', got %q", state.keyword)
	}
	expected := `{"content":"opencode and opencode are tools"}`
	if string(out) != expected {
		t.Fatalf("expected %s, got %s", expected, out)
	}
}

func TestApplyOpencodeAlias_AllKeywords(t *testing.T) {
	for _, kw := range opencodeAliasKeywords {
		body := []byte(`{"content":"uses ` + kw + ` today"}`)
		out, state := applyOpencodeAlias(body)
		if state == nil {
			t.Fatalf("keyword %q should match", kw)
		}
		if state.keyword != kw {
			t.Fatalf("expected %q, got %q", kw, state.keyword)
		}
		if !containsStr(out, "opencode") {
			t.Fatalf("replacement should contain 'opencode', got %s", out)
		}
		if containsStr(out, kw) {
			t.Fatalf("original keyword %q should be gone, got %s", kw, out)
		}
	}
}

func TestRestoreOpencodeAliasNonStream_ChatString(t *testing.T) {
	state := &opencodeAliasState{keyword: "DeepSeek Harness"}
	body := []byte(`{"model":"opencode/latest/2.2.1/cli","choices":[{"message":{"content":"I am opencode, nice to meet you"}}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	// Model name should NOT be restored.
	if !containsStr(out, "opencode/latest/2.2.1/cli") {
		t.Fatalf("model name should be preserved, got %s", out)
	}
	// Content should be restored.
	if !containsStr(out, "I am DeepSeek Harness") {
		t.Fatalf("content should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_ChatArray(t *testing.T) {
	state := &opencodeAliasState{keyword: "codex"}
	body := []byte(`{"choices":[{"message":{"content":[{"type":"text","text":"powered by opencode"}]}}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	if !containsStr(out, "powered by codex") {
		t.Fatalf("content array text should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_ChatReasoning(t *testing.T) {
	state := &opencodeAliasState{keyword: "codex"}
	body := []byte(`{"choices":[{"message":{"content":"ok","reasoning_content":"thinking about opencode"}}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	if !containsStr(out, "thinking about codex") {
		t.Fatalf("reasoning_content should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_Anthropic(t *testing.T) {
	state := &opencodeAliasState{keyword: "claude code"}
	body := []byte(`{"model":"claude-3","content":[{"type":"text","text":"hello from opencode"}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatAnthropicMessage, state)
	// Model name should NOT be restored.
	if !containsStr(out, `"model":"claude-3"`) {
		t.Fatalf("model name should be preserved, got %s", out)
	}
	if !containsStr(out, "hello from claude code") {
		t.Fatalf("content text should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_Responses(t *testing.T) {
	state := &opencodeAliasState{keyword: "pi agent"}
	body := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"opencode says hi"}]}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIResponse, state)
	if !containsStr(out, "pi agent says hi") {
		t.Fatalf("responses content text should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_NilState(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"opencode"}}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, nil)
	if string(out) != string(body) {
		t.Fatalf("nil state should be no-op, got %s", out)
	}
}

func TestRestoreOpencodeAliasNonStream_NoOpencodeInBody(t *testing.T) {
	state := &opencodeAliasState{keyword: "codex"}
	body := []byte(`{"choices":[{"message":{"content":"hello world"}}]}`)
	out := restoreOpencodeAliasNonStream(body, llm.APIFormatOpenAIChatCompletion, state)
	if string(out) != string(body) {
		t.Fatalf("body without 'opencode' should be unchanged, got %s", out)
	}
}

func TestRestoreOpencodeAliasStream_Chat(t *testing.T) {
	state := &opencodeAliasState{keyword: "DeepSeek Harness"}
	data := []byte(`{"choices":[{"delta":{"content":"I am opencode"}}]}`)
	out := restoreOpencodeAliasStream(data, llm.APIFormatOpenAIChatCompletion, state)
	if !containsStr(out, "I am DeepSeek Harness") {
		t.Fatalf("stream content should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasStream_ChatReasoning(t *testing.T) {
	state := &opencodeAliasState{keyword: "codex"}
	data := []byte(`{"choices":[{"delta":{"reasoning_content":"opencode thinks"}}]}`)
	out := restoreOpencodeAliasStream(data, llm.APIFormatOpenAIChatCompletion, state)
	if !containsStr(out, "codex thinks") {
		t.Fatalf("stream reasoning should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasStream_Anthropic(t *testing.T) {
	state := &opencodeAliasState{keyword: "claude code"}
	data := []byte(`{"type":"content_block_delta","delta":{"text":"opencode here"}}`)
	out := restoreOpencodeAliasStream(data, llm.APIFormatAnthropicMessage, state)
	if !containsStr(out, "claude code here") {
		t.Fatalf("stream anthropic text should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasStream_Responses(t *testing.T) {
	state := &opencodeAliasState{keyword: "pi agent"}
	data := []byte(`{"type":"response.output_text.delta","delta":"opencode rules"}`)
	out := restoreOpencodeAliasStream(data, llm.APIFormatOpenAIResponse, state)
	if !containsStr(out, "pi agent rules") {
		t.Fatalf("stream responses delta should be restored, got %s", out)
	}
}

func TestRestoreOpencodeAliasStream_NilState(t *testing.T) {
	data := []byte(`{"choices":[{"delta":{"content":"opencode"}}]}`)
	out := restoreOpencodeAliasStream(data, llm.APIFormatOpenAIChatCompletion, nil)
	if string(out) != string(data) {
		t.Fatalf("nil state should be no-op, got %s", out)
	}
}

func TestOpencodeAliasRoundTrip(t *testing.T) {
	// Full round-trip: request replaces keyword, response restores it.
	original := `{"messages":[{"role":"system","content":"You are DeepSeek Harness, a coding agent"}]}`
	masked, state := applyOpencodeAlias([]byte(original))
	if state == nil {
		t.Fatal("expected alias state")
	}
	if containsStr(masked, "DeepSeek Harness") {
		t.Fatal("keyword should be replaced in request")
	}
	if !containsStr(masked, "opencode") {
		t.Fatal("replacement should be in request")
	}
	// Simulate upstream response mentioning "opencode".
	resp := []byte(`{"choices":[{"message":{"content":"I am opencode, ready to help"}}]}`)
	restored := restoreOpencodeAliasNonStream(resp, llm.APIFormatOpenAIChatCompletion, state)
	if !containsStr(restored, "I am DeepSeek Harness") {
		t.Fatalf("response should restore keyword, got %s", restored)
	}
}

func containsStr(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}
