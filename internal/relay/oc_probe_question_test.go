package relay

import (
	"regexp"
	"strings"
	"testing"
)

// TestOcProbeRandomQuestionPool 锁定问题池轮换: 抽样覆盖全部问题, 含 %d 的
// 问题以 1-1000 随机填充且不再保留占位符。
func TestOcProbeRandomQuestionPool(t *testing.T) {
	seen := make(map[string]bool, len(ocProbeQuestions))
	distanceRe := regexp.MustCompile(`^我要去洗汽车，洗车店离我家只有(\d+)m，走路去还是坐公交车去？回复最多五个字。$`)
	distances := make(map[string]bool)
	for i := 0; i < 800; i++ {
		q := ocProbeRandomQuestion()
		if q == "" {
			t.Fatal("空问题")
		}
		if m := distanceRe.FindStringSubmatch(q); m != nil {
			distances[m[1]] = true
			seen[ocProbeQuestions[1]] = true
			continue
		}
		seen[q] = true
	}
	for i, q := range ocProbeQuestions {
		if !strings.Contains(q, "%d") && !seen[q] {
			t.Fatalf("问题[%d]未被抽到: %q", i, q)
		}
	}
	if !seen[ocProbeQuestions[1]] {
		t.Fatal("洗车问题未被抽到")
	}
	if len(distances) < 10 {
		t.Fatalf("距离随机性不足, 仅 %d 种取值", len(distances))
	}
}
