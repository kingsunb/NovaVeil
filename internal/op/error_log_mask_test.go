package op

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// decodeMaskMatches 把 normalizeMaskMatches 的输出(json.RawMessage)解码为切片,
// 便于断言裁剪/归一结果; nil 输入返回 nil。
func decodeMaskMatches(t *testing.T, raw json.RawMessage) []maskMatchRecord {
	t.Helper()
	if raw == nil {
		return nil
	}
	var got []maskMatchRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解码归一结果失败: %v, raw=%s", err, raw)
	}
	return got
}

// TestNormalizeMaskMatchesInvalidInput 验证坏输入归一为 nil(文档 07 §3.2、design §2.1.3(4)):
// nil / 空串 / 坏 JSON / 非数组 / 空数组 / 非字符串字段 → nil, 字段缺席, 不夹带坏字节。
func TestNormalizeMaskMatchesInvalidInput(t *testing.T) {
	cases := map[string]json.RawMessage{
		"nil":              nil,
		"empty":            json.RawMessage(""),
		"bad json":         json.RawMessage(`{"label":`),
		"string not array": json.RawMessage(`"not an array"`),
		"object not array": json.RawMessage(`{"label":"PHONE"}`),
		"empty array":      json.RawMessage(`[]`),
		"non-string field": json.RawMessage(`[{"label":123,"original":"x","placeholder":"y"}]`),
	}
	for name, in := range cases {
		if got := normalizeMaskMatches(in); got != nil {
			t.Errorf("%s 应归一为 nil, got %s", name, got)
		}
	}
}

// TestNormalizeMaskMatchesTruncation 验证合法数组超限条目被裁剪(条数/字段字节/总字节)。
func TestNormalizeMaskMatchesTruncation(t *testing.T) {
	t.Run("more than 128 clipped", func(t *testing.T) {
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < maskMatchMaxPerLog+50; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"label":"PHONE","original":"13800138000","placeholder":"p"}`)
		}
		b.WriteByte(']')
		got := decodeMaskMatches(t, normalizeMaskMatches(json.RawMessage(b.String())))
		if len(got) != maskMatchMaxPerLog {
			t.Fatalf("条数应裁剪到 %d, got %d", maskMatchMaxPerLog, len(got))
		}
	})

	t.Run("field bytes truncated at UTF-8 boundary", func(t *testing.T) {
		raw := json.RawMessage(`[{"label":"` + strings.Repeat("中", 20) + `","original":"` + strings.Repeat("o", 300) + `","placeholder":"` + strings.Repeat("p", 100) + `"}]`)
		got := decodeMaskMatches(t, normalizeMaskMatches(raw))
		if len(got) != 1 {
			t.Fatalf("应保留 1 条, got %d", len(got))
		}
		if got[0].Label != strings.Repeat("中", 10) {
			t.Fatalf("label = %q, want 10 个完整\"中\"", got[0].Label)
		}
		if !utf8.ValidString(got[0].Label) {
			t.Fatal("label 截断破坏 UTF-8 边界")
		}
		if len(got[0].Original) != maskMatchMaxOriginalBytes {
			t.Fatalf("original 截断异常: len=%d want=%d", len(got[0].Original), maskMatchMaxOriginalBytes)
		}
		if len(got[0].Placeholder) != maskMatchMaxPlaceholderBytes {
			t.Fatalf("placeholder 截断异常: len=%d want=%d", len(got[0].Placeholder), maskMatchMaxPlaceholderBytes)
		}
	})

	t.Run("total bytes clipped before count limit", func(t *testing.T) {
		label := strings.Repeat("l", maskMatchMaxLabelBytes)
		original := strings.Repeat("o", maskMatchMaxOriginalBytes)
		placeholder := strings.Repeat("p", maskMatchMaxPlaceholderBytes)
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < maskMatchMaxPerLog; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"label":"` + label + `","original":"` + original + `","placeholder":"` + placeholder + `"}`)
		}
		b.WriteByte(']')
		got := decodeMaskMatches(t, normalizeMaskMatches(json.RawMessage(b.String())))
		if len(got) >= maskMatchMaxPerLog {
			t.Fatalf("应在条数上限前因总字节裁剪, got %d", len(got))
		}
		total := 0
		for _, m := range got {
			total += len(m.Label) + len(m.Original) + len(m.Placeholder)
		}
		if total > maskMatchMaxTotalBytes {
			t.Fatalf("裁剪后总字节 %d 仍超上限 %d", total, maskMatchMaxTotalBytes)
		}
	})
}

// TestNormalizeMaskMatchesDoesNotRedactOriginal 验证 original 字段不被 redact:
// 命中原文本身即被批准下发的敏感片段, redact 会破坏「展示原文」目的(design §2.1.3(4))。
func TestNormalizeMaskMatchesDoesNotRedactOriginal(t *testing.T) {
	original := `password=hunter2 sk-abc123`
	raw := json.RawMessage(`[{"label":"SECRET","original":"` + original + `","placeholder":"{{SECRET_x}}"}]`)
	got := decodeMaskMatches(t, normalizeMaskMatches(raw))
	if len(got) != 1 {
		t.Fatalf("应保留 1 条, got %d", len(got))
	}
	if got[0].Original != original {
		t.Fatalf("original 被 redact 或改动: got %q, want %q", got[0].Original, original)
	}
}

// TestSanitizeErrorLogNormalizesMaskMatches 验证 sanitizeErrorLog 对含 MaskMatches 的
// ErrorLog 端到端归一: 合法明细被归一且 original 不被 redact, 坏 JSON 归一为 nil。
func TestSanitizeErrorLogNormalizesMaskMatches(t *testing.T) {
	t.Run("valid matches normalized and original preserved", func(t *testing.T) {
		entry := model.ErrorLog{
			Model:       "m",
			ErrClass:    "timeout",
			ErrBrief:    "boom",
			MaskMatches: json.RawMessage(`[{"label":"PHONE","original":"13800138000","placeholder":"{{PHONE_x}}"},{"label":"SECRET","original":"password=hunter2","placeholder":"{{SECRET_y}}"}]`),
		}
		got := sanitizeErrorLog(entry)
		decoded := decodeMaskMatches(t, got.MaskMatches)
		if len(decoded) != 2 {
			t.Fatalf("应保留 2 条, got %d", len(decoded))
		}
		if decoded[0].Label != "PHONE" || decoded[0].Original != "13800138000" || decoded[0].Placeholder != "{{PHONE_x}}" {
			t.Fatalf("第 1 条被改动: %+v", decoded[0])
		}
		if decoded[1].Original != "password=hunter2" {
			t.Fatalf("original 应不被 redact: %q", decoded[1].Original)
		}
	})

	t.Run("bad json normalized to nil", func(t *testing.T) {
		entry := model.ErrorLog{
			Model:       "m",
			ErrClass:    "timeout",
			MaskMatches: json.RawMessage(`{"label":`),
		}
		got := sanitizeErrorLog(entry)
		if got.MaskMatches != nil {
			t.Fatalf("坏 JSON 应归一为 nil, got %s", got.MaskMatches)
		}
	})
}