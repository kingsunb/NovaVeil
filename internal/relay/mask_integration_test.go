package relay

import (
	"bytes"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/looplj/axonhub/llm"
)

// TestMaskRoundtrip_CustomTerms 验证自定义敏感词的脱敏→还原完整往返:
// 请求体中的拦截词被替换为占位符, 还原后恢复原文。
func TestMaskRoundtrip_CustomTerms(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	terms := []mask.CustomTerm{
		{Value: "李四", Category: "人名"},
		{Value: "ProjectAlpha", Category: "项目代号"},
	}
	original := `{"model":"gpt-4","messages":[{"role":"user","content":"让李四检查ProjectAlpha的日志"}]}`

	result, err := engine.Apply(original, "session-1", nil, terms)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	// 脱敏后不应包含原文拦截词
	if contains(result.Masked, "李四") {
		t.Errorf("masked body still contains custom term '李四': %s", result.Masked)
	}
	if contains(result.Masked, "ProjectAlpha") {
		t.Errorf("masked body still contains custom term 'ProjectAlpha': %s", result.Masked)
	}
	// 应包含占位符
	if !contains(result.Masked, "{{TERM_") {
		t.Errorf("masked body should contain TERM placeholder: %s", result.Masked)
	}

	// 非流式还原
	restored := restoreNonStream([]byte(result.Masked), result.Mapping)
	if string(restored) != original {
		t.Errorf("roundtrip mismatch:\n  original:  %s\n  restored:  %s", original, restored)
	}
}

// TestMaskRoundtrip_BuiltinRules 验证内置规则(手机号)的脱敏→还原往返。
func TestMaskRoundtrip_BuiltinRules(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	enabled := map[string]bool{"PHONE": true}
	original := `{"content":"联系电话 13800138000 请回拨"}`

	result, err := engine.Apply(original, "s1", enabled, nil)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if contains(result.Masked, "13800138000") {
		t.Errorf("masked body still contains phone number: %s", result.Masked)
	}
	restored := restoreNonStream([]byte(result.Masked), result.Mapping)
	if string(restored) != original {
		t.Errorf("roundtrip mismatch:\n  original:  %s\n  restored:  %s", original, restored)
	}
}

// TestRestoreNonStream_NilMapping 验证映射表为 nil 时原样返回(默认关闭零开销)。
func TestRestoreNonStream_NilMapping(t *testing.T) {
	body := []byte(`{"content":"hello 13800138000"}`)
	out := restoreNonStream(body, nil)
	if string(out) != string(body) {
		t.Errorf("nil mapping should be no-op, got: %s", out)
	}
}

// TestRestoreStreamEvent_NilMapping 验证流式还原映射表为 nil 时原样返回。
func TestRestoreStreamEvent_NilMapping(t *testing.T) {
	data := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	out := restoreStreamEvent(data, llm.APIFormatOpenAIChatCompletion, nil)
	if string(out) != string(data) {
		t.Errorf("nil mapping should be no-op, got: %s", out)
	}
}

// TestRestoreStreamEvent_OpenAI 验证 OpenAI Chat SSE 事件的占位符还原。
func TestRestoreStreamEvent_OpenAI(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	terms := []mask.CustomTerm{{Value: "张三", Category: "人名"}}

	// 先脱敏一段文本拿到映射表与占位符
	result, err := engine.Apply("张三", "s1", nil, terms)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	mapping := result.Mapping
	placeholder := extractPlaceholder(result.Masked)
	if placeholder == "" {
		t.Fatalf("expected a placeholder in masked text: %s", result.Masked)
	}

	// 模拟上游返回的 SSE 事件(含占位符)
	data := []byte(`{"choices":[{"delta":{"content":"建议 ` + placeholder + ` 核对"}}]}`)
	restorer := mask.NewStreamRestorer(mapping)
	out := restoreStreamEvent(data, llm.APIFormatOpenAIChatCompletion, restorer)
	if !contains(string(out), "张三") {
		t.Errorf("restored event should contain original '张三': %s", out)
	}
	if contains(string(out), placeholder) {
		t.Errorf("restored event should not contain placeholder: %s", out)
	}
}

// TestRestoreStreamEvent_Anthropic 验证 Anthropic SSE 事件的占位符还原。
func TestRestoreStreamEvent_Anthropic(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	terms := []mask.CustomTerm{{Value: "王五", Category: "人名"}}

	result, err := engine.Apply("王五", "s1", nil, terms)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	mapping := result.Mapping
	placeholder := extractPlaceholder(result.Masked)

	data := []byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"联系` + placeholder + `确认"}}`)
	restorer := mask.NewStreamRestorer(mapping)
	out := restoreStreamEvent(data, llm.APIFormatAnthropicMessage, restorer)
	if !contains(string(out), "王五") {
		t.Errorf("restored Anthropic event should contain '王五': %s", out)
	}
}

// TestRestoreStreamEvent_NoContentField 验证无文本字段的事件原样返回(如 usage/finish 事件)。
func TestRestoreStreamEvent_NoContentField(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	terms := []mask.CustomTerm{{Value: "赵六", Category: "人名"}}
	result, _ := engine.Apply("赵六", "s1", nil, terms)
	mapping := result.Mapping

	data := []byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	restorer := mask.NewStreamRestorer(mapping)
	out := restoreStreamEvent(data, llm.APIFormatOpenAIChatCompletion, restorer)
	if string(out) != string(data) {
		t.Errorf("event without content field should be unchanged: %s", out)
	}
}

// TestMaskSessionConsistency 验证同一会话内同一拦截词映射为同一占位符(多轮一致性)。
func TestMaskSessionConsistency(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	terms := []mask.CustomTerm{{Value: "李四", Category: "人名"}}

	r1, _ := engine.Apply("告诉李四", "session-42", nil, terms)
	r2, _ := engine.Apply("回复李四", "session-42", nil, terms)

	p1 := extractPlaceholder(r1.Masked)
	p2 := extractPlaceholder(r2.Masked)
	if p1 == "" || p2 == "" {
		t.Fatalf("expected placeholders, got p1=%q p2=%q", p1, p2)
	}
	if p1 != p2 {
		t.Errorf("same term in same session should map to same placeholder: %q vs %q", p1, p2)
	}
}

// TestMaskDisabled_NoTerms 验证无自定义词且无启用规则时原样返回。
func TestMaskDisabled_NoTerms(t *testing.T) {
	engine := mask.NewEngine(mask.NewSessionStore())
	original := "hello world 13800138000"
	result, err := engine.Apply(original, "s1", nil, nil)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if result.Masked != original {
		t.Errorf("with no rules and no terms, body should be unchanged: %s", result.Masked)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func extractPlaceholder(s string) string {
	return mask.PlaceholderRe.FindString(s)
}

// TestApplyRequestMaskContract 验证 applyRequestMask 的映射表返回契约:
//   - 无敏感信息命中 → nil 映射(不误标"已脱敏");
//   - 命中敏感信息   → 非 nil 映射 + 还原往返恢复原文;
//   - 分组开关关闭   → nil 映射(零开销短路)。
//
// 复用 failover 集成测试的 setupFailoverTest(integrationOnce) 初始化共享 DB,
// 不另开临时数据库: 避免替换全局 DB 后关闭导致后续集成测试 "database is closed",
// 也避免 SQLite mmap 地址空间泄漏触发 SQLITE_CANTOPEN。
func TestApplyRequestMaskContract(t *testing.T) {
	setupFailoverTest(t)
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache: %v", err)
	}
	if err := op.MaskConfigSet(model.MaskConfig{
		Enabled:           true,
		BuiltinRuleSwitch: map[string]bool{},
		CustomTerms:       []model.MaskConfigTerm{{Value: "机密项目", Category: "项目代号"}},
	}); err != nil {
		t.Fatalf("MaskConfigSet: %v", err)
	}

	t.Run("no match returns nil mapping", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"今天天气真好"}]}`)
		masked, mapping, matches, err := applyRequestMask(body, "sess-no-match", true)
		if err != nil {
			t.Fatalf("applyRequestMask: %v", err)
		}
		if mapping != nil {
			t.Errorf("无敏感信息命中时映射表应为 nil, 避免 Masked 误标; got non-nil mapping, masked=%s", masked)
		}
		if !bytes.Equal(masked, body) {
			t.Errorf("无命中时请求体应原样返回, got: %s", masked)
		}
		if matches != nil {
			t.Errorf("无命中时命中明细应为 nil, got: %v", matches)
		}
	})

	t.Run("hit returns non-nil mapping", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"请检查机密项目的进度"}]}`)
		masked, mapping, matches, err := applyRequestMask(body, "sess-hit", true)
		if err != nil {
			t.Fatalf("applyRequestMask: %v", err)
		}
		if mapping == nil {
			t.Fatalf("命中敏感信息时映射表应非 nil, masked=%s", masked)
		}
		if bytes.Equal(masked, body) {
			t.Errorf("命中时请求体应被替换, 不应与原文相同")
		}
		if len(matches) == 0 {
			t.Errorf("命中时命中明细应非空")
		}
		restored := restoreNonStream(masked, mapping)
		if !bytes.Equal(restored, body) {
			t.Errorf("还原后应恢复原文, got: %s", restored)
		}
	})

	t.Run("group switch off returns nil mapping", func(t *testing.T) {
		body := []byte(`{"content":"请检查机密项目的进度"}`)
		_, mapping, matches, err := applyRequestMask(body, "sess-group-off", false)
		if err != nil {
			t.Fatalf("applyRequestMask: %v", err)
		}
		if mapping != nil {
			t.Errorf("分组开关关闭时映射表应为 nil")
		}
		if matches != nil {
			t.Errorf("分组开关关闭时命中明细应为 nil")
		}
	})
}
