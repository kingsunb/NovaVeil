package mask

import (
	"encoding/json"
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
	// 91330100799655058B 为真实在册统一社会信用代码(阿里巴巴), 校验位 B 经 USCC 算法验证通过。
	const code = "91330100799655058B"

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

	// 校验位非法(篡改合法样例末位): 不命中, 原样保留。
	res3, err := e.Apply("code 91330100799655058A end", "s3", enable("USCC"), nil)
	require.NoError(t, err)
	assert.Equal(t, "code 91330100799655058A end", res3.Masked, "校验位非法不脱敏")
	for _, m := range res3.Matches {
		assert.NotEqual(t, "USCC", m.Label, "非法信用代码不应出现在命中明细")
	}
}

func TestEngine_APIKey_StripePrefixMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 预检 apiPrefixes 必须覆盖正则支持的 Stripe/Restricted 前缀,
	// 否则纯 sk_live_ 文本会被预检整条跳过而明文透出。
	for _, prefix := range []string{"sk_live_", "sk_test_", "rk_live_"} {
		// 完整假密钥由前缀运行时拼接, 源码不出现完整密钥字面量,
		// 避免 GitHub push protection 把占位样例当真实凭据拦截推送。
		key := prefix + "123456789012345678901234"
		res, err := e.Apply("key "+key+" ok", "s1", enable("API_KEY"), nil)
		require.NoError(t, err)
		assert.NotContains(t, res.Masked, key, "凭据前缀应被脱敏(预检不得漏前缀)")
		assert.Contains(t, res.Masked, "{{APIKEY_")
	}
}

func TestEngine_MAC_IPv6NotTruncated(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 冒号链 {5,} 贪婪 + macOK 恰 6 组校验: 8 组 IPv6 整链被拒, 不再截取前 6 组。
	res, err := e.Apply("addr 11:22:33:44:55:66:77:88 end", "s1", enable("MAC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "11:22:33:44:55:66:77:88", "IPv6 应原样保留")
	assert.NotContains(t, res.Masked, "{{MAC_")

	// 恰 6 组的正常 MAC 不受影响。
	res2, err := e.Apply("mac 00:1A:2B:3C:4D:5E end", "s2", enable("MAC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Masked, "{{MAC_", "正常 MAC 仍应脱敏")
	assert.NotContains(t, res2.Masked, "00:1A:2B:3C:4D:5E")
}

func TestEngine_Overlap_LongestCandidateWins(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 同起点: CONNSTR 完整密码(含 .tail)应整体覆盖, 不能让先序 API_KEY 的
	// 短命中截短后残留 .tail。
	res, err := e.Apply("postgres://u:sk-aaaaaaaaaaaaaaaaaaaa.tail@host", "s1", enable("API_KEY", "CONNSTR"), nil)
	require.NoError(t, err)
	assert.NotContains(t, res.Masked, ".tail", "密码尾部不应残留")
	assert.NotContains(t, res.Masked, "aaaaaaaa", "密码不应明文残留")

	// JWT 与 SECRET 同起点: 整串应被一次替换, 后两段不得残留。
	res2, err := e.Apply("token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl", "s2", enable("SECRET", "JWT"), nil)
	require.NoError(t, err)
	assert.NotContains(t, res2.Masked, "eyJzdWIiOiJ4In0", "JWT 载荷不应残留")
	assert.NotContains(t, res2.Masked, "c2lnbmF0dXJl", "JWT 签名不应残留")
}

func TestEngine_JSONStringValuesMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// JSON 转义引号: 解码后的字符串值才与上游读取内容一致, 扫描必须走解码值。
	body := `{"messages":[{"role":"user","content":"password=\"abc123\""}]}`
	res, err := e.Apply(body, "s1", enable("SECRET"), nil)
	require.NoError(t, err)
	assert.NotContains(t, res.Masked, "abc123", "JSON 转义凭据应被脱敏")
	assert.True(t, json.Valid([]byte(res.Masked)), "脱敏后应保持合法 JSON")
	restored := RestoreString(res.Masked, res.Mapping)
	// 已知限制: 还原是纯文本替换, 不按 JSON 重新转义(与 PRIVATE_KEY 含换行同类),
	// 引号原样回填会破坏响应 JSON 转义, 但机密内容本身已完整还原。
	assert.Contains(t, restored, `password="abc123"`, "机密内容应被还原")

	// JSON unicode 转义手机号(原文无 JSON 特殊字符): 语义往返。
	// JSON 感知扫描会规范化转义形态(\u0031 → 字面字符), 故按解码值而非原文字节断言。
	body2 := `{"content":"\u0031\u0033\u0038\u0030\u0030\u0031\u0033\u0038\u0030\u0030\u0030"}`
	res2, err := e.Apply(body2, "s2", enable("PHONE"), nil)
	require.NoError(t, err)
	assert.NotContains(t, res2.Masked, "13800138000", "JSON unicode 手机号应被脱敏")
	assert.True(t, json.Valid([]byte(res2.Masked)), "脱敏后应保持合法 JSON")
	restored2 := RestoreString(res2.Masked, res2.Mapping)
	assert.True(t, json.Valid([]byte(restored2)), "还原后应保持合法 JSON")
	var back map[string]any
	require.NoError(t, json.Unmarshal([]byte(restored2), &back))
	assert.Equal(t, "13800138000", back["content"], "还原后语义应与原文一致")
}
