package relay

import "testing"

func TestJitteredWaitSecondsBounds(t *testing.T) {
	for _, seconds := range []float64{-1, 0, 0.001, 2, 60} {
		got := jitteredWaitSeconds(seconds)
		if seconds <= 0 {
			if got != seconds {
				t.Fatalf("非正等待不应抖动: %v -> %v", seconds, got)
			}
			continue
		}
		if got < seconds || got >= seconds*(1+retryJitterRatio) {
			t.Fatalf("抖动越界: %v -> %v", seconds, got)
		}
	}
}

func TestJitteredWaitSecondsVaries(t *testing.T) {
	seen := make(map[float64]struct{})
	for i := 0; i < 20; i++ {
		seen[jitteredWaitSeconds(10)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("重试等待应带随机抖动")
	}
}
