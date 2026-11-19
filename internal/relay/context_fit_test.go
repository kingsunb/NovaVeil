package relay

import (
	"strings"
	"testing"
	"time"

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

func TestEstimateRequestTokensTextMatchesBytes(t *testing.T) {
	for _, body := range []string{"", "abcd", "中文", "ab中文", strings.Repeat("word ", 500)} {
		if got, want := estimateRequestTokensText(body), estimateRequestTokens([]byte(body)); got != want {
			t.Fatalf("文本与字节估算不一致(%q): %d != %d", body, got, want)
		}
	}
}

func TestObserveTokenDensityEWMA(t *testing.T) {
	const baseURL = "https://observe.example/"
	const modelName = "observe-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := strings.Repeat("a", tokenDensityMinBase*4)
	observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase*2), body)
	if got := modelTokenDensity(0, baseURL, modelName); got != 2 {
		t.Fatalf("首次观测应直接初始化 = %v, want 2", got)
	}
	observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase), body)
	want := 2 + tokenDensityAlpha*(1-2)
	if got := modelTokenDensity(0, baseURL, modelName); got != want {
		t.Fatalf("第二次观测 = %v, want %v", got, want)
	}
}

func TestObserveTokenDensityInitializesAfterDefaultCache(t *testing.T) {
	const baseURL = "https://observe-default.example/"
	const modelName = "observe-default-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })
	tokenDensityCache.Store(key, tokenDensityState{Density: defaultTokenDensity})

	body := strings.Repeat("a", tokenDensityMinBase*4)
	observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase*2), body)
	if got := modelTokenDensity(0, baseURL, modelName); got != 2 {
		t.Fatalf("默认缓存后首次观测应直接初始化 = %v, want 2", got)
	}
}

func TestObserveTokenDensitySkipsSmallRequests(t *testing.T) {
	const baseURL = "https://observe-small.example/"
	const modelName = "observe-small-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	observeTokenDensity(0, baseURL, modelName, 10, strings.Repeat("a", 400))
	if got := modelTokenDensity(0, baseURL, modelName); got != defaultTokenDensity {
		t.Fatalf("小请求不应写入密度, got %v", got)
	}
}

func TestObserveTokenDensityDailyLimit(t *testing.T) {
	const baseURL = "https://observe-daily.example/"
	const modelName = "observe-daily-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Store(key, tokenDensityState{
		Density:      1,
		Samples:      tokenDensityBootstrapSamples,
		DailySamples: 0,
		SampleDay:    tokenDensityDay(time.Now().AddDate(0, 0, -1)),
	})
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := strings.Repeat("a", tokenDensityMinBase*4)
	for i := 0; i < 4; i++ {
		observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase*2), body)
	}

	state, _ := tokenDensityCache.Load(key)
	current, _ := state.(tokenDensityState)
	if current.Density != 2 {
		t.Fatalf("超差应更新密度 = %v, want 2", current.Density)
	}
	if current.Samples != tokenDensityBootstrapSamples+3 || current.DailySamples != 3 {
		t.Fatalf("每日只验证三条, state = %+v", current)
	}
}

func TestObserveTokenDensityTolerance(t *testing.T) {
	const baseURL = "https://observe-tolerance.example/"
	const modelName = "observe-tolerance-model"
	key := tokenDensityKey(0, baseURL, modelName)
	body := strings.Repeat("a", tokenDensityMinBase*4)
	promptBase := int64(tokenDensityMinBase)

	tokenDensityCache.Store(key, tokenDensityState{
		Density:      1,
		Samples:      tokenDensityBootstrapSamples,
		DailySamples: 0,
		SampleDay:    tokenDensityDay(time.Now().AddDate(0, 0, -1)),
	})
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	observeTokenDensity(0, baseURL, modelName, promptBase*102/100, body)
	if got := modelTokenDensity(0, baseURL, modelName); got != 1 {
		t.Fatalf("3%% 内不应改密度, got %v", got)
	}

	tokenDensityCache.Store(key, tokenDensityState{
		Density:      1,
		Samples:      tokenDensityBootstrapSamples,
		DailySamples: 0,
		SampleDay:    tokenDensityDay(time.Now().AddDate(0, 0, -1)),
	})
	observeTokenDensity(0, baseURL, modelName, promptBase*105/100, body)
	if got := modelTokenDensity(0, baseURL, modelName); got != 1.05 {
		t.Fatalf("超差应直接更新, got %v", got)
	}
}

func TestObserveTokenDensityValidatesAfterBootstrap(t *testing.T) {
	const baseURL = "https://observe-bootstrap.example/"
	const modelName = "observe-bootstrap-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := strings.Repeat("a", tokenDensityMinBase*4)
	for i := 0; i < tokenDensityBootstrapSamples; i++ {
		observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase), body)
	}
	for i := 0; i < 4; i++ {
		observeTokenDensity(0, baseURL, modelName, int64(tokenDensityMinBase*2), body)
	}

	state, _ := tokenDensityCache.Load(key)
	current, _ := state.(tokenDensityState)
	if current.Density != 2 || current.Samples != tokenDensityBootstrapSamples+3 || current.DailySamples != 3 {
		t.Fatalf("满十条后当日只验证三条, state = %+v", current)
	}
}

func TestInvalidateChannelTokenDensityRemovesAllModels(t *testing.T) {
	first := tokenDensityKey(7, "", "model-a")
	second := tokenDensityKey(7, "", "model-b")
	other := tokenDensityKey(8, "", "model-a")
	tokenDensityCache.Store(first, tokenDensityState{Density: 1, Samples: 1})
	tokenDensityCache.Store(second, tokenDensityState{Density: 1, Samples: 1})
	tokenDensityCache.Store(other, tokenDensityState{Density: 1, Samples: 1})
	t.Cleanup(func() {
		tokenDensityCache.Delete(first)
		tokenDensityCache.Delete(second)
		tokenDensityCache.Delete(other)
	})

	InvalidateChannelTokenDensity(7)

	if _, ok := tokenDensityCache.Load(first); ok {
		t.Fatal("first density should be removed")
	}
	if _, ok := tokenDensityCache.Load(second); ok {
		t.Fatal("second density should be removed")
	}
	if _, ok := tokenDensityCache.Load(other); !ok {
		t.Fatal("other channel density should remain")
	}
}

func TestEstimateModelTokensUsesSharedDensity(t *testing.T) {
	const baseURL = "https://upstream.example/"
	const modelName = "shared-density-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	body := []byte(strings.Repeat("a", 4000))
	if got := estimateModelTokens(body, 0, baseURL, modelName); got != 563 {
		t.Fatalf("未实测按默认密度 4/7.1 = %d, want 563", got)
	}
	tokenDensityCache.Store(key, tokenDensityState{Density: 0.5})
	if got := estimateModelTokens(body, 0, baseURL, modelName); got != 500 {
		t.Fatalf("实测密度 0.5 时 = %d, want 500", got)
	}
	tokenDensityCache.Store(key, tokenDensityState{Density: 1.0})
	if got := estimateModelTokens(body, 0, baseURL, modelName); got != 1000 {
		t.Fatalf("实测密度 1.0 覆盖默认后 = %d, want 1000", got)
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
			key := tokenDensityKey(0, baseURL, modelName)
			// 实测密度 1 锚定基准口径, 验证大规模下线性。
			tokenDensityCache.Store(key, tokenDensityState{Density: 1.0})
			t.Cleanup(func() { tokenDensityCache.Delete(key) })

			body := []byte(strings.Repeat("a", tc.tokens*4))
			if got := estimateModelTokens(body, 0, baseURL, modelName); got != tc.tokens {
				t.Fatalf("estimateModelTokens(%s) = %d, want %d", tc.name, got, tc.tokens)
			}
		})
	}
}

func TestContextFitsWindow(t *testing.T) {
	const baseURL = "https://upstream.example/"
	const modelName = "window-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Store(key, tokenDensityState{Density: 1.0})
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
			if got := contextFitsWindow(body, tc.window, 0, baseURL, modelName); got != tc.want {
				t.Fatalf("contextFitsWindow(window=%d) = %v, want %v", tc.window, got, tc.want)
			}
		})
	}
}

func TestContextFitsWindowDefaultDensityMatchesUpstream(t *testing.T) {
	const baseURL = "https://default-density.example/"
	const modelName = "default-density-model"
	key := tokenDensityKey(0, baseURL, modelName)
	tokenDensityCache.Delete(key)
	t.Cleanup(func() { tokenDensityCache.Delete(key) })

	// 语料实测锚定: 约 1,420,000 个 ASCII 字符即上游 200K 实际 token。
	fits := []byte(strings.Repeat("a", 1_420_000))
	if !contextFitsWindow(fits, 200_000, 0, baseURL, modelName) {
		t.Fatal("1,420,000 字符按默认密度应装进 200K 手动窗口")
	}
	overflow := []byte(strings.Repeat("a", 1_500_000))
	if contextFitsWindow(overflow, 200_000, 0, baseURL, modelName) {
		t.Fatal("1,500,000 字符按默认密度不应装进 200K 手动窗口")
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
