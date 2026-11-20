package mask

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenSuffix_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := genSuffix()
		assert.Len(t, s, 6, "后缀必须 6 位")
		for _, c := range s {
			assert.Contains(t, consonants, string(c), "后缀必须全为辅音: %q", s)
		}
	}
}

func TestNewToken_Format(t *testing.T) {
	tok := NewToken("PHONE")
	assert.True(t, IsPlaceholder(tok), "NewToken 须生成合法占位符: %q", tok)
	assert.True(t, strings.HasPrefix(tok, "{{REDACTED_PHONE_NUMBER_"), "占位符须以清晰类型名开头: %q", tok)
	assert.True(t, strings.HasSuffix(tok, "}}"), "占位符须以 }} 结尾: %q", tok)
}

func TestNewToken_LabelSanitization(t *testing.T) {
	long := NewToken(strings.Repeat("A", 100))
	assert.True(t, IsPlaceholder(long), "超长标签仍应生成可还原占位符")
	assert.Equal(t, 62, len(long), "占位符长度须锁定在还原明细上限内")
	cases := []struct {
		in   string
		want string
	}{
		{"phone", "REDACTED_PHONE_NUMBER"},
		{"my-label!", "REDACTED_MY_LABEL"},
		{"", "REDACTED_CUSTOM_SENSITIVE_TEXT"},
		{"密码", "REDACTED_CUSTOM_SENSITIVE_TEXT"},
		{"a1b2c3d4e5f6g7h8i9", "REDACTED_A1B2C3D4E5F6G7H8I9"},
		{"api-key", "REDACTED_API_KEY"},
		{"SOLANA_KEYPAIR", "REDACTED_SOLANA_PRIVATE_KEYPAIR"},
		{"REDACTED_API_KEY", "REDACTED_API_KEY"},
		{"SECRET", "REDACTED_CREDENTIAL"},
		{"TOKEN", "REDACTED_BEARER_TOKEN"},
		{"IP", "REDACTED_PUBLIC_IPV4_ADDRESS"},
	}
	for _, c := range cases {
		tok := NewToken(c.in)
		assert.True(t, strings.HasPrefix(tok, "{{"+c.want+"_"), "label=%q 期望前缀 {{%s_, 得 %q", c.in, c.want, tok)
		assert.True(t, IsPlaceholder(tok), "须为合法占位符: %q", tok)
	}
}

func TestIsPlaceholder(t *testing.T) {
	assert.True(t, IsPlaceholder("{{PHONE_bcdfgh}}"))
	assert.True(t, IsPlaceholder("{{REDACTED_EMAIL_ADDRESS_zkpmqx}}"))
	assert.True(t, IsPlaceholder("前缀 {{REDACTED_EMAIL_ADDRESS_zkpmqx}} 后缀")) // 子串含占位符
	assert.False(t, IsPlaceholder("not a placeholder"))
	assert.False(t, IsPlaceholder("{{PHONE_bcdfg}}"))   // 后缀 5 位
	assert.False(t, IsPlaceholder("{{PHONE_bcdfgh7}}")) // 后缀含数字
	assert.False(t, IsPlaceholder("{{phone_bcdfgh}}"))  // 标签非大写
	assert.False(t, IsPlaceholder("{{_bcdfgh}}"))       // 标签空
	assert.False(t, IsPlaceholder(""))
}

func TestPlaceholderRe_CaptureGroups(t *testing.T) {
	for _, tc := range []struct {
		value string
		label string
	}{
		{"{{REDACTED_PHONE_NUMBER_bcdfgh}}", "REDACTED_PHONE_NUMBER"},
		{"{{PHONE_bcdfgh}}", "PHONE"},
	} {
		m := PlaceholderRe.FindStringSubmatch(tc.value)
		assert.Len(t, m, 3, "须捕获 label 与 suffix 两组")
		assert.Equal(t, tc.label, m[1])
		assert.Equal(t, "bcdfgh", m[2])
	}
}
