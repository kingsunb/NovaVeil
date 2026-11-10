package op

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// STA-04 回归: 截断破坏日志结构化脱敏。
// 验证字段正则覆盖 JSON key 结束双引号、结构化/兜底双路径脱敏、引号/转义/嵌套/不完整值,
// 以及端到端 ErrorLogEnqueue→落库→API 读取链路中原占位值不泄漏。

// TestFieldSecretPatternMatchesJSONKey 直接验证修正后的字段正则能命中 JSON key 形态。
// 修复前分隔符组只接受 \s*[:=]\s*, "api_key":"..." 因结束双引号挡在冒号前而漏匹配。
func TestFieldSecretPatternMatchesJSONKey(t *testing.T) {
	cases := []string{
		`"api_key":"PLACEHOLDER_VALUE"`,
		`"password":"PLACEHOLDER_VALUE"`,
		`"x-api-key":"PLACEHOLDER_VALUE"`,
		`"access_token":"PLACEHOLDER_VALUE"`,
		`"client_secret":"PLACEHOLDER_VALUE"`,
	}
	for _, c := range cases {
		got := redactSensitiveText(c)
		if strings.Contains(got, "PLACEHOLDER_VALUE") {
			t.Fatalf("JSON key 形态未被脱敏: %q -> %q", c, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("脱敏结果应含占位符: %q -> %q", c, got)
		}
	}
}

// TestRedactRequestBodyCompleteJSON 走结构化路径: 完整 JSON 解码成功,
// 递归脱敏敏感字段后重编码为合法 JSON, 非敏感字段保留。
func TestRedactRequestBodyCompleteJSON(t *testing.T) {
	const apiKey = "PLACEHOLDER_API_KEY_SMALL"
	const password = "PLACEHOLDER_PASSWORD_SMALL"
	body := `{"api_key":"` + apiKey + `","password":"` + password + `","model":"gpt","nested":{"token":"PLACEHOLDER_TOKEN"}}`
	got := redactRequestBody(body)
	if strings.Contains(got, apiKey) || strings.Contains(got, password) || strings.Contains(got, "PLACEHOLDER_TOKEN") {
		t.Fatalf("完整 JSON 脱敏后仍含占位值: %s", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("脱敏后应是合法 JSON: %v (got: %s)", err, got)
	}
	if decoded["api_key"] != "[REDACTED]" || decoded["password"] != "[REDACTED]" {
		t.Fatalf("敏感字段应被替换为 [REDACTED]: %s", got)
	}
	if decoded["model"] != "gpt" {
		t.Fatalf("非敏感字段应保留: %s", got)
	}
	nested, ok := decoded["nested"].(map[string]any)
	if !ok || nested["token"] != "[REDACTED]" {
		t.Fatalf("嵌套敏感字段应被脱敏: %s", got)
	}
}

// TestRedactRequestBodyTruncatedAtQuote 截断落在引号内: 值无闭合引号,
// JSON 解码失败, 文本正则兜底的无引号分支应吞掉不完整值并脱敏。
func TestRedactRequestBodyTruncatedAtQuote(t *testing.T) {
	const secret = "PLACEHOLDER_SECRET_AT_QUOTE_BOUNDARY"
	body := `{"api_key":"` + secret // 无闭合引号, 模拟截断
	got := redactRequestBody(body)
	if strings.Contains(got, secret) {
		t.Fatalf("截断在引号处的 secret 泄漏: %s", got)
	}
}

// TestRedactRequestBodyTruncatedAtEscape 截断落在转义序列处:
// 值含转义引号且无闭合引号, 兜底正则仍应脱敏。
func TestRedactRequestBodyTruncatedAtEscape(t *testing.T) {
	const secret = `PLACEHOLDER_ESCAPED` + `\` + `"SECRET` // 含 \" 转义
	body := `{"password":"` + secret                       // 无闭合引号
	got := redactRequestBody(body)
	if strings.Contains(got, "PLACEHOLDER_ESCAPED") {
		t.Fatalf("截断在转义处的 secret 泄漏: %s", got)
	}
}

// TestRedactRequestBodyNestedTruncated 嵌套字段被截断: 结构化路径失败,
// 兜底正则仍命中嵌套层中的敏感字段名。
func TestRedactRequestBodyNestedTruncated(t *testing.T) {
	const secret = "PLACEHOLDER_NESTED_PASSWORD"
	body := `{"data":{"password":"` + secret // 嵌套且截断
	got := redactRequestBody(body)
	if strings.Contains(got, secret) {
		t.Fatalf("嵌套截断字段中的 secret 泄漏: %s", got)
	}
}

// TestRedactRequestBodyNonJSONText 非 JSON 文本(key=value / key: value 形态)兜底脱敏。
func TestRedactRequestBodyNonJSONText(t *testing.T) {
	const secret = "PLACEHOLDER_TEXT_SECRET"
	body := "api_key=" + secret + "\npassword: " + secret + "\nAuthorization: Bearer " + secret
	got := redactRequestBody(body)
	if strings.Contains(got, secret) {
		t.Fatalf("非 JSON 文本中的 secret 泄漏: %s", got)
	}
}

// TestRedactSensitiveTextJSONFragmentsInDetail ErrDetail/ErrBrief 常含上游响应体片段,
// 其中 JSON 形态的敏感字段同样需被脱敏(审计指出 ErrDetail/ErrBrief 有同类风险)。
func TestRedactSensitiveTextJSONFragmentsInDetail(t *testing.T) {
	const secret = "PLACEHOLDER_FRAGMENT_SECRET"
	fragment := `upstream 500: {"error":{"api_key":"` + secret + `","password":"` + secret + `"}}`
	got := redactSensitiveText(fragment)
	if strings.Contains(got, secret) {
		t.Fatalf("JSON 片段中的 secret 泄漏: %s", got)
	}
}

// TestErrorLogEnqueueRedactsSecretsEndToEnd 端到端: api_key/password 位于前部,
// messages 超 4096 字节使整体 JSON 超出入队截断上限; 经 ErrorLogEnqueue→落库→API 读取,
// 原占位值从未出现在持久化结果中。
func TestErrorLogEnqueueRedactsSecretsEndToEnd(t *testing.T) {
	ctx := errorLogTestContext(t)

	const apiKeyPlaceholder = "PLACEHOLDER_API_KEY_VALUE_12345"
	const passwordPlaceholder = "PLACEHOLDER_PASSWORD_VALUE_67890"

	// messages 内容使整体 JSON 超过 MaxRequestBodyLogBytes(64KB),
	// 入队走 JSON 感知截断: 缩短过长 content 字符串值但保留合法 JSON 结构,
	// 随后写入协程走结构化脱敏路径替换敏感字段为 [REDACTED]。
	bigContent := strings.Repeat("x", model.MaxRequestBodyLogBytes+512)
	body := map[string]any{
		"api_key":  apiKeyPlaceholder,
		"password": passwordPlaceholder,
		"messages": []map[string]any{
			{"role": "user", "content": bigContent},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) <= model.MaxRequestBodyLogBytes {
		t.Fatalf("测试体应超过 %d 字节, 实际 %d", model.MaxRequestBodyLogBytes, len(raw))
	}

	entry := model.ErrorLog{Model: "grp", ErrClass: "upstream_5xx", ErrBrief: "b", RequestBody: string(raw)}
	if !ErrorLogEnqueue(entry) {
		t.Fatal("入队失败")
	}
	if err := FlushErrorLogQueue(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	logs, err := ErrorLogList(ctx, 1, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expect 1 row, got %d", len(logs))
	}
	stored := logs[0].RequestBody
	if strings.Contains(stored, apiKeyPlaceholder) {
		t.Fatalf("api_key 占位值泄漏到持久化结果: %s", stored)
	}
	if strings.Contains(stored, passwordPlaceholder) {
		t.Fatalf("password 占位值泄漏到持久化结果: %s", stored)
	}
}

// TestErrorLogCreateRedactsCompleteJSONEndToEnd 端到端: 完整小 JSON 走结构化路径,
// 落库后敏感字段被替换为 [REDACTED], 非敏感字段保留。
func TestErrorLogCreateRedactsCompleteJSONEndToEnd(t *testing.T) {
	ctx := errorLogTestContext(t)

	const apiKeyPlaceholder = "PLACEHOLDER_API_KEY_E2E"
	const passwordPlaceholder = "PLACEHOLDER_PASSWORD_E2E"
	body := `{"api_key":"` + apiKeyPlaceholder + `","password":"` + passwordPlaceholder + `","model":"gpt-4"}`
	entry := model.ErrorLog{Model: "grp", ErrClass: "upstream_4xx", ErrBrief: "b", RequestBody: body}
	if err := ErrorLogCreate(ctx, entry); err != nil {
		t.Fatalf("create: %v", err)
	}

	logs, err := ErrorLogList(ctx, 1, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expect 1 row, got %d", len(logs))
	}
	stored := logs[0].RequestBody
	if strings.Contains(stored, apiKeyPlaceholder) || strings.Contains(stored, passwordPlaceholder) {
		t.Fatalf("完整 JSON 端到端泄漏占位值: %s", stored)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
		t.Fatalf("落库结果应是合法 JSON: %v (got: %s)", err, stored)
	}
	if decoded["model"] != "gpt-4" {
		t.Fatalf("非敏感字段应保留: %s", stored)
	}
}

// TestRedactRequestBodyNeverReturnsRawOnInvalidJSON 安全失败策略:
// 无效/截断 JSON 不得未经脱敏退回原文。构造含敏感字段的不完整 JSON,
// 确认返回值与原文不同(已被脱敏)且不含 secret。
func TestRedactRequestBodyNeverReturnsRawOnInvalidJSON(t *testing.T) {
	const secret = "PLACEHOLDER_SAFEFAIL_SECRET"
	body := `{"api_key":"` + secret + `","password":"` + secret // 不完整
	got := redactRequestBody(body)
	if got == body {
		t.Fatal("无效 JSON 不得未经安全处理退回原文")
	}
	if strings.Contains(got, secret) {
		t.Fatalf("安全失败后仍泄漏 secret: %s", got)
	}
}

// TestSanitizeErrorLogKeepsRawErrorText 后台日志保留原始错误文本，仅请求体脱敏。
func TestSanitizeErrorLogKeepsRawErrorText(t *testing.T) {
	const secret = "PLACEHOLDER_ADMIN_VISIBLE_SECRET"
	entry := model.ErrorLog{
		ErrBrief:    "Authorization: Bearer " + secret,
		ErrDetail:   `upstream 401: {"api_key":"` + secret + `"}`,
		RequestBody: `{"api_key":"` + secret + `"}`,
	}
	got := sanitizeErrorLog(entry)
	if !strings.Contains(got.ErrBrief, secret) || !strings.Contains(got.ErrDetail, secret) {
		t.Fatalf("后台错误文本应保留原文: brief=%q detail=%q", got.ErrBrief, got.ErrDetail)
	}
	if strings.Contains(got.RequestBody, secret) {
		t.Fatalf("请求体仍应脱敏: %s", got.RequestBody)
	}
}
