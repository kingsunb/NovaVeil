package relay

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

func TestContextProbeMaxTokensIsDecimalMillion(t *testing.T) {
	if contextProbeMaxTokens != 1_000_000 {
		t.Fatalf("contextProbeMaxTokens = %d, want 1000000", contextProbeMaxTokens)
	}
}

func TestProbeTargetTokensUsesDecimalUnits(t *testing.T) {
	cases := []struct {
		tokens int
		want   int
	}{
		{tokens: 1_000_000, want: 980_000},
		{tokens: 500_000, want: 499_000},
		{tokens: 128_000, want: 127_000},
		{tokens: 1_000, want: 1},
		{tokens: 1, want: 1},
	}
	for _, tc := range cases {
		if got := probeTargetTokens(tc.tokens); got != tc.want {
			t.Fatalf("probeTargetTokens(%d) = %d, want %d", tc.tokens, got, tc.want)
		}
	}
}

func TestProbeToleranceIsTenPercent(t *testing.T) {
	cases := []struct {
		nominal int
		want    int
	}{
		{nominal: 1_000_000, want: 100_000},
		{nominal: 128_000, want: 12_800},
		{nominal: 9, want: 1},
		{nominal: 1, want: 1},
	}
	for _, tc := range cases {
		if got := probeTolerance(tc.nominal); got != tc.want {
			t.Fatalf("probeTolerance(%d) = %d, want %d", tc.nominal, got, tc.want)
		}
	}
}

func TestProbeRetryTargetUsesToleranceFloor(t *testing.T) {
	cases := []struct {
		nominal int
		want    int
	}{
		{nominal: 1_000_000, want: 900_000},
		{nominal: 128_000, want: 115_200},
		{nominal: 1, want: 1},
	}
	for _, tc := range cases {
		if got := probeRetryTarget(tc.nominal); got != tc.want {
			t.Fatalf("probeRetryTarget(%d) = %d, want %d", tc.nominal, got, tc.want)
		}
	}
}

func TestProbeConstructedTokens(t *testing.T) {
	cases := []struct {
		target int
		ratio  float64
		want   int
	}{
		{target: 980_000, ratio: 1, want: 980_000},
		{target: 980_000, ratio: 0.5, want: 1_960_000},
		{target: 980_000, ratio: 2, want: 490_000},
		{target: 1, ratio: 16, want: 1},
	}
	for _, tc := range cases {
		if got := probeConstructedTokens(tc.target, tc.ratio); got != tc.want {
			t.Fatalf("probeConstructedTokens(%d, %v) = %d, want %d", tc.target, tc.ratio, got, tc.want)
		}
	}
}

func TestBuildContextProbeTextUsesFixedBaseline(t *testing.T) {
	text := buildContextProbeText(2_000)
	if !strings.HasSuffix(text, " ") {
		t.Fatalf("填充文本应以空格结尾, 实际 %q", text[max(0, len(text)-16):])
	}
	got := estimateRequestTokens([]byte(text))
	if got < 2_000 || got > 2_002 {
		t.Fatalf("2000 token 的填充文本按基准口径估算 = %d, want 2000..2002", got)
	}
}

func TestParseRequestedOverflowTokens(t *testing.T) {
	cases := []struct {
		detail string
		want   int64
		ok     bool
	}{
		{detail: "you requested about 1,817,292 tokens", want: 1_817_292, ok: true},
		{detail: "requested 131072 tokens", want: 131_072, ok: true},
		{detail: "maximum context length is 131072 tokens", ok: false},
		{detail: "requested 0 tokens", ok: false},
		{detail: "Requested About 1,817,292 Tokens", want: 1_817_292, ok: true},
		{detail: "prompt is too long: 137,242 tokens > 131072 maximum", want: 137_242, ok: true},
		{detail: "PROMPT IS TOO LONG: 12345 tokens", want: 12_345, ok: true},
		{detail: strings.Repeat("x", 2_000) + " requested 9,999 tokens", want: 9_999, ok: true},
	}
	for _, tc := range cases {
		got, ok := parseRequestedOverflowTokens(tc.detail)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseRequestedOverflowTokens(%q) = (%d, %v), want (%d, %v)", tc.detail, got, ok, tc.want, tc.ok)
		}
	}
}

func TestClampProbeOutputBudget(t *testing.T) {
	chat := clampProbeOutputBudget([]byte(`{"max_tokens":4096,"reasoning_effort":"high","messages":[]}`), llm.APIFormatOpenAIChatCompletion)
	if got := gjson.GetBytes(chat, "max_tokens").Int(); got != 1 {
		t.Fatalf("chat max_tokens = %d, want 1", got)
	}
	if gjson.GetBytes(chat, "reasoning_effort").Exists() {
		t.Fatal("chat reasoning_effort 应被删除")
	}
	responses := clampProbeOutputBudget([]byte(`{"max_output_tokens":4096,"reasoning":{"effort":"high"},"input":"x"}`), llm.APIFormatOpenAIResponse)
	if got := gjson.GetBytes(responses, "max_output_tokens").Int(); got != 1 {
		t.Fatalf("responses max_output_tokens = %d, want 1", got)
	}
	if gjson.GetBytes(responses, "reasoning").Exists() {
		t.Fatal("responses reasoning 应被删除")
	}
	anthropic := clampProbeOutputBudget([]byte(`{"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[]}`), llm.APIFormatAnthropicMessage)
	if got := gjson.GetBytes(anthropic, "max_tokens").Int(); got != 1 {
		t.Fatalf("anthropic max_tokens = %d, want 1", got)
	}
	if gjson.GetBytes(anthropic, "thinking").Exists() {
		t.Fatal("anthropic thinking 应被删除")
	}
	embedding := []byte(`{"input":"x","max_tokens":7}`)
	if got := string(clampProbeOutputBudget(embedding, llm.APIFormatOpenAIEmbedding)); got != string(embedding) {
		t.Fatalf("非对话协议不应改动 body: %s", got)
	}
}

// newProbeUpstream 构造可控 usage 的假上游与命中计数。
func newProbeUpstream(t *testing.T, respond func(hits int) (int, string)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		status, body := respond(int(hits.Add(1)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, hits
}

func probeUsageResponse(promptTokens int) string {
	return fmt.Sprintf(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":%d,"completion_tokens":1,"total_tokens":%d}}`, promptTokens, promptTokens+1)
}

func TestProbeContextWindowDensityAndRetry(t *testing.T) {
	setupFailoverTest(t)
	const modelName = "probe-behavior-model"

	t.Run("容差内不重测且写密度", func(t *testing.T) {
		server, hits := newProbeUpstream(t, func(int) (int, string) { return http.StatusOK, probeUsageResponse(10_000) })
		channel := createIntegrationChannel(t, "probe-density-in", model.ChannelProviderOpenAI, server.URL, modelName)
		key := tokenDensityKey(channel.BaseURL, modelName)
		tokenDensityCache.Delete(key)
		t.Cleanup(func() { tokenDensityCache.Delete(key) })

		result, err := ProbeContextWindow(context.Background(), channel.ID, modelName, 10_000)
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		if !result.Accepted || hits.Load() != 1 {
			t.Fatalf("accepted=%v hits=%d, want true/1", result.Accepted, hits.Load())
		}
		want := clampTokenDensity(10_000.0 / 9_000.0)
		if got := modelTokenDensity(channel.BaseURL, modelName); math.Abs(got-want) > 1e-9 {
			t.Fatalf("密度 = %v, want %v", got, want)
		}
	})

	t.Run("恰好 10% 不重测", func(t *testing.T) {
		server, hits := newProbeUpstream(t, func(int) (int, string) { return http.StatusOK, probeUsageResponse(11_000) })
		channel := createIntegrationChannel(t, "probe-density-edge", model.ChannelProviderOpenAI, server.URL, modelName)
		key := tokenDensityKey(channel.BaseURL, modelName)
		tokenDensityCache.Delete(key)
		t.Cleanup(func() { tokenDensityCache.Delete(key) })

		result, err := ProbeContextWindow(context.Background(), channel.ID, modelName, 10_000)
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("恰好 10%% 不应重测, hits=%d", hits.Load())
		}
		if result.Tokens != 10_000 {
			t.Fatalf("Tokens = %d, want 10000", result.Tokens)
		}
	})

	t.Run("超过 10% 重测一次", func(t *testing.T) {
		server, hits := newProbeUpstream(t, func(hit int) (int, string) {
			if hit == 1 {
				return http.StatusOK, probeUsageResponse(12_000)
			}
			return http.StatusOK, probeUsageResponse(6_000)
		})
		channel := createIntegrationChannel(t, "probe-density-retry", model.ChannelProviderOpenAI, server.URL, modelName)
		key := tokenDensityKey(channel.BaseURL, modelName)
		tokenDensityCache.Delete(key)
		t.Cleanup(func() { tokenDensityCache.Delete(key) })

		result, err := ProbeContextWindow(context.Background(), channel.ID, modelName, 10_000)
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		if hits.Load() != 2 {
			t.Fatalf("超差应重测一次, hits=%d", hits.Load())
		}
		measured := clampTokenDensity(12_000.0 / 9_000.0)
		retryConstructed := probeConstructedTokens(probeRetryTarget(10_000), measured)
		want := clampTokenDensity(6_000.0 / float64(retryConstructed))
		if got := modelTokenDensity(channel.BaseURL, modelName); math.Abs(got-want) > 1e-9 {
			t.Fatalf("重测密度 = %v, want %v", got, want)
		}
		if !result.Accepted {
			t.Fatal("重测接受后应返回 accepted")
		}
	})

	t.Run("无 usage 不写密度不重测", func(t *testing.T) {
		server, hits := newProbeUpstream(t, func(int) (int, string) {
			return http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`
		})
		channel := createIntegrationChannel(t, "probe-density-none", model.ChannelProviderOpenAI, server.URL, modelName)
		key := tokenDensityKey(channel.BaseURL, modelName)
		tokenDensityCache.Delete(key)
		t.Cleanup(func() { tokenDensityCache.Delete(key) })

		result, err := ProbeContextWindow(context.Background(), channel.ID, modelName, 10_000)
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("无实测值不应重测, hits=%d", hits.Load())
		}
		if result.ActualTokens != 0 {
			t.Fatalf("ActualTokens = %d, want 0", result.ActualTokens)
		}
		if _, ok := tokenDensityCache.Load(key); ok {
			t.Fatal("无实测值不应写密度")
		}
	})

	t.Run("拒绝体短语在截断之外仍可解析", func(t *testing.T) {
		longBody := `{"error":{"message":"context too long: ` + strings.Repeat("x", 600) + ` you requested 12,345 tokens"}}`
		server, hits := newProbeUpstream(t, func(hit int) (int, string) {
			if hit == 1 {
				return http.StatusBadRequest, longBody
			}
			return http.StatusOK, probeUsageResponse(6_000)
		})
		channel := createIntegrationChannel(t, "probe-density-long-body", model.ChannelProviderOpenAI, server.URL, modelName)
		key := tokenDensityKey(channel.BaseURL, modelName)
		tokenDensityCache.Delete(key)
		t.Cleanup(func() { tokenDensityCache.Delete(key) })

		result, err := ProbeContextWindow(context.Background(), channel.ID, modelName, 10_000)
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		if hits.Load() != 2 {
			t.Fatalf("解析到超差规模应重测一次, hits=%d", hits.Load())
		}
		measured := clampTokenDensity(12_345.0 / 9_000.0)
		retryConstructed := probeConstructedTokens(probeRetryTarget(10_000), measured)
		want := clampTokenDensity(6_000.0 / float64(retryConstructed))
		if got := modelTokenDensity(channel.BaseURL, modelName); math.Abs(got-want) > 1e-9 {
			t.Fatalf("重测密度 = %v, want %v", got, want)
		}
		if !result.Accepted {
			t.Fatal("重测接受后应返回 accepted")
		}
	})
}
