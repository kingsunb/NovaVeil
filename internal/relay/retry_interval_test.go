package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestRetryWaitPreservesFractionalSeconds(t *testing.T) {
	for _, tc := range []struct {
		seconds float64
		want    time.Duration
	}{
		{0, 0},
		{0.01, 10 * time.Millisecond},
		{0.5, 500 * time.Millisecond},
		{2.5, 2500 * time.Millisecond},
		{5, 5 * time.Second},
	} {
		t.Run(fmt.Sprintf("%gs", tc.seconds), func(t *testing.T) {
			// 使用虚拟时钟验证精确间隔, 不依赖主机调度速度。
			synctest.Test(t, func(t *testing.T) {
				request := &RequestState{}
				start := time.Now()
				if !request.wait(context.Background(), tc.seconds) {
					t.Fatal("等待不应被取消")
				}
				if elapsed := time.Since(start); elapsed != tc.want {
					t.Fatalf("等待应为 %v, 实际 %v", tc.want, elapsed)
				}
			})
		})
	}
}

func TestMemberRetryZeroAndFractionalIntervals(t *testing.T) {
	for _, seconds := range []float64{0, 0.01} {
		t.Run(fmt.Sprintf("%gs", seconds), func(t *testing.T) {
			setupFailoverTest(t)
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if hits.Add(1) == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"message":"temporary failure"}}`))
					return
				}
				_, _ = w.Write(chatCompletionBody("chatcmpl-retry-ok", "retry-succeeded", 5, 7))
			}))
			defer upstream.Close()

			channel := createIntegrationChannel(t, "it-retry-interval", model.ChannelProviderOpenAI, upstream.URL, "it-retry-model")
			config := model.DefaultGroupRelayConfig()
			config.MemberMaxAttempts = 2
			config.MemberRetryIntervalSeconds = seconds
			group := createIntegrationGroup(t, "it-retry-interval", config, integrationLeafItem(t, channel, "it-retry-model"))
			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
			// 窗口小于默认 2 秒, 配置被回填默认时无法成功完成。
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			recorder := postRelayJSON(t, engine, path, body, "", ctx)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "retry-succeeded") {
				t.Fatalf("应在截止前重试成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("同一成员应恰好尝试两次, 实际 %d", got)
			}
		})
	}
}
