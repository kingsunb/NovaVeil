package relay

// 渠道并发槽位满载的准入语义集成测试:
// 满载不是上游故障, 不计入成员业务失败连击、不进入冷却、不清会话粘合;
// 单成员满载立即故障转移到下一优先级, 全部成员满载按 503+Retry-After 终止。
// 对应修复: errChannelConcurrencyFull 此前落入 recordRouteFailureIfReal 的
// business 计数, 默认 3 次即冷却健康成员并级联 failover(审计 M2)。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// holdChannelConcurrencySlot 预占指定渠道的全部并发槽位并注册清理归还。
// 返回的归还函数幂等, 与生产 release 闭包语义一致。
func holdChannelConcurrencySlot(t *testing.T, channelID int) {
	t.Helper()
	release, err := acquireChannelConcurrency(context.Background(), channelID, 1)
	if err != nil {
		t.Fatalf("预占渠道 %d 槽位失败: %v", channelID, err)
	}
	t.Cleanup(release)
}

// limitChannelConcurrency 把渠道并发上限写为 limit(经 op 层落库并刷新缓存)。
func limitChannelConcurrency(t *testing.T, channelID int, limit int) {
	t.Helper()
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channelID, MaxConcurrent: &limit}, context.Background()); err != nil {
		t.Fatalf("更新渠道 %d 并发上限失败: %v", channelID, err)
	}
}

// TestChannelConcurrencyFullDoesNotPoisonMemberHealth 验证满载成员不被计入失败:
// 高优成员槽位被占满时, 请求立即切到低优健康成员成功; 满载成员不进入冷却、
// 不累计失败等级, 其上游一次都不会被调用(本地准入在发起上游前拒绝)。
func TestChannelConcurrencyFullDoesNotPoisonMemberHealth(t *testing.T) {
	setupFailoverTest(t)
	resetChannelLimits()

	var busyHits, healthyHits atomic.Int64
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		busyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-busy", "should-never-serve", 1, 1))
	}))
	defer busy.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-ok", "ok-from-healthy-member", 5, 7))
	}))
	defer healthy.Close()

	busyChannel := createIntegrationChannel(t, "it-busy-high", model.ChannelProviderOpenAI, busy.URL, "it-model-busy")
	healthyChannel := createIntegrationChannel(t, "it-healthy-low", model.ChannelProviderOpenAI, healthy.URL, "it-model-healthy")
	limitChannelConcurrency(t, busyChannel.ID, 1)
	holdChannelConcurrencySlot(t, busyChannel.ID)

	group := createIntegrationGroup(t, "it-busy-failover",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     3,
			MemberRetryIntervalSeconds:            1,
			MemberCooldownSeconds:                 60,
			MemberNonStreamResponseTimeoutSeconds: 5,
		},
		integrationLeafItem(t, busyChannel, "it-model-busy"),
		integrationLeafItem(t, healthyChannel, "it-model-healthy"))
	busyItemID := itemIDByModelName(t, group, "it-model-busy")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到低优成员的成功响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ok-from-healthy-member") {
		t.Fatalf("响应应由健康成员承载, 实际: %s", recorder.Body.String())
	}
	if got := busyHits.Load(); got != 0 {
		t.Fatalf("满载成员上游不应被调用(准入在上游前拒绝), 实际调用 %d 次", got)
	}
	if got := healthyHits.Load(); got != 1 {
		t.Fatalf("健康成员应恰好承载一次, 实际 %d 次", got)
	}

	snapshot := routeSnapshot(t, group.ID)
	if _, cooling := snapshot.Cooldowns[busyItemID]; cooling {
		t.Fatal("满载成员不应进入冷却(本地准入拒绝不是上游故障)")
	}
	if level := snapshot.Levels[busyItemID]; level != 0 {
		t.Fatalf("满载成员不应累计失败等级, 实际 %d", level)
	}
}

// TestAllChannelsBusyRejectsWith503 验证全部成员满载时的终态:
// 请求按 503+Retry-After 拒绝, 让下游 SDK 按可重试处理; 成员健康度不受影响。
func TestAllChannelsBusyRejectsWith503(t *testing.T) {
	setupFailoverTest(t)
	resetChannelLimits()

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(chatCompletionBody("chatcmpl", "never", 1, 1))
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-busy-only", model.ChannelProviderOpenAI, upstream.URL, "it-model-busy-only")
	limitChannelConcurrency(t, channel.ID, 1)
	holdChannelConcurrencySlot(t, channel.ID)

	group := createIntegrationGroup(t, "it-busy-all",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     3,
			MemberRetryIntervalSeconds:            1,
			MemberCooldownSeconds:                 60,
			MemberNonStreamResponseTimeoutSeconds: 5,
		},
		integrationLeafItem(t, channel, "it-model-busy-only"))
	itemID := itemIDByModelName(t, group, "it-model-busy-only")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("全部成员满载应按 503 拒绝, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("503 响应应带 Retry-After: 5, 实际 %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "渠道并发已满") {
		t.Fatalf("错误文案应说明渠道并发已满, 实际: %s", recorder.Body.String())
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("满载成员上游不应被调用, 实际 %d 次", got)
	}

	snapshot := routeSnapshot(t, group.ID)
	if _, cooling := snapshot.Cooldowns[itemID]; cooling {
		t.Fatal("满载成员不应进入冷却")
	}
	if level := snapshot.Levels[itemID]; level != 0 {
		t.Fatalf("满载成员不应累计失败等级, 实际 %d", level)
	}
}

// TestClassifyRoundChannelBusy 验证满载哨兵归类为独立的 channel_busy,
// 不落入基础设施或上游错误分类。
func TestClassifyRoundChannelBusy(t *testing.T) {
	if got := classifyRound(errChannelConcurrencyFull, nil, nil); got != ErrClassChannelBusy {
		t.Fatalf("classifyRound(满载) = %q, 期望 %q", got, ErrClassChannelBusy)
	}
	if isInfrastructureError(errChannelConcurrencyFull) {
		t.Fatal("满载哨兵不应被识别为基础设施错误(它不是网络层故障)")
	}
}
