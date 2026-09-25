package relay

// 端到端集成测试: 通过 Forward gin 入口 + httptest 假上游验证管理端"终止请求"
// 在轮间退避等待中立即生效(修复前的 bug 场景)。

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// TestStopRequestDuringRetryWaitEndToEnd 验证完整转发路径下的修复:
// 上游持续 500 → 首轮失败 → 进入 MemberRetryIntervalSeconds=10 退避等待 →
// 管理端调用 StopRequestByID → wait 被 stopCh 立即唤醒 → 请求以 canceled 终态定稿,
// 而非卡在 10 秒退避中继续显示"进行中"。
func TestStopRequestDuringRetryWaitEndToEnd(t *testing.T) {
	setupFailoverTest(t)

	// 假上游始终返回 500, 使每轮都失败并进入退避等待。
	// hits 由上游 handler goroutine 写、测试主 goroutine 轮询读, 必须原子访问(race 检测下裸 int 报 DATA RACE)。
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream broken"}}`))
	}))
	defer upstream.Close()

	ch := createIntegrationChannel(t, "it-stop-wait", model.ChannelProviderOpenAI, upstream.URL, "it-stop-model")
	group := createIntegrationGroup(t, "it-stop-wait",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     3,
			MemberRetryIntervalSeconds:            10, // 长退避: 修复前 StopRequest 无法唤醒
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
		},
		integrationLeafItem(t, ch, "it-stop-model"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)

	// 预告请求 ID, 供管理端终止调用使用。
	reqID := nextRequestID()

	// 在 goroutine 中发起请求(ServeHTTP 阻塞至请求结束)。
	done := make(chan struct{})
	go func() {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, req)
		close(done)
	}()

	// 等待首轮上游调用发生, 确认请求已进入退避等待。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() < 1 {
		t.Fatal("上游未被调用, 请求未正常启动")
	}

	// 此时首轮已失败, 请求应正在 10 秒退避 wait 中。
	// 调用管理端终止(模拟面板"终止请求"按钮)。
	start := time.Now()
	if !StopRequestByID(reqID) {
		t.Fatalf("StopRequestByID(%d) 返回 false, 请求不存在", reqID)
	}

	// 请求应在毫秒级结束(被 stopCh 唤醒), 远早于 10 秒退避。
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("请求应被立即终止, 实际耗时 %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopRequestByID 未能终止请求, 仍卡在退避等待中(修复未生效)")
	}

	// 验证终态为 canceled 而非 running/failed。
	state := requestStateOf(t, reqID)
	if state.Status != StatusCanceled {
		t.Fatalf("终态应为 canceled, 实际 %q", state.Status)
	}
	if state.Class != ErrClassClientCancel {
		t.Fatalf("终态分类应为 client_cancel, 实际 %q", state.Class)
	}

	// 只应发生首轮 1 次上游调用, 终止后不应继续重试。
	if hits.Load() != 1 {
		t.Fatalf("终止后不应继续重试, 实际上游调用 %d 次", hits.Load())
	}
}
