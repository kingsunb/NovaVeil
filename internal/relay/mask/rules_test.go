package mask

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ruleByLabel 按标签取内置规则(测试辅助)。
func ruleByLabel(t *testing.T, label string) Rule {
	t.Helper()
	for _, r := range BuiltinRules {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("规则 %q 未找到", label)
	return Rule{}
}

// makeJWT 用给定 header JSON 构造一个三段式 JWT 字符串(测试辅助)。
func makeJWT(t *testing.T, headerJSON string) string {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))
	p := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return h + "." + p + "." + sig
}

func TestRule_PrivateKey(t *testing.T) {
	r := ruleByLabel(t, "PRIVATE_KEY")
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0123456789abcdef\n-----END RSA PRIVATE KEY-----"
	assert.NotEmpty(t, r.Pattern.FindString(pem), "PEM 私钥应匹配")
	assert.Empty(t, r.Pattern.FindString("just some text with no key"))
}

func TestRule_APIKey(t *testing.T) {
	r := ruleByLabel(t, "API_KEY")
	assert.NotEmpty(t, r.Pattern.FindString("token is ghp_"+repeat("a", 30)), "ghp_ 前缀应匹配")
	assert.NotEmpty(t, r.Pattern.FindString("AIza"+repeat("b", 30)), "AIza 前缀应匹配")
	assert.NotEmpty(t, r.Pattern.FindString("xoxp-"+repeat("c", 20)), "xoxp- 前缀应匹配")
	assert.NotEmpty(t, r.Pattern.FindString("sk-"+repeat("d", 30)), "sk- 前缀应匹配")
	assert.Empty(t, r.Pattern.FindString("ghp_short"), "过短不匹配")
	assert.Empty(t, r.Pattern.FindString("aghp_"+repeat("a", 30)), "词中嵌 ghp_ 不应匹配(\\b 边界)")
}

func TestRule_ConnStr(t *testing.T) {
	r := ruleByLabel(t, "CONNSTR")
	m := r.Pattern.FindStringSubmatch("postgres://user:secretpw@host:5432/db")
	assert.NotNil(t, m, "连接串应匹配")
	assert.Equal(t, "secretpw", m[1], "应只捕获密码组")
	assert.Empty(t, r.Pattern.FindString("https://example.com/path"), "无密码连接串不匹配")
}

func TestRule_Phone(t *testing.T) {
	r := ruleByLabel(t, "PHONE")
	assert.NotEmpty(t, r.Pattern.FindString("call 13800138000 now"), "11 位手机号匹配")
	assert.NotEmpty(t, r.Pattern.FindString("+8613800138000"), "+86 前缀匹配")
	assert.NotEmpty(t, r.Pattern.FindString("138-1234-5678"), "分隔符格式匹配")
	assert.Empty(t, r.Pattern.FindString("11111111111"), "全同号不匹配(1[3-9] 拒第二位 1)")
}

func TestRule_Email(t *testing.T) {
	r := ruleByLabel(t, "EMAIL")
	assert.NotEmpty(t, r.Pattern.FindString("contact alice@example.com"))
	assert.Empty(t, r.Pattern.FindString("no at sign here"))
}

func TestRule_IDCard(t *testing.T) {
	r := ruleByLabel(t, "IDCARD")
	assert.NotEmpty(t, r.Pattern.FindString("id 110101199003078857 end"), "合法身份证匹配")
	assert.Empty(t, r.Pattern.FindString("12345"), "过短不匹配")
}

func TestRule_Card(t *testing.T) {
	r := ruleByLabel(t, "CARD")
	assert.NotEmpty(t, r.Pattern.FindString("card 4111111111111111 ok"), "16 位卡号匹配")
	assert.Empty(t, r.Pattern.FindString("123"), "过短不匹配")
}

func TestRule_Secret(t *testing.T) {
	r := ruleByLabel(t, "SECRET")
	m := r.Pattern.FindStringSubmatch("password=abc123")
	assert.NotNil(t, m, "password= 赋值应匹配")
	assert.Equal(t, "abc123", m[1], "应只捕获值组")
	m2 := r.Pattern.FindStringSubmatch("密码: mypw998")
	assert.NotNil(t, m2, "中文关键词应匹配")
	assert.Equal(t, "mypw998", m2[1])
	assert.Empty(t, r.Pattern.FindString("no secret keyword value"), "无关键词不匹配")
}

func TestRule_JWT(t *testing.T) {
	r := ruleByLabel(t, "JWT")
	valid := makeJWT(t, `{"alg":"HS256","typ":"JWT"}`)
	assert.NotEmpty(t, r.Pattern.FindString("auth "+valid), "JWT 三段式匹配")
	assert.Empty(t, r.Pattern.FindString("not.a.jwt"), "非 eyJ 开头不匹配")
}

func TestRule_Token(t *testing.T) {
	r := ruleByLabel(t, "TOKEN")
	m := r.Pattern.FindStringSubmatch("Authorization: Bearer abc12345_xyz")
	assert.NotNil(t, m, "Bearer 应匹配")
	assert.Equal(t, "abc12345_xyz", m[1], "应只捕获 token 值")
	assert.Empty(t, r.Pattern.FindString("no bearer here"))
}

// ---- 校验函数 ----

func TestLuhnOK(t *testing.T) {
	assert.True(t, luhnOK("4111111111111111"), "Visa 测试卡 Luhn 合法")
	assert.True(t, luhnOK("4242424242424242"))
	assert.False(t, luhnOK("4111111111111112"), "改末位 Luhn 非法")
	assert.False(t, luhnOK("12345"), "过短非法")
}

func TestIDCard18OK(t *testing.T) {
	assert.True(t, idcard18OK("110101199003078857"), "合法身份证(北京 1990-03-07)")
	assert.False(t, idcard18OK("110101199003078858"), "校验位错误")
	assert.False(t, idcard18OK("990101199003078857"), "非法省份 99")
	assert.False(t, idcard18OK("110101199002300000"), "非法日期 2 月 30 日")
	assert.False(t, idcard18OK("12345"), "长度不足")
}

func TestPhoneOK(t *testing.T) {
	assert.True(t, phoneOK("13800138000"))
	assert.True(t, phoneOK("+8613800138000"))
	assert.True(t, phoneOK("138-1234-5678"))
	assert.True(t, phoneOK("19800138000"), "第二位 9 合法号段")
	assert.False(t, phoneOK("11111111111"), "全同号")
	assert.False(t, phoneOK("12000138000"), "第二位 2 非法号段(须 3-9)")
	assert.False(t, phoneOK("12345"), "长度不足")
}

func TestEmailOK(t *testing.T) {
	assert.True(t, emailOK("alice@example.com"))
	assert.True(t, emailOK("a.b+c@d.co"))
	assert.False(t, emailOK("plaintext"))
	assert.False(t, emailOK("a@b"), "域名过短")
	assert.False(t, emailOK("a@.com"), "空 TLD 段")
	assert.False(t, emailOK("a..b@example.com"), "连续双点")
}

func TestJWTOK(t *testing.T) {
	valid := makeJWT(t, `{"alg":"HS256","typ":"JWT"}`)
	assert.True(t, jwtOK(valid), "含 alg 的 header 合法")
	noAlg := makeJWT(t, `{"foo":"bar"}`)
	assert.False(t, jwtOK(noAlg), "无 alg 非法")
	assert.False(t, jwtOK("not-a-jwt"))
}

func TestSecretOK(t *testing.T) {
	assert.True(t, secretOK("abc123"), "含数字通过")
	assert.True(t, secretOK("abc!def"), "含符号通过")
	assert.False(t, secretOK("abcdef"), "纯字母拒绝")
}

func TestRule_MAC(t *testing.T) {
	r := ruleByLabel(t, "MAC")
	assert.NotEmpty(t, r.Pattern.FindString("mac 00:1A:2B:3C:4D:5E end"), "冒号格式整匹配")
	assert.NotEmpty(t, r.Pattern.FindString("mac 00-1A-2B-3C-4D-5E end"), "连字符格式整匹配")
	assert.NotEmpty(t, r.Pattern.FindString("mac 001A.2B3C.4D5E end"), "点三分组格式整匹配")
	assert.Empty(t, r.Pattern.FindString("abc00:1A:2B:3C:4D:5E"), "前接字母数字不命中(\\b 边界)")
	assert.Empty(t, r.Pattern.FindString("00:1A:2B:3C:4D:5Ef"), "后接字母数字不命中(\\b 边界)")
}

func TestRule_USCC(t *testing.T) {
	r := ruleByLabel(t, "USCC")
	assert.NotEmpty(t, r.Pattern.FindString("code 913100007757804495 end"), "18 位限字符集匹配")
	assert.Empty(t, r.Pattern.FindString("91310000775780449I"), "含禁用字符 I 不命中")
	assert.Empty(t, r.Pattern.FindString("91310000775780449"), "长度不足不命中")
	assert.Empty(t, r.Pattern.FindString("9131000077578044951"), "长度超过 18 不命中(\\b 边界)")
}

// makeUSCC 用给定前 17 位自动补一个合法校验位(测试辅助)。31 个候选校验位中恰有一者通过。
func makeUSCC(front17 string) string {
	for _, c := range usccAlphabet {
		if usccOK(front17 + string(c)) {
			return front17 + string(c)
		}
	}
	return front17 + "0"
}

func TestUsccOK(t *testing.T) {
	assert.True(t, usccOK("913100007757804495"), "真实合法信用代码(校验位 5)")
	assert.True(t, usccOK(makeUSCC("91350100M000100Y4")), "自生成校验位样例通过")
	assert.False(t, usccOK("913100007757804496"), "篡改末位校验位不匹配")
	assert.False(t, usccOK("91310000775780449I"), "含禁用字符非法")
	assert.False(t, usccOK("91310000775780449"), "长度不足非法")
	assert.False(t, usccOK("9131000077578044951"), "长度超 18 非法")
	assert.False(t, usccOK("91310000775780449O"), "含禁用字符 O 非法")
}

func TestBuiltinRuleMeta_AllDefaultOff(t *testing.T) {
	// 硬约束: 所有内置规则默认关闭。
	for _, m := range BuiltinRuleMeta {
		assert.False(t, m.DefaultEnabled, "规则 %q 必须默认关闭", m.Label)
	}
	// 元信息标签与规则标签一致。
	metaLabels := make(map[string]bool)
	for _, m := range BuiltinRuleMeta {
		metaLabels[m.Label] = true
	}
	for _, r := range BuiltinRules {
		assert.True(t, metaLabels[r.Label], "规则 %q 缺少元信息", r.Label)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
