package relay

import "testing"

func TestLooksLikeAnthropicMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "anthropic with system array",
			body: `{"model":"m","max_tokens":128000,"system":[{"type":"text","text":"You are helpful.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`,
			want: true,
		},
		{
			name: "anthropic with system string",
			body: `{"model":"m","system":"You are helpful.","messages":[{"role":"user","content":"hi"}]}`,
			want: true,
		},
		{
			name: "anthropic with input_schema tools",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"calc","description":"calc","input_schema":{"type":"object","properties":{}}}]}`,
			want: true,
		},
		{
			name: "anthropic with both system and tools",
			body: `{"model":"m","system":"sys","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"calc","input_schema":{"type":"object"}}]}`,
			want: true,
		},
		{
			name: "openai chat standard",
			body: `{"model":"m","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"calc","description":"calc","parameters":{"type":"object"}}}]}`,
			want: false,
		},
		{
			name: "openai chat with tool_calls",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`,
			want: false,
		},
		{
			name: "minimal openai chat",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "empty body",
			body: `{}`,
			want: false,
		},
		{
			name: "anthropic no tools no system",
			body: `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeAnthropicMessages([]byte(tt.body))
			if got != tt.want {
				t.Errorf("looksLikeAnthropicMessages() = %v, want %v", got, tt.want)
			}
		})
	}
}
