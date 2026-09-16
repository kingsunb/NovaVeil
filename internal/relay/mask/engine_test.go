package mask

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enable 构造 enabledRules map(测试辅助)。
func enable(labels ...string) map[string]bool {
	m := make(map[string]bool, len(labels))
	for _, l := range labels {
		m[l] = true
	}
	return m
}

func TestEngine_CustomTermsWholeWord(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("the cat sat on the category", "s1", enable(), []CustomTerm{{Value: "cat"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{TERM_", "cat 应被整词替换")
	assert.Contains(t, res.Masked, "category", "category 不应被替换(词边界)")
	assert.NotContains(t, res.Masked, " cat ")
	// 还原往返。
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "the cat sat on the category", restored)
}

func TestEngine_CustomTermsChineseNoBoundary(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 中文词无 \\b 边界, 字面匹配。
	res, err := e.Apply("我叫张三, 张三丰是另一个人", "s1", enable(), []CustomTerm{{Value: "张三"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{TERM_")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "我叫张三, 张三丰是另一个人", restored)
}

func TestEngine_CustomTermsPriorityOverRegex(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 自定义词优先于正则: 手机号作为自定义词应得 TERM 占位符, 不被 PHONE 规则再脱。
	res, err := e.Apply("call 13800138000", "s1", enable("PHONE"), []CustomTerm{{Value: "13800138000"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{TERM_", "自定义词优先, 标签为 TERM")
	assert.NotContains(t, res.Masked, "{{PHONE_", "不应被 PHONE 规则二次脱敏")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "call 13800138000", restored)
}

func TestEngine_CustomTermsLongFirst(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 长词优先: "category" 应整体替换, 不被 "cat" 拆碎。
	res, err := e.Apply("the category", "s1", enable(), []CustomTerm{{Value: "cat"}, {Value: "category"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{TERM_")
	assert.NotContains(t, res.Masked, "category")
	assert.NotContains(t, res.Masked, "egory", "不应残留 category 被拆后的尾巴")
}

func TestEngine_PlaceholdersConsistentWithinSession(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("a 13800138000 b 13800138000 c", "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	// 同一原文两次出现应映射为同一占位符。
	phs := PlaceholderRe.FindAllString(res.Masked, -1)
	require.Len(t, phs, 2, "应有两个占位符")
	assert.Equal(t, phs[0], phs[1], "两次出现映射为同一占位符")
	// 去重后只记录一次命中。
	phoneMatches := 0
	for _, m := range res.Matches {
		if m.Label == "PHONE" {
			phoneMatches++
		}
	}
	assert.Equal(t, 1, phoneMatches, "去重后只记录一次 PHONE 命中")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "a 13800138000 b 13800138000 c", restored)
}

func TestEngine_AntiNesting_PlaceholderNotReMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 第一轮: 脱敏手机号。
	res1, err := e.Apply("13800138000", "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	ph := PlaceholderRe.FindString(res1.Masked)
	require.NotEmpty(t, ph)

	// 第二轮: 文本含上一轮占位符 + 新手机号。占位符应原样保留, 不被二次脱敏。
	res2, err := e.Apply(ph+" and 13900139000", "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Masked, ph, "已有占位符原样保留")
	restored := RestoreString(res2.Masked, res2.Mapping)
	assert.Equal(t, "13800138000 and 13900139000", restored, "还原后两手机号均回原文")
}

func TestEngine_ConnStr_OnlyPasswordMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("db: postgres://user:secretpw@host:5432/x", "s1", enable("CONNSTR"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "postgres://user:", "scheme/user 保留")
	assert.Contains(t, res.Masked, "@host:5432/x", "host 保留")
	assert.Contains(t, res.Masked, "{{CONNSTR_", "密码被占位符替换")
	assert.NotContains(t, res.Masked, "secretpw", "密码不应残留")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "db: postgres://user:secretpw@host:5432/x", restored)
}

func TestEngine_Secret_ValidatorRejectsPureAlpha(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 含数字的值应脱敏。
	res, err := e.Apply("password=abc123", "s1", enable("SECRET"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "password={{SECRET_")
	// 纯字母值(方法名)应被校验函数拒绝, 不脱敏。
	res2, err := e.Apply("token=getUserInfo", "s2", enable("SECRET"), nil)
	require.NoError(t, err)
	assert.Equal(t, "token=getUserInfo", res2.Masked, "纯字母值不脱敏")
}

func TestEngine_NoRulesEnabled_Passthrough(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("13800138000 alice@example.com", "s1", enable(), nil)
	require.NoError(t, err)
	assert.Equal(t, "13800138000 alice@example.com", res.Masked, "无规则开启时原样透传")
}

func TestEngine_FailClosedOnPanic(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 注入 Pattern 为 nil 的非法规则, 触发 panic。
	e.rules = append([]Rule{{Label: "BAD", Pattern: nil, Group: 0}}, e.rules...)
	res, err := e.Apply("hello world", "s1", enable("BAD"), nil)
	assert.Error(t, err, "panic 须转为 error(fail-closed)")
	assert.Empty(t, res.Masked, "半成品须清空, 绝不放行")
}

func TestEngine_ApplyBytes(t *testing.T) {
	e := NewEngine(NewSessionStore())
	out, mapping, err := e.ApplyBytes([]byte("call 13800138000"), "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	assert.NotEmpty(t, out)
	assert.NotNil(t, mapping)
	assert.False(t, strings.Contains(string(out), "13800138000"))
	restored := RestoreBytes(out, mapping)
	assert.Equal(t, "call 13800138000", string(restored))
}

func TestEngine_EmptyBody(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("", "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	assert.Empty(t, res.Masked)
}

func TestEngine_MAC(t *testing.T) {
	e := NewEngine(NewSessionStore())
	const mac = "00:1A:2B:3C:4D:5E"

	// 开关关闭: 原样保留。
	res, err := e.Apply("mac "+mac+" end", "s1", enable(), nil)
	require.NoError(t, err)
	assert.Equal(t, "mac "+mac+" end", res.Masked, "开关关闭原样透传")

	// 开关开启: 整体替换为占位符, 命中明细齐全, 还原往返一致。
	res2, err := e.Apply("mac "+mac+" end", "s2", enable("MAC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Masked, "{{MAC_")
	assert.NotContains(t, res2.Masked, mac)
	var m *Match
	for i := range res2.Matches {
		if res2.Matches[i].Label == "MAC" {
			m = &res2.Matches[i]
			break
		}
	}
	require.NotNil(t, m, "应存在 MAC 命中明细")
	assert.Equal(t, mac, m.Original)
	restored := RestoreString(res2.Masked, res2.Mapping)
	assert.Equal(t, "mac "+mac+" end", restored)
}

func TestEngine_MAC_ConsistentWithinSession(t *testing.T) {
	e := NewEngine(NewSessionStore())
	const mac = "00:1A:2B:3C:4D:5E"
	res, err := e.Apply("a "+mac+" b "+mac+" c", "s1", enable("MAC"), nil)
	require.NoError(t, err)
	phs := PlaceholderRe.FindAllString(res.Masked, -1)
	require.Len(t, phs, 2, "两处出现应各替换为一个占位符")
	assert.Equal(t, phs[0], phs[1], "同一 MAC 映射为同一占位符")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "a "+mac+" b "+mac+" c", restored)
}

func TestEngine_USCC(t *testing.T) {
	e := NewEngine(NewSessionStore())
	const code = "913100007757804495"

	// 开关关闭: 原样保留。
	res, err := e.Apply("code "+code+" end", "s1", enable(), nil)
	require.NoError(t, err)
	assert.Equal(t, "code "+code+" end", res.Masked)

	// 开关开启: 替换为占位符, 还原往返一致。
	res2, err := e.Apply("code "+code+" end", "s2", enable("USCC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Masked, "{{USCC_")
	assert.NotContains(t, res2.Masked, code)
	restored := RestoreString(res2.Masked, res2.Mapping)
	assert.Equal(t, "code "+code+" end", restored)

	// 校验位非法(篡改末位): 不命中, 原样保留。
	res3, err := e.Apply("code 913100007757804496 end", "s3", enable("USCC"), nil)
	require.NoError(t, err)
	assert.Equal(t, "code 913100007757804496 end", res3.Masked, "校验位非法不脱敏")
	for _, m := range res3.Matches {
		assert.NotEqual(t, "USCC", m.Label, "非法信用代码不应出现在命中明细")
	}
}
