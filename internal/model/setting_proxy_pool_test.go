package model

import (
	"strings"
	"testing"
)

// proxyPoolJSON 拼一个代理池 JSON 数组, 方便用例书写。
func proxyPoolJSON(entries ...string) string {
	if len(entries) == 0 {
		return "[]"
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// TestValidateProxyPoolAccountPlaceholder 覆盖 {account} 占位符场景: 代理池条目最终作为
// 渠道代理使用, 必须与渠道代理一样支持 {account}, 不能被 net/url 的 userinfo 校验拒绝。
func TestValidateProxyPoolAccountPlaceholder(t *testing.T) {
	// 用户名中含 {account}: 即上报 bug 的用例, 修复前报 "net/url: invalid userinfo"。
	withPlaceholder := proxyPoolJSON(`{"id":"a","name":"账号代理","url":"socks5h://Default.{account}:sk-22@resin:2260","enabled":true}`)
	if err := validateProxyPool(withPlaceholder); err != nil {
		t.Fatalf("用户名含 {account} 应通过校验, 实际错误: %v", err)
	}

	// 密码中含 {account} 字面量: 与 helper.ResolveProxyTemplate 一致, 原样保留, 校验通过。
	inPassword := proxyPoolJSON(`{"id":"b","name":"pw","url":"socks5h://u:{account}@host:1080","enabled":true}`)
	if err := validateProxyPool(inPassword); err != nil {
		t.Fatalf("密码含 {account} 应通过校验, 实际错误: %v", err)
	}

	// 用户名与密码都含 {account}。
	both := proxyPoolJSON(`{"id":"c","name":"both","url":"socks5h://u.{account}:{account}@host:1080","enabled":true}`)
	if err := validateProxyPool(both); err != nil {
		t.Fatalf("用户名与密码都含 {account} 应通过校验, 实际错误: %v", err)
	}
}

// TestValidateProxyPoolPlain 覆盖不含占位符的合法与非法用例。
func TestValidateProxyPoolPlain(t *testing.T) {
	valid := []struct {
		name  string
		value string
	}{
		{"空字符串", ""},
		{"纯空白", "   "},
		{"空数组", "[]"},
		{"http", proxyPoolJSON(`{"id":"a","name":"n","url":"http://127.0.0.1:7890","enabled":true}`)},
		{"socks5h 无 userinfo", proxyPoolJSON(`{"id":"b","name":"n","url":"socks5h://resin:2260","enabled":false}`)},
	}
	for _, tc := range valid {
		if err := validateProxyPool(tc.value); err != nil {
			t.Errorf("%s: 应通过校验, 实际错误: %v", tc.name, err)
		}
	}

	invalid := []struct {
		name  string
		value string
		want  string
	}{
		{
			"非 JSON", "not json",
			"代理池必须是合法的 JSON 数组",
		},
		{
			"ID 为空", proxyPoolJSON(`{"id":"","name":"n","url":"http://h:1"}`),
			"代理条目 ID 不能为空",
		},
		{
			"ID 重复", proxyPoolJSON(`{"id":"x","name":"n","url":"http://h:1"}`, `{"id":"x","name":"m","url":"http://h:2"}`),
			"代理条目 ID 重复: x",
		},
		{
			"名称为空", proxyPoolJSON(`{"id":"a","name":" ","url":"http://h:1"}`),
			"代理条目名称不能为空",
		},
		{
			"地址为空", proxyPoolJSON(`{"id":"a","name":"n","url":""}`),
			"代理条目 n 的地址不能为空",
		},
		{
			"非法协议", proxyPoolJSON(`{"id":"a","name":"n","url":"ftp://h:1"}`),
			"代理条目 n 的协议必须为 http/https/socks/socks5/socks5h",
		},
		{
			"缺少主机", proxyPoolJSON(`{"id":"a","name":"n","url":"socks5h://"}`),
			"代理条目 n 的地址缺少主机",
		},
	}
	for _, tc := range invalid {
		err := validateProxyPool(tc.value)
		if err == nil {
			t.Errorf("%s: 期望错误 %q, 实际为 nil", tc.name, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 期望错误包含 %q, 实际: %v", tc.name, tc.want, err)
		}
	}
}

// TestHasAccountPlaceholder 覆盖字面量与百分号转义两种占位符。
func TestHasAccountPlaceholder(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     bool
	}{
		{"字面量", "socks5://warp-{account}:p@host:1080", true},
		{"转义", "socks5://warp-%7Baccount%7D:p@host:1080", true},
		{"小写转义", "socks5://warp-%7baccount%7d:p@host:1080", true},
		{"无占位符", "socks5://warp-0:p@host:1080", false},
		{"空串", "", false},
	}
	for _, tc := range cases {
		if got := HasAccountPlaceholder(tc.template); got != tc.want {
			t.Errorf("%s: HasAccountPlaceholder(%q) = %v, want %v", tc.name, tc.template, got, tc.want)
		}
	}
}

// TestValidateProxyPoolAccountPlaceholderStillRejectsBadScheme 确认占位符转义只放宽
// userinfo 字符限制, 不会放过非法协议等其余校验。
func TestValidateProxyPoolAccountPlaceholderStillRejectsBadScheme(t *testing.T) {
	bad := proxyPoolJSON(`{"id":"a","name":"n","url":"ftp://u.{account}:p@host:1"}`)
	err := validateProxyPool(bad)
	if err == nil {
		t.Fatalf("含 {account} 但协议非法应被拒绝, 实际通过")
	}
	if !strings.Contains(err.Error(), "协议必须为") {
		t.Fatalf("应报协议非法, 实际: %v", err)
	}
}
