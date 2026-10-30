package helper

import (
	"net/url"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestResolveProxyTemplateReplacesAccountInUsername 验证基本占位符替换:
// 仅用户名中的 {account} 被替换, 密码与主机等其余部分保持不变。
func TestResolveProxyTemplateReplacesAccountInUsername(t *testing.T) {
	resolved, err := ResolveProxyTemplate("socks5h://grok.{account}:sk-111@resin:2260", "alice")
	if err != nil {
		t.Fatalf("解析合法模板失败: %v", err)
	}
	if resolved.Scheme != "socks5h" {
		t.Fatalf("scheme 应保持 socks5h, 实际 %q", resolved.Scheme)
	}
	if resolved.Host != "resin:2260" {
		t.Fatalf("host 应保持 resin:2260, 实际 %q", resolved.Host)
	}
	if got := resolved.User.Username(); got != "grok.alice" {
		t.Fatalf("用户名应为 grok.alice, 实际 %q", got)
	}
	password, ok := resolved.User.Password()
	if !ok || password != "sk-111" {
		t.Fatalf("密码应保持 sk-111 且存在, 实际 %q/%v", password, ok)
	}
}

// TestResolveProxyTemplateEscapesSpecialCharacters 验证别名含 @ : / 等特殊字符时的安全性:
// 编程式重建后序列化再反解, 用户名与密码必须完整还原, URL 结构不得被破坏。
func TestResolveProxyTemplateEscapesSpecialCharacters(t *testing.T) {
	tricky := "a@b:c/d"
	resolved, err := ResolveProxyTemplate("socks5h://u.{account}:p@host:1080", tricky)
	if err != nil {
		t.Fatalf("解析含特殊字符别名的模板失败: %v", err)
	}
	if got := resolved.User.Username(); got != "u."+tricky {
		t.Fatalf("替换后用户名应保留原始特殊字符, 实际 %q", got)
	}
	// 序列化结果必须可无损反解: 裸字符串拼接会在这一步破坏 userinfo 结构。
	roundtrip, err := url.Parse(resolved.String())
	if err != nil {
		t.Fatalf("序列化后的代理地址无法反解: %v (%s)", err, resolved.String())
	}
	if got := roundtrip.User.Username(); got != "u."+tricky {
		t.Fatalf("反解后用户名应完整还原, 实际 %q (serialized: %s)", got, resolved.String())
	}
	if password, ok := roundtrip.User.Password(); !ok || password != "p" {
		t.Fatalf("反解后密码应完整还原, 实际 %q/%v", password, ok)
	}
	if roundtrip.Host != "host:1080" {
		t.Fatalf("特殊字符不得破坏 host 结构, 实际 %q", roundtrip.Host)
	}
}

// TestResolveProxyTemplateLeavesPasswordUntouched 验证密码段不参与替换,
// 密码中恰好包含 {account} 字面量时也原样保留。
func TestResolveProxyTemplateLeavesPasswordUntouched(t *testing.T) {
	resolved, err := ResolveProxyTemplate("socks5h://u.{account}:{account}@host", "alice")
	if err != nil {
		t.Fatalf("解析模板失败: %v", err)
	}
	if got := resolved.User.Username(); got != "u.alice" {
		t.Fatalf("用户名应完成替换, 实际 %q", got)
	}
	if password, ok := resolved.User.Password(); !ok || password != "{account}" {
		t.Fatalf("密码段不得参与替换, 实际 %q/%v", password, ok)
	}
}

// TestResolveProxyTemplateWithoutPlaceholderOrUserinfo 验证无占位符或无 userinfo 时原样返回。
func TestResolveProxyTemplateWithoutPlaceholderOrUserinfo(t *testing.T) {
	cases := []string{
		"socks5h://static:p@resin:2260", // 有 userinfo 无占位符。
		"socks5://resin:2260",           // 无 userinfo。
		"http://proxy.example.com:8080", // 无 userinfo 的 http 代理。
	}
	for _, template := range cases {
		resolved, err := ResolveProxyTemplate(template, "alice")
		if err != nil {
			t.Fatalf("模板 %q 解析失败: %v", template, err)
		}
		if resolved.String() != template {
			t.Fatalf("无占位符模板应原样返回, 输入 %q 实际 %q", template, resolved.String())
		}
	}
}

// TestResolveProxyTemplateInvalidReturnsError 验证非法模板返回 error 而不是 panic,
// 调用方据此把本轮记为失败。
func TestResolveProxyTemplateInvalidReturnsError(t *testing.T) {
	for _, template := range []string{"::not-a-url", "socks5h://%zz@host:2260"} {
		if _, err := ResolveProxyTemplate(template, "alice"); err == nil {
			t.Fatalf("非法模板 %q 应返回错误", template)
		}
	}
}

// TestResolveProxyTemplateTrimsWhitespace 验证模板两侧空白被容忍, 与渠道代理读取惯例一致。
func TestResolveProxyTemplateTrimsWhitespace(t *testing.T) {
	resolved, err := ResolveProxyTemplate("  socks5://u.{account}@host  ", "alice")
	if err != nil {
		t.Fatalf("带空白的模板解析失败: %v", err)
	}
	if got := resolved.User.Username(); got != "u.alice" {
		t.Fatalf("空白容忍下替换仍应生效, 实际 %q", got)
	}
}

// TestResolveChannelProxyTemplateWarpBypassesAlias WARP 渠道应清空代理模板。
func TestResolveChannelProxyTemplateWarpBypassesAlias(t *testing.T) {
	template := "socks5h://{account}:pass@" + model.WarpProxyMarker + ":1080"
	channel := model.Channel{ID: 7, ChannelProxy: &template}
	if err := ResolveChannelProxyTemplate(&channel); err != nil {
		t.Fatalf("WARP 渠道解析失败: %v", err)
	}
	if channel.ChannelProxy != nil {
		t.Fatalf("WARP 渠道应清空代理模板, 实际 %q", *channel.ChannelProxy)
	}
}

// TestResolveChannelProxyTemplateResinKeepsAlias resin 渠道仍按 Key 别名解析。
func TestResolveChannelProxyTemplateResinKeepsAlias(t *testing.T) {
	template := "socks5h://grok.{account}:sk-1@resin:2260"
	channel := model.Channel{
		ID:           7,
		Keys:         []model.ChannelKey{{ID: "key-1", Key: "sk-1"}},
		ChannelProxy: &template,
	}
	if err := ResolveChannelProxyTemplate(&channel); err != nil {
		t.Fatalf("resin 渠道解析失败: %v", err)
	}
	want := "socks5h://grok." + AccountAliasFor(7, "key-1") + ":sk-1@resin:2260"
	if channel.ChannelProxy == nil || *channel.ChannelProxy != want {
		t.Fatalf("resin 渠道应派生 Key 别名, want %q got %v", want, channel.ChannelProxy)
	}
}

// TestAccountAliasForStableAndDistinct 验证 {account} 生成别名的核心不变量:
// 同一渠道同一 Key 恒定一致(跨请求/跨重启), 不同 Key、不同渠道互不相同,
// 输出恒为 8 位 hex, 且派生自 ID 而非密钥明文(不传密钥, 只传 ID)。
func TestAccountAliasForStableAndDistinct(t *testing.T) {
	first := AccountAliasFor(77, "id-first")
	if got := AccountAliasFor(77, "id-first"); got != first {
		t.Fatalf("同渠道同 Key 别名应一致: %q vs %q", first, got)
	}
	if AccountAliasFor(77, "id-second") == first {
		t.Fatal("同渠道不同 Key 别名应不同")
	}
	if AccountAliasFor(78, "id-first") == first {
		t.Fatal("不同渠道同一 Key 别名应不同")
	}
	if len(first) != 8 {
		t.Fatalf("别名应为 8 位 hex, 实际 %q", first)
	}
	for _, c := range first {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("别名含非 hex 字符: %q", first)
		}
	}
	// 旧式单 Key 渠道: 空 keyID 得到渠道级恒定别名。
	if AccountAliasFor(9, "") != AccountAliasFor(9, "") {
		t.Fatal("旧式单 Key 别名应稳定")
	}
}
