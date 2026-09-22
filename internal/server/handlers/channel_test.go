package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestChannelKeyMaskedLeavesLast4Runes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"abcd", "****"},
		{"sk-secret-key-1234", "****1234"},
	}
	for _, tc := range cases {
		if got := channelKeyMasked(tc.in); got != tc.want {
			t.Fatalf("channelKeyMasked(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatChannelExportPlaintextNameURLAndKeys(t *testing.T) {
	text := formatChannelExport([]model.Channel{
		{
			Name:    "OpenAI",
			BaseURL: "https://api.openai.com",
			Keys: []model.ChannelKey{
				{Key: "sk-live-aaaa"},
				{Key: ""},
				{Key: "sk-live-bbbb"},
			},
			Key: "sk-should-not-appear",
		},
		{
			Name:    "Legacy",
			BaseURL: "https://example.invalid/v1",
			Key:     "sk-legacy-cccc",
		},
	})
	want := strings.Join([]string{
		"# OpenAI",
		"https://api.openai.com",
		"sk-live-aaaa",
		"sk-live-bbbb",
		"",
		"# Legacy",
		"https://example.invalid/v1",
		"sk-legacy-cccc",
		"",
		"",
	}, "\n")
	if text != want {
		t.Fatalf("export =\n%s\nwant =\n%s", text, want)
	}
	if strings.Contains(text, "****") {
		t.Fatal("channel export must keep keys plaintext")
	}
}

func TestFormatChannelExportIncludesBuiltinAndKeyless(t *testing.T) {
	text := formatChannelExport([]model.Channel{
		{
			Name:    "OpenCode",
			BaseURL: "https://opencode.ai/zen/go",
			Builtin: true,
		},
		{
			Name:    "OpenCode Free",
			BaseURL: "https://opencode.ai/zen",
			Builtin: true,
			Key:     "public",
		},
		{
			Name:    "Empty Slots",
			BaseURL: "https://example.invalid",
			Keys:    []model.ChannelKey{{Key: ""}, {Key: "   "}},
		},
	})
	for _, part := range []string{
		"# OpenCode\nhttps://opencode.ai/zen/go\n\n",
		"# OpenCode Free\nhttps://opencode.ai/zen\npublic\n\n",
		"# Empty Slots\nhttps://example.invalid\n\n",
	} {
		if !strings.Contains(text, part) {
			t.Fatalf("export missing %q\n%s", part, text)
		}
	}
}

func TestProbeErrorHumanMapsUpstreamStatus(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		hasWant string // 结果必须包含的中文语义前缀; 空表示期望原样返回。
	}{
		{
			name:    "helper 401",
			err:     errors.New(`upstream returned HTTP 401: {"error":"invalid_api_key"}`),
			hasWant: "上游返回 401（没有该密钥/未授权），原始错误：",
		},
		{
			name:    "relay responded 403",
			err:     errors.New("upstream responded 403 Forbidden: denied"),
			hasWant: "上游返回 403（无访问权限），原始错误：",
		},
		{
			name:    "httpclient with status 429",
			err:     errors.New("POST - https://upstream/v1/chat/completions with status 429 Too Many Requests: quota exceeded"),
			hasWant: "上游返回 429（触发上游限流），原始错误：",
		},
		{
			name:    "5xx 区间兜底",
			err:     errors.New("upstream returned HTTP 502: bad gateway"),
			hasWant: "上游返回 502（上游服务错误），原始错误：",
		},
		{
			name:    "无状态码中文原样",
			err:     errors.New("渠道未配置任何密钥"),
			hasWant: "",
		},
		{
			name:    "网络错误不误判",
			err:     errors.New("dial tcp: lookup api.example.com: no such host"),
			hasWant: "",
		},
	}
	for _, tc := range cases {
		got := probeErrorHuman(tc.err)
		if tc.hasWant == "" {
			if got != tc.err.Error() {
				t.Fatalf("%s: got %q, want original %q", tc.name, got, tc.err.Error())
			}
			continue
		}
		if !strings.HasPrefix(got, tc.hasWant) {
			t.Fatalf("%s: got %q, want prefix %q", tc.name, got, tc.hasWant)
		}
		// 原始错误必须完整保留在末尾, 便于管理员查上游原文。
		if !strings.HasSuffix(got, tc.err.Error()) {
			t.Fatalf("%s: raw error not preserved at tail: %q", tc.name, got)
		}
	}
}
