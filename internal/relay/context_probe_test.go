package relay

import (
	"strings"
	"testing"
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
	}
	for _, tc := range cases {
		got, ok := parseRequestedOverflowTokens(tc.detail)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseRequestedOverflowTokens(%q) = (%d, %v), want (%d, %v)", tc.detail, got, ok, tc.want, tc.ok)
		}
	}
}
