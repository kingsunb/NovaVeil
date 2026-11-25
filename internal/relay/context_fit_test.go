package relay

import (
	"context"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
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

func contextFitDensityChannel(t *testing.T, density float64) model.Channel {
	t.Helper()
	setupFailoverTest(t)
	channel := createIntegrationChannel(t, "context-fit-density", model.ChannelProviderOpenAI, "https://density.example", "density-model")
	t.Cleanup(func() {
		if err := op.ChannelDel(channel.ID, context.Background()); err != nil {
			t.Errorf("清理密度测试渠道失败: %v", err)
		}
	})
	if density > 0 {
		if err := op.TokenDensitySave(context.Background(), channel.ID, "density-model", density, 1, 1, "2026-11-20"); err != nil {
			t.Fatal(err)
		}
	}
	return channel
}

func TestEstimateModelTokensUsesSharedDensity(t *testing.T) {
	channel := contextFitDensityChannel(t, 0)
	body := []byte(strings.Repeat("a", 4000))
	if got := estimateModelTokens(body, channel.ID, channel.BaseURL, "density-model"); got != 563 {
		t.Fatalf("未观测应使用默认密度: %d", got)
	}
	for _, test := range []struct {
		density float64
		tokens  int
	}{{0.5, 500}, {1, 1000}} {
		if err := op.TokenDensitySave(context.Background(), channel.ID, "density-model", test.density, 1, 1, "2026-11-20"); err != nil {
			t.Fatal(err)
		}
		if got := estimateModelTokens(body, channel.ID, channel.BaseURL, "density-model"); got != test.tokens {
			t.Fatalf("密度 %v 应估算 %d token, 实际 %d", test.density, test.tokens, got)
		}
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
			body := []byte(strings.Repeat("a", tc.tokens*4))
			if got := scaleBaseTokens(estimateRequestTokens(body), 1); got != tc.tokens {
				t.Fatalf("estimateModelTokens(%s) = %d, want %d", tc.name, got, tc.tokens)
			}
		})
	}
}

func TestContextFitsWindow(t *testing.T) {
	channel := contextFitDensityChannel(t, 1)

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
			if got := contextFitsWindow(body, tc.window, channel.ID, channel.BaseURL, "density-model"); got != tc.want {
				t.Fatalf("contextFitsWindow(window=%d) = %v, want %v", tc.window, got, tc.want)
			}
		})
	}
}

func TestContextFitsWindowDefaultDensityMatchesUpstream(t *testing.T) {
	const baseURL = "https://default-density.example/"
	const modelName = "default-density-model"

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
	if got := effectiveContextWindow(model.Channel{}, channelModel); got != 0 {
		t.Fatalf("同步窗口不应参与过滤, got %d", got)
	}

	manual := 300_000
	channel := model.Channel{ModelLimits: map[string]model.ChannelModelLimit{
		"manual-only": {ContextWindow: &manual},
	}}
	if got := effectiveContextWindow(channel, channelModel); got != manual {
		t.Fatalf("手动窗口 = %d, want %d", got, manual)
	}
}

func TestEffectiveContextWindowOpencodeFreeGlobalFallback(t *testing.T) {
	original := opencodeFreeContextWindow
	opencodeFreeContextWindow = func() int { return 250_000 }
	t.Cleanup(func() { opencodeFreeContextWindow = original })

	freeLane := model.Channel{OpencodeCompat: true, BaseURL: "https://opencode.ai/inference"}
	paidGo := model.Channel{OpencodeCompat: true, BaseURL: "https://opencode.ai/zen/v1"}
	other := model.Channel{BaseURL: "https://api.example.com"}
	modelName := &model.ChannelModel{Name: "free-model"}

	if got := effectiveContextWindow(freeLane, modelName); got != 250_000 {
		t.Fatalf("免费档应回退全局窗口, got %d", got)
	}
	if got := effectiveContextWindow(paidGo, modelName); got != 0 {
		t.Fatalf("付费 go 档不应使用全局窗口, got %d", got)
	}
	if got := effectiveContextWindow(other, modelName); got != 0 {
		t.Fatalf("非 OpenCode 渠道不应使用全局窗口, got %d", got)
	}

	manual := 400_000
	freeLane.ModelLimits = map[string]model.ChannelModelLimit{
		"free-model": {ContextWindow: &manual},
	}
	if got := effectiveContextWindow(freeLane, modelName); got != manual {
		t.Fatalf("渠道手动窗口应优先于全局, got %d", got)
	}
}

func TestContextFitState(t *testing.T) {
	cases := []struct {
		name string
		scan contextFitScan
		want string
	}{
		{"skipped", contextFitScan{candidates: 2, skipped: map[int]bool{1: true}, maxWindow: 100}, "skipped"},
		{"unconfigured", contextFitScan{candidates: 1}, "unconfigured"},
		{"fits", contextFitScan{candidates: 1, maxWindow: 100, fitting: 1}, "fits"},
	}
	for _, c := range cases {
		if got := contextFitState(c.scan); got != c.want {
			t.Fatalf("%s: contextFitState = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestObserveRoundContext(t *testing.T) {
	original := opencodeFreeContextWindow
	opencodeFreeContextWindow = func() int { return 128_000 }
	t.Cleanup(func() { opencodeFreeContextWindow = original })

	channel := model.Channel{ID: 424242, OpencodeCompat: true, BaseURL: "https://opencode.ai/inference"}
	channelModel := &model.ChannelModel{Name: "observe-model"}
	obs := observeRoundContext(channel, channelModel, []byte("hello world"))
	if obs.EffectiveWindow != 128_000 {
		t.Fatalf("生效窗口 = %d, want 128000", obs.EffectiveWindow)
	}
	if obs.EstimatedTokens <= 0 {
		t.Fatalf("预估 token 应大于 0, got %d", obs.EstimatedTokens)
	}
	if obs.DensitySource != "default" {
		t.Fatalf("未观测密度来源应为 default, got %q", obs.DensitySource)
	}
}
