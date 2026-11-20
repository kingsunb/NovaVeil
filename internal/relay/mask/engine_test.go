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
	assert.Contains(t, res.Masked, "{{REDACTED_CUSTOM_SENSITIVE_TEXT_", "cat 应被整词替换")
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
	assert.Contains(t, res.Masked, "{{REDACTED_CUSTOM_SENSITIVE_TEXT_")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "我叫张三, 张三丰是另一个人", restored)
}

func TestEngine_CustomTermsPriorityOverRegex(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 自定义词优先于正则: 手机号作为自定义词应得 TERM 占位符, 不被 PHONE 规则再脱。
	res, err := e.Apply("call 13800138000", "s1", enable("PHONE"), []CustomTerm{{Value: "13800138000"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{REDACTED_CUSTOM_SENSITIVE_TEXT_", "自定义词优先, 标签为 TERM")
	assert.NotContains(t, res.Masked, "{{REDACTED_PHONE_NUMBER_", "不应被 PHONE 规则二次脱敏")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "call 13800138000", restored)
}

func TestEngine_CustomTermsLongFirst(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 长词优先: "category" 应整体替换, 不被 "cat" 拆碎。
	res, err := e.Apply("the category", "s1", enable(), []CustomTerm{{Value: "cat"}, {Value: "category"}})
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "{{REDACTED_CUSTOM_SENSITIVE_TEXT_")
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
	assert.Contains(t, res.Masked, "{{REDACTED_CONNECTION_PASSWORD_", "密码被占位符替换")
	assert.NotContains(t, res.Masked, "secretpw", "密码不应残留")
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "db: postgres://user:secretpw@host:5432/x", restored)
}

func TestEngine_Secret_ValidatorRejectsPureAlpha(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 含数字的值应脱敏。
	res, err := e.Apply("password=abc123", "s1", enable("SECRET"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "password={{REDACTED_CREDENTIAL_")
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
	assert.Contains(t, res2.Masked, "{{REDACTED_MAC_ADDRESS_")
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
	assert.Contains(t, res2.Masked, "{{REDACTED_CN_UNIFIED_SOCIAL_CREDIT_CODE_")
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
		assert.Contains(t, res.Masked, "{{REDACTED_API_KEY_")
	}
}

func TestEngine_MAC_IPv6NotTruncated(t *testing.T) {
	e := NewEngine(NewSessionStore())
	// 冒号链 {5,} 贪婪 + macOK 恰 6 组校验: 8 组 IPv6 整链被拒, 不再截取前 6 组。
	res, err := e.Apply("addr 11:22:33:44:55:66:77:88 end", "s1", enable("MAC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "11:22:33:44:55:66:77:88", "IPv6 应原样保留")
	assert.NotContains(t, res.Masked, "{{REDACTED_MAC_ADDRESS_")

	// 恰 6 组的正常 MAC 不受影响。
	res2, err := e.Apply("mac 00:1A:2B:3C:4D:5E end", "s2", enable("MAC"), nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Masked, "{{REDACTED_MAC_ADDRESS_", "正常 MAC 仍应脱敏")
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

// TestCustomTermTableDropsRemovedTerms 验证词集变化后整表重建:
// 废弃词的正则不残留, 新词可用, 未变更词复用同一编译结果。
func TestCustomTermTableDropsRemovedTerms(t *testing.T) {
	first := customTermTable([]CustomTerm{{Value: "cat"}, {Value: "张三"}})
	require.Contains(t, first, "cat")
	require.Contains(t, first, "张三")

	second := customTermTable([]CustomTerm{{Value: "dog"}, {Value: "张三"}})
	assert.NotContains(t, second, "cat", "废弃词应从新表移除")
	assert.Contains(t, second, "dog")
	assert.Contains(t, second, "张三")
	assert.Same(t, first["张三"], second["张三"], "未变更词应复用编译结果")

	current := *customTermRegexCache.Load()
	assert.NotContains(t, current, "cat", "全局缓存应随重建废弃旧词")
	assert.Contains(t, current, "dog")

	// 词集未变时返回同一张表, 不重复编译。
	again := customTermTable([]CustomTerm{{Value: "dog"}, {Value: "张三"}})
	assert.Same(t, second["dog"], again["dog"])
}

// TestEngine_IP_ExtraOctetNotMasked 五段点分串不得被截成合法 IPv4 脱敏。
func TestEngine_IP_ExtraOctetNotMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	address := strings.Join([]string{"93", "184", "216", "34"}, ".")
	masked, err := e.Apply("公网 "+address, "public-ip", enable("IP"), nil)
	require.NoError(t, err)
	assert.NotContains(t, masked.Masked, address, "公网 IPv4 应被脱敏")
	assert.Contains(t, masked.Masked, "{{REDACTED_PUBLIC_IPV4_ADDRESS_", "占位符应说明完整类型")

	for _, invalid := range []string{address + ".5", "x" + address, address + "x", "v" + address} {
		res, err := e.Apply(invalid, "invalid-"+invalid, enable("IP"), nil)
		require.NoError(t, err)
		assert.Equal(t, invalid, res.Masked, "相邻字段不能截半识别: %s", invalid)
	}

	for _, version := range []string{
		"version=" + address, "build " + address, "release: " + address,
		"版本号 " + address, "构建版本：" + address, "Chrome/" + address, "v13.37.42.7", "v " + address,
	} {
		res, err := e.Apply(version, "version-"+version, enable("IP"), nil)
		require.NoError(t, err)
		assert.Equal(t, version, res.Masked, "版本号不应识别为公网 IP: %s", version)
	}

	mixed := "version=" + address + " server=" + address
	res, err := e.Apply(mixed, "mixed-version-and-ip", enable("IP"), nil)
	require.NoError(t, err)
	assert.Contains(t, res.Masked, "version="+address, "版本字段应保留")
	assert.Contains(t, res.Masked, "{{REDACTED_PUBLIC_IPV4_ADDRESS_", "同一行的真实公网 IP 仍应脱敏")
	assert.NotContains(t, res.Masked, "server="+address, "公网 IP 原文不应残留")

	for _, quoted := range []string{
		"version=\"" + address + "\"", "version = '" + address + "'", "构建版本：\"" + address + "\"",
	} {
		res, err := e.Apply(quoted, "quoted-version-"+quoted, enable("IP"), nil)
		require.NoError(t, err)
		assert.Equal(t, quoted, res.Masked, "带引号版本号不应识别为公网 IP: %s", quoted)
	}

	jsonBody := `{"version":"` + address + `","server":"` + address + `"}`
	resJSON, err := e.Apply(jsonBody, "json-version-and-ip", enable("IP"), nil)
	require.NoError(t, err)
	assert.Contains(t, resJSON.Masked, `"version":"`+address+`"`, "JSON 版本字段应保留")
	assert.Contains(t, resJSON.Masked, "{{REDACTED_PUBLIC_IPV4_ADDRESS_", "JSON 真实公网 IP 仍应脱敏")
	assert.NotContains(t, resJSON.Masked, `"server":"`+address+`"`, "JSON 公网 IP 原文不应残留")
}

func TestEngine_IP_SentencePunctuation(t *testing.T) {
	address := strings.Join([]string{"93", "184", "216", "34"}, ".")
	for _, suffix := range []string{".", ". Next sentence", ".)", ".\n", "。"} {
		t.Run(suffix, func(t *testing.T) {
			e := NewEngine(NewSessionStore())
			body := "Server: " + address + suffix
			res, err := e.Apply(body, "sentence-ip", enable("IP"), nil)
			require.NoError(t, err)
			require.Len(t, res.Matches, 1)
			assert.Equal(t, address, res.Matches[0].Original)
			assert.Equal(t, "Server: "+res.Matches[0].Placeholder+suffix, res.Masked)
			assert.Equal(t, body, RestoreString(res.Masked, res.Mapping))
		})
	}

	for _, body := range []string{
		address + ".5", address + ".5555", address + ".suffix", address + "._suffix",
		"_" + address, address + "_", "version=" + address + ".",
	} {
		t.Run(body, func(t *testing.T) {
			e := NewEngine(NewSessionStore())
			res, err := e.Apply(body, "adjacent-ip", enable("IP"), nil)
			require.NoError(t, err)
			assert.Equal(t, body, res.Masked)
			assert.Empty(t, res.Matches)
		})
	}
}

func TestEngine_IP_ContextWindow(t *testing.T) {
	address := strings.Join([]string{"93", "184", "216", "34"}, ".")
	for _, tc := range []struct {
		name       string
		key        string
		prefix     string
		wantMasked bool
	}{
		{name: "first_line_key", key: "version"},
		{name: "key_at_window_edge", key: "version", prefix: strings.Repeat(" ", ipv4ContextWindow-len("version: "))},
		{name: "key_cut_by_window", key: "version", prefix: strings.Repeat(" ", ipv4ContextWindow-len("version: ")+1), wantMasked: true},
		{name: "key_outside_window", key: "version", prefix: strings.Repeat(" ", ipv4ContextWindow), wantMasked: true},
		{name: "newline_stops_key", key: "version", prefix: "\n", wantMasked: true},
		{name: "newline_outside_window", key: "version", prefix: "\n" + strings.Repeat(" ", ipv4ContextWindow), wantMasked: true},
		{name: "local_version_after_newline", key: "server", prefix: "text\nversion="},
		{name: "local_version_after_long_prefix", key: "server", prefix: strings.Repeat("x", 4096) + " version="},
		{name: "version_on_previous_line", key: "server", prefix: "version=\n", wantMasked: true},
		{name: "long_key_tail", key: strings.Repeat("x", 4096) + " version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(NewSessionStore())
			body, err := json.Marshal(map[string]string{tc.key: tc.prefix + address})
			require.NoError(t, err)
			res, err := e.Apply(string(body), tc.name, enable("IP"), nil)
			require.NoError(t, err)
			if tc.wantMasked {
				require.Len(t, res.Matches, 1)
				assert.NotContains(t, res.Masked, address)
			} else {
				assert.Empty(t, res.Matches)
				assert.Equal(t, string(body), res.Masked)
			}
			assert.Equal(t, string(body), RestoreString(res.Masked, res.Mapping))
		})
	}
}

func TestEngine_IP_LongSingleLine(t *testing.T) {
	e := NewEngine(NewSessionStore())
	address := strings.Join([]string{"93", "184", "216", "34"}, ".")
	const occurrences = 4096
	body := "version=" + address + strings.Repeat(" server="+address+".", occurrences)
	res, err := e.Apply(body, "long-ip-line", enable("IP"), nil)
	require.NoError(t, err)
	require.Len(t, res.Matches, 1)
	assert.Equal(t, occurrences, strings.Count(res.Masked, res.Matches[0].Placeholder))
	assert.Equal(t, 1, strings.Count(res.Masked, address))
	assert.Equal(t, body, RestoreString(res.Masked, res.Mapping))
}

func BenchmarkEngine_IP_LongSingleLine(b *testing.B) {
	address := strings.Join([]string{"93", "184", "216", "34"}, ".")
	for _, tc := range []struct {
		name        string
		occurrences int
	}{
		{name: "128", occurrences: 128},
		{name: "4096", occurrences: 4096},
	} {
		b.Run(tc.name, func(b *testing.B) {
			e := NewEngine(NewSessionStore())
			body := strings.Repeat("server="+address+" ", tc.occurrences)
			rules := enable("IP")
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.Apply(body, "benchmark-ip-line", rules, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestEngine_SSN_AdjacentDigitsNotMasked 相邻数字不得被截成 SSN 形状脱敏。
func TestEngine_SSN_AdjacentDigitsNotMasked(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("编号 321-54-9876 555-12-3456", "s1", enable("SSN"), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(res.Masked, "{{REDACTED_US_SSN_"), "相邻两条 SSN 都应识别")

	res2, err := e.Apply("订单 2024-01-2345", "s2", enable("SSN"), nil)
	require.NoError(t, err)
	assert.Equal(t, "订单 2024-01-2345", res2.Masked, "日期串不得截半脱敏")
}
