package helper

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestParseOpencodeCatalogFixture(t *testing.T) {
	const fixture = `{
		"opencode": {
			"id": "opencode",
			"npm": "@ai-sdk/openai-compatible",
			"api": "https://opencode.ai/zen/v1",
			"models": {
				"deepseek-v4-flash-free": {"id": "deepseek-v4-flash-free", "limit": {"context": 976000, "output": 375000}, "reasoning": true, "tool_call": true, "modalities": {"input": ["text", "image"]}},
				"gpt-5": {"id": "gpt-5", "provider": {"npm": "@ai-sdk/openai"}, "limit": {"context": 1024000, "output": 125000}, "reasoning": true, "tool_call": true, "modalities": {"input": ["text", "image", "pdf"]}},
				"claude-x": {"id": "claude-x", "provider": {"npm": "@ai-sdk/anthropic"}, "limit": {"context": 200000, "output": 64000}, "reasoning": false, "tool_call": true},
				"gemini-x": {"id": "gemini-x", "provider": {"npm": "@ai-sdk/google"}}
			}
		},
		"opencode-go": {
			"id": "opencode-go",
			"npm": "@ai-sdk/openai-compatible",
			"api": "https://opencode.ai/zen/go/v1",
			"models": {
				"deepseek-v4-flash-free": {"id": "deepseek-v4-flash-free", "provider": {"npm": "@ai-sdk/anthropic"}, "limit": {"context": 1000000, "output": 128000}},
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

	zen, err := ParseOpencodeCatalog([]byte(fixture), "zen")
	if err != nil {
		t.Fatalf("parse zen: %v", err)
	}
	if zen["deepseek-v4-flash-free"].Protocol != model.UpstreamProtocolChat {
		t.Fatalf("zen free model protocol = %q, want chat", zen["deepseek-v4-flash-free"].Protocol)
	}
	if zen["deepseek-v4-flash-free"].ContextWindow != 976000 {
		t.Fatalf("zen free model context = %d, want 976000", zen["deepseek-v4-flash-free"].ContextWindow)
	}
	if zen["deepseek-v4-flash-free"].MaxOutput != 375000 {
		t.Fatalf("zen free model max output = %d, want 375000", zen["deepseek-v4-flash-free"].MaxOutput)
	}
	if !zen["deepseek-v4-flash-free"].Reasoning {
		t.Fatal("zen free model reasoning = false, want true")
	}
	if !zen["deepseek-v4-flash-free"].ToolCall {
		t.Fatal("zen free model tool_call = false, want true")
	}
	if len(zen["deepseek-v4-flash-free"].InputModalities) != 2 {
		t.Fatalf("zen free model modalities = %v, want 2 entries", zen["deepseek-v4-flash-free"].InputModalities)
	}
	if zen["gpt-5"].Protocol != model.UpstreamProtocolResponses {
		t.Fatalf("zen gpt-5 protocol = %q, want responses", zen["gpt-5"].Protocol)
	}
	if zen["gpt-5"].ContextWindow != 1024000 {
		t.Fatalf("zen gpt-5 context = %d, want 1024000", zen["gpt-5"].ContextWindow)
	}
	if zen["claude-x"].Protocol != model.UpstreamProtocolAnthropic {
		t.Fatalf("zen claude-x protocol = %q, want anthropic", zen["claude-x"].Protocol)
	}
	if zen["claude-x"].Reasoning {
		t.Fatal("zen claude-x reasoning = true, want false")
	}
	if _, ok := zen["gemini-x"]; ok {
		t.Fatal("unrecognized google SDK must not be guessed")
	}

	goTier, err := ParseOpencodeCatalog([]byte(fixture), "go")
	if err != nil {
		t.Fatalf("parse go: %v", err)
	}
	if goTier["deepseek-v4-flash-free"].Protocol != model.UpstreamProtocolAnthropic {
		t.Fatalf("go free model protocol = %q, want anthropic", goTier["deepseek-v4-flash-free"].Protocol)
	}
	if goTier["deepseek-v4-flash-free"].ContextWindow != 1000000 {
		t.Fatalf("go free model context = %d, want 1000000", goTier["deepseek-v4-flash-free"].ContextWindow)
	}
	if goTier["gpt-5.6-luna"].Protocol != model.UpstreamProtocolResponses {
		t.Fatalf("go luna protocol = %q, want responses", goTier["gpt-5.6-luna"].Protocol)
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
