package op

import "testing"

// TestValidateChannelBaseURL 验证渠道上游 BaseURL 只做格式校验(scheme/host/无 userinfo),
// 不做目标地址范围限制: 私网/环回/链路本地/保留段/内网域名一律放行。
func TestValidateChannelBaseURL(t *testing.T) {
	for _, raw := range []string{
		"https://api.example.com/v1",
		"http://127.0.0.1:8080",
		"http://10.0.0.1:8080",
		"http://192.168.1.1:8080",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://grok2api:8000",
	} {
		if err := validateChannelBaseURL(raw); err != nil {
			t.Fatalf("valid base url %q should be accepted: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"ftp://example.com",
		"not-a-url",
		"",
		"https://user:pass@example.com",
	} {
		if err := validateChannelBaseURL(raw); err == nil {
			t.Fatalf("malformed base url %q should be rejected", raw)
		}
	}
}

// TestValidateChannelEgressBaseURL 验证导出的校验与 validateChannelBaseURL 行为一致:
// 不再拒绝私网/环回/链路本地/保留段地址, 仅拒绝非 http/https、空 host、userinfo。
func TestValidateChannelEgressBaseURL(t *testing.T) {
	for _, raw := range []string{
		"http://8.8.8.8:8080/v1",
		"http://127.0.0.1:8080",
		"http://10.0.0.1:8080",
		"http://172.16.0.1:8080",
		"http://192.168.1.1:8080",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0:8080",
		"http://255.255.255.255:8080",
		"http://100.64.0.1:8080",
		"http://192.0.2.1:8080",
		"http://198.18.0.1:8080",
		"http://198.51.100.1:8080",
		"http://203.0.113.1:8080",
		"http://240.0.0.1:8080",
		"http://localhost:8080",
		"http://grok2api:8000",
	} {
		if err := ValidateChannelEgressBaseURL(raw); err != nil {
			t.Fatalf("base url %q should be accepted: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"ftp://example.com",
		"not-a-url",
		"",
		"https://user:pass@example.com",
	} {
		if err := ValidateChannelEgressBaseURL(raw); err == nil {
			t.Fatalf("malformed base url %q should be rejected", raw)
		}
	}
}
