package helper

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestParseOpencodeProtocolCatalogFixture(t *testing.T) {
	const fixture = `{
		"opencode": {
			"id": "opencode",
			"npm": "@ai-sdk/openai-compatible",
			"api": "https://opencode.ai/zen/v1",
			"models": {
				"deepseek-v4-flash-free": {"id": "deepseek-v4-flash-free"},
				"gpt-5": {"id": "gpt-5", "provider": {"npm": "@ai-sdk/openai"}},
				"claude-x": {"id": "claude-x", "provider": {"npm": "@ai-sdk/anthropic"}},
				"gemini-x": {"id": "gemini-x", "provider": {"npm": "@ai-sdk/google"}}
			}
		},
		"opencode-go": {
			"id": "opencode-go",
			"npm": "@ai-sdk/openai-compatible",
			"api": "https://opencode.ai/zen/go/v1",
			"models": {
				"deepseek-v4-flash-free": {"id": "deepseek-v4-flash-free", "provider": {"npm": "@ai-sdk/anthropic"}},
				"gpt-5.6-luna": {"id": "gpt-5.6-luna", "provider": {"npm": "@ai-sdk/openai"}}
			}
		},
		"other": {
			"id": "other",
			"npm": "@ai-sdk/anthropic",
			"api": "https://example.invalid/v1",
			"models": {"deepseek-v4-flash-free": {"id": "deepseek-v4-flash-free"}}
		}
	}`

	zen, err := ParseOpencodeProtocolCatalog([]byte(fixture), "zen")
	if err != nil {
		t.Fatalf("parse zen: %v", err)
	}
	if zen["deepseek-v4-flash-free"] != model.UpstreamProtocolChat {
		t.Fatalf("zen free model = %q, want chat", zen["deepseek-v4-flash-free"])
	}
	if zen["gpt-5"] != model.UpstreamProtocolResponses {
		t.Fatalf("zen gpt-5 = %q, want responses", zen["gpt-5"])
	}
	if zen["claude-x"] != model.UpstreamProtocolAnthropic {
		t.Fatalf("zen claude-x = %q, want anthropic", zen["claude-x"])
	}
	if _, ok := zen["gemini-x"]; ok {
		t.Fatal("unrecognized google SDK must not be guessed")
	}

	goTier, err := ParseOpencodeProtocolCatalog([]byte(fixture), "go")
	if err != nil {
		t.Fatalf("parse go: %v", err)
	}
	if goTier["deepseek-v4-flash-free"] != model.UpstreamProtocolAnthropic {
		t.Fatalf("go free model = %q, want anthropic", goTier["deepseek-v4-flash-free"])
	}
	if goTier["gpt-5.6-luna"] != model.UpstreamProtocolResponses {
		t.Fatalf("go luna = %q, want responses", goTier["gpt-5.6-luna"])
	}
	if _, ok := goTier["claude-x"]; ok {
		t.Fatal("zen-only model must not leak into go tier")
	}
}

func TestProtocolForSDK(t *testing.T) {
	cases := []struct {
		npm  string
		want string
		ok   bool
	}{
		{"@ai-sdk/openai-compatible", model.UpstreamProtocolChat, true},
		{"@ai-sdk/openai", model.UpstreamProtocolResponses, true},
		{"@ai-sdk/anthropic", model.UpstreamProtocolAnthropic, true},
		{"@ai-sdk/google", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := protocolForSDK(tc.npm)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("protocolForSDK(%q) = %q, %v; want %q, %v", tc.npm, got, ok, tc.want, tc.ok)
		}
	}
}
