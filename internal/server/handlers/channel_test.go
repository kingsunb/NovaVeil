package handlers

import (
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
