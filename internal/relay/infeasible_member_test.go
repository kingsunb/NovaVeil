package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

func infeasibleSkipConfig() model.GroupRelayConfig {
	config := refSkipConfig()
	config.MemberMaxAttempts = 1
	config.MemberRetryIntervalSeconds = 0
	return config
}

func fixedStatusUpstream(t *testing.T, status int, body string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func postInfeasibleGroup(t *testing.T, group model.Group) *httptest.ResponseRecorder {
	t.Helper()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	return postRelayJSON(t, engine, path, body, "", nil)
}

func assertItemNotCooling(t *testing.T, groupID, itemID int) {
	t.Helper()
	if _, cooling := routeSnapshot(t, groupID).Cooldowns[itemID]; cooling {
		t.Fatalf("成员 %d 不应因请求级不可用进入冷却", itemID)
	}
}

func TestRequestTooLargeSkipsMemberWithoutCooldown(t *testing.T) {
	setupFailoverTest(t)
	var largeHits, healthyHits atomic.Int64
	large := fixedStatusUpstream(t, http.StatusRequestEntityTooLarge, `{"error":{"message":"request too large"}}`, &largeHits)
	healthy := chatUpstream(t, &healthyHits, "healthy-after-413")
	largeChannel := createIntegrationChannel(t, "it-413-first", model.ChannelProviderOpenAI, large.URL, "it-413-first-model")
	healthyChannel := createIntegrationChannel(t, "it-413-second", model.ChannelProviderOpenAI, healthy.URL, "it-413-second-model")
	group := createIntegrationGroup(t, "it-413-failover", infeasibleSkipConfig(),
		integrationLeafItem(t, largeChannel, "it-413-first-model"),
		integrationLeafItem(t, healthyChannel, "it-413-second-model"))
	largeID := itemIDByModelName(t, group, "it-413-first-model")

	for attempt := 1; attempt <= 2; attempt++ {
		recorder := postInfeasibleGroup(t, group)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "healthy-after-413") {
			t.Fatalf("第 %d 次请求应由健康成员承载, 实际 %d: %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if largeHits.Load() != 2 {
		t.Fatalf("413 成员每次请求都应由上游重新判定, 实际调用 %d 次", largeHits.Load())
	}
	if healthyHits.Load() != 2 {
		t.Fatalf("健康成员应承载两次, 实际 %d", healthyHits.Load())
	}
	assertItemNotCooling(t, group.ID, largeID)
}

func TestContextOverflowSkipsMemberWithoutCooldown(t *testing.T) {
	setupFailoverTest(t)
	var overflowHits, healthyHits atomic.Int64
	overflow := fixedStatusUpstream(t, http.StatusBadRequest, contextOverflowRejectionBody, &overflowHits)
	healthy := chatUpstream(t, &healthyHits, "healthy-after-overflow")
	overflowChannel := createIntegrationChannel(t, "it-overflow-skip", model.ChannelProviderOpenAI, overflow.URL, "it-overflow-skip-model")
	healthyChannel := createIntegrationChannel(t, "it-overflow-healthy", model.ChannelProviderOpenAI, healthy.URL, "it-overflow-healthy-model")
	group := createIntegrationGroup(t, "it-overflow-skip-group", infeasibleSkipConfig(),
		integrationLeafItem(t, overflowChannel, "it-overflow-skip-model"),
		integrationLeafItem(t, healthyChannel, "it-overflow-healthy-model"))
	overflowID := itemIDByModelName(t, group, "it-overflow-skip-model")

	for attempt := 1; attempt <= 2; attempt++ {
		recorder := postInfeasibleGroup(t, group)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "healthy-after-overflow") {
			t.Fatalf("第 %d 次请求应由健康成员承载, 实际 %d: %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if overflowHits.Load() != 2 {
		t.Fatalf("上下文超限成员每次请求都应重新尝试, 实际调用 %d 次", overflowHits.Load())
	}
	if healthyHits.Load() != 2 {
		t.Fatalf("健康成员应承载两次, 实际 %d", healthyHits.Load())
	}
	assertItemNotCooling(t, group.ID, overflowID)
}

func TestAllMembersRequestTooLargeReturns413WithoutCooldown(t *testing.T) {
	setupFailoverTest(t)
	var firstHits, secondHits atomic.Int64
	first := fixedStatusUpstream(t, http.StatusRequestEntityTooLarge, contextOverflowRejectionBody, &firstHits)
	second := fixedStatusUpstream(t, http.StatusRequestEntityTooLarge, contextOverflowRejectionBody, &secondHits)
	firstChannel := createIntegrationChannel(t, "it-413-all-first", model.ChannelProviderOpenAI, first.URL, "it-413-all-first-model")
	secondChannel := createIntegrationChannel(t, "it-413-all-second", model.ChannelProviderOpenAI, second.URL, "it-413-all-second-model")
	group := createIntegrationGroup(t, "it-413-all-group", infeasibleSkipConfig(),
		integrationLeafItem(t, firstChannel, "it-413-all-first-model"),
		integrationLeafItem(t, secondChannel, "it-413-all-second-model"))
	firstID := itemIDByModelName(t, group, "it-413-all-first-model")
	secondID := itemIDByModelName(t, group, "it-413-all-second-model")

	expectedID := nextRequestID()
	recorder := postInfeasibleGroup(t, group)
	if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), errRequestTooLarge.Error()) {
		t.Fatalf("全部 413 应返回明确提示与 413, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "输入可能超出上游上下文窗口") {
		t.Fatalf("413 命中上下文超限正文时应追加提示: %s", recorder.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("每个成员应只尝试一次, 实际 %d/%d", firstHits.Load(), secondHits.Load())
	}
	assertItemNotCooling(t, group.ID, firstID)
	assertItemNotCooling(t, group.ID, secondID)
	state := requestStateOf(t, expectedID)
	if state.Class != ErrClassUpstream4xx {
		t.Fatalf("413 终态分类 = %q, want %q", state.Class, ErrClassUpstream4xx)
	}
}

func TestAllMembersContextOverflowReturns400WithoutCooldown(t *testing.T) {
	setupFailoverTest(t)
	var firstHits, secondHits atomic.Int64
	first := fixedStatusUpstream(t, http.StatusBadRequest, contextOverflowRejectionBody, &firstHits)
	second := fixedStatusUpstream(t, http.StatusBadRequest, contextOverflowRejectionBody, &secondHits)
	firstChannel := createIntegrationChannel(t, "it-overflow-all-skip-first", model.ChannelProviderOpenAI, first.URL, "it-overflow-all-skip-first-model")
	secondChannel := createIntegrationChannel(t, "it-overflow-all-skip-second", model.ChannelProviderOpenAI, second.URL, "it-overflow-all-skip-second-model")
	group := createIntegrationGroup(t, "it-overflow-all-skip-group", infeasibleSkipConfig(),
		integrationLeafItem(t, firstChannel, "it-overflow-all-skip-first-model"),
		integrationLeafItem(t, secondChannel, "it-overflow-all-skip-second-model"))
	firstID := itemIDByModelName(t, group, "it-overflow-all-skip-first-model")
	secondID := itemIDByModelName(t, group, "it-overflow-all-skip-second-model")

	expectedID := nextRequestID()
	recorder := postInfeasibleGroup(t, group)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowExceeded.Error()) {
		t.Fatalf("全部上下文超限应返回明确 400, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("每个成员应只尝试一次, 实际 %d/%d", firstHits.Load(), secondHits.Load())
	}
	assertItemNotCooling(t, group.ID, firstID)
	assertItemNotCooling(t, group.ID, secondID)
	state := requestStateOf(t, expectedID)
	if state.Class != ErrClassUpstream4xx {
		t.Fatalf("上下文超限终态分类 = %q, want %q", state.Class, ErrClassUpstream4xx)
	}
}

func TestOrdinaryDeterministic400StillCoolsMembers(t *testing.T) {
	setupFailoverTest(t)
	var firstHits, secondHits atomic.Int64
	first := fixedStatusUpstream(t, http.StatusBadRequest, ordinaryDeterministicRejectionBody, &firstHits)
	second := fixedStatusUpstream(t, http.StatusBadRequest, ordinaryDeterministicRejectionBody, &secondHits)
	firstChannel := createIntegrationChannel(t, "it-ordinary-400-first", model.ChannelProviderOpenAI, first.URL, "it-ordinary-400-first-model")
	secondChannel := createIntegrationChannel(t, "it-ordinary-400-second", model.ChannelProviderOpenAI, second.URL, "it-ordinary-400-second-model")
	group := createIntegrationGroup(t, "it-ordinary-400-group", infeasibleSkipConfig(),
		integrationLeafItem(t, firstChannel, "it-ordinary-400-first-model"),
		integrationLeafItem(t, secondChannel, "it-ordinary-400-second-model"))
	firstID := itemIDByModelName(t, group, "it-ordinary-400-first-model")
	secondID := itemIDByModelName(t, group, "it-ordinary-400-second-model")

	recorder := postInfeasibleGroup(t, group)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errNoAvailableChannels.Error()) {
		t.Fatalf("普通确定性 400 应保持统一 400, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("每个成员应只失败一次, 实际 %d/%d", firstHits.Load(), secondHits.Load())
	}
	for _, itemID := range []int{firstID, secondID} {
		if _, cooling := routeSnapshot(t, group.ID).Cooldowns[itemID]; !cooling {
			t.Fatalf("普通确定性 400 应继续冷却成员 %d", itemID)
		}
	}
}
