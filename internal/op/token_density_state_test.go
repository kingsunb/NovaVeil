package op

import (
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestTokenDensitySamplingBoundaries(t *testing.T) {
	now := time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
	today := now.Format("2006-01-02")
	cases := []struct {
		name     string
		current  model.TokenDensity
		prompt   int64
		base     int
		density  float64
		samples  int64
		daily    int
		accepted bool
	}{
		{name: "首次初始化", prompt: 4000, base: 2000, density: 2, samples: 1, daily: 1, accepted: true},
		{name: "忽略小请求", prompt: 4000, base: 1999},
		{name: "忽略零输入", base: 2000},
		{name: "密度下界", prompt: 1, base: 2000, density: 0.25, samples: 1, daily: 1, accepted: true},
		{name: "密度上界", prompt: 100000, base: 2000, density: 16, samples: 1, daily: 1, accepted: true},
		{name: "指数平滑", current: model.TokenDensity{Density: 2, SampleCount: 1, DailySamples: 1, SampleDay: today}, prompt: 2000, base: 2000, density: 1.8, samples: 2, daily: 2, accepted: true},
		{name: "第十条重置验证计数", current: model.TokenDensity{Density: 1, SampleCount: 9, DailySamples: 9, SampleDay: today}, prompt: 2000, base: 2000, density: 1, samples: 10, daily: 0, accepted: true},
		{name: "容差内保持", current: model.TokenDensity{Density: 1, SampleCount: 10, SampleDay: today}, prompt: 2040, base: 2000, density: 1, samples: 11, daily: 1, accepted: true},
		{name: "超差直接更新", current: model.TokenDensity{Density: 1, SampleCount: 10, SampleDay: today}, prompt: 2100, base: 2000, density: 1.05, samples: 11, daily: 1, accepted: true},
		{name: "每日三条封顶", current: model.TokenDensity{Density: 1, SampleCount: 13, DailySamples: 3, SampleDay: today}, prompt: 4000, base: 2000, density: 1, samples: 13, daily: 3},
		{name: "跨日重新验证", current: model.TokenDensity{Density: 1, SampleCount: 13, DailySamples: 3, SampleDay: "2026-11-19"}, prompt: 4000, base: 2000, density: 2, samples: 14, daily: 1, accepted: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			next, accepted := nextTokenDensity(test.current, test.prompt, test.base, now)
			if accepted != test.accepted || next.Density != test.density || next.SampleCount != test.samples || next.DailySamples != test.daily {
				t.Fatalf("观测结果=%+v accepted=%v, want density=%v samples=%d daily=%d accepted=%v", next, accepted, test.density, test.samples, test.daily, test.accepted)
			}
			if accepted && next.SampleDay != today {
				t.Fatalf("观测日期=%q, want %q", next.SampleDay, today)
			}
		})
	}
}
