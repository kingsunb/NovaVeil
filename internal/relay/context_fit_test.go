package relay

import (
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestEstimateRequestTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "空请求", body: "", want: 0},
		{name: "ASCII 恰好 4 字符", body: "abcd", want: 1},
		{name: "ASCII 不足 4 字符向下取整", body: "abc", want: 0},
		{name: "ASCII 6 字符向下取整", body: "abcdef", want: 1},
		{name: "非 ASCII 每字 1 token", body: "中文", want: 2},
		{name: "混合宽度相加", body: "ab中文", want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateRequestTokens([]byte(tc.body)); got != tc.want {
				t.Fatalf("estimateRequestTokens(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestEstimateModelTokensUsesSharedDensity(t *testing.T) {
	const baseURL = "https://upstream.example/"
	const modelName = "shared-density-model"
	key := tokenDensityKey(baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := []byte(strings.Repeat("a", 4000))
	if got := estimateModelTokens(body, baseURL, modelName); got != 1000 {
		t.Fatalf("未实测时按基准口径 = %d, want 1000", got)
	}
	tokenDensityCache.Store(key, 0.5)
	if got := estimateModelTokens(body, baseURL, modelName); got != 500 {
		t.Fatalf("实测密度 0.5 时 = %d, want 500", got)
	}
}

func TestEstimateModelTokensScale(t *testing.T) {
	cases := []struct {
		name   string
		tokens int
	}{
		{name: "1K", tokens: 1_000},
		{name: "200K", tokens: 200_000},
		{name: "689K", tokens: 689_106},
		{name: "1M", tokens: 1_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const baseURL = "https://scale.example/"
			modelName := "model-" + tc.name
			key := tokenDensityKey(baseURL, modelName)
			tokenDensityCache.Delete(key)
			t.Cleanup(func() { tokenDensityCache.Delete(key) })

			body := []byte(strings.Repeat("a", tc.tokens*4))
			if got := estimateModelTokens(body, baseURL, modelName); got != tc.tokens {
				t.Fatalf("estimateModelTokens(%s) = %d, want %d", tc.name, got, tc.tokens)
			}
		})
	}
}

func TestContextFitsWindow(t *testing.T) {
	const baseURL = "https://upstream.example/"
	const modelName = "window-model"
	key := tokenDensityKey(baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := []byte(strings.Repeat("a", 4000))
	cases := []struct {
		name   string
		window int
		want   bool
	}{
		{name: "窗口未知视为放行", window: 0, want: true},
		{name: "正边界", window: 1000, want: true},
		{name: "超出一个 token", window: 999, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextFitsWindow(body, tc.window, baseURL, modelName); got != tc.want {
				t.Fatalf("contextFitsWindow(window=%d) = %v, want %v", tc.window, got, tc.want)
			}
		})
	}
}

func TestEffectiveContextWindowOnlyUsesManualValue(t *testing.T) {
	channelModel := &model.ChannelModel{Name: "manual-only", ContextWindow: 200_000}
	if got := effectiveContextWindow(nil, channelModel); got != 0 {
		t.Fatalf("同步窗口不应参与过滤, got %d", got)
	}

	manual := 300_000
	limits := map[string]model.ChannelModelLimit{
		"manual-only": {ContextWindow: &manual},
	}
	if got := effectiveContextWindow(limits, channelModel); got != manual {
		t.Fatalf("手动窗口 = %d, want %d", got, manual)
	}
}
