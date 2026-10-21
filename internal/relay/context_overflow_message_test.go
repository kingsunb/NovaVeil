package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

const contextOverflowRejectionBody = `{"error":{"type":"invalid_request_error","message":"maximum context length exceeded","code":"context_length_exceeded"}}`

const ordinaryDeterministicRejectionBody = `{"error":{"type":"invalid_request_error","message":"invalid parameter"}}`

func deterministicRejectServer(t *testing.T, body string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
}

func assertContextOverflowResponse(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("上下文超限应以 400 收尾, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := recorder.Body.String()
	if !strings.Contains(responseBody, errContextWindowExceeded.Error()) {
		t.Fatalf("下游应收到上下文超限提示, 实际: %s", responseBody)
	}
	if strings.Contains(responseBody, errNoAvailableChannels.Error()) {
		t.Fatalf("上下文超限不应退化为暂无可用渠道: %s", responseBody)
	}
	if strings.Contains(responseBody, "context_length_exceeded") || strings.Contains(responseBody, "maximum context length") {
		t.Fatalf("不得透传上游原始正文: %s", responseBody)
	}
}

func TestAllMembersContextOverflowReturnsOverflowMessage(t *testing.T) {
	setupFailoverTest(t)

	var firstHits, secondHits atomic.Int64
	first := deterministicRejectServer(t, contextOverflowRejectionBody, &firstHits)
	defer first.Close()
	second := deterministicRejectServer(t, contextOverflowRejectionBody, &secondHits)
	defer second.Close()

	firstModel := integrationUniqueName("it-overflow-all-first")
	secondModel := integrationUniqueName("it-overflow-all-second")
	firstChannel := createIntegrationChannel(t, "it-overflow-all-first-ch", model.ChannelProviderOpenAI, first.URL, firstModel)
	secondChannel := createIntegrationChannel(t, "it-overflow-all-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
	group := createIntegrationGroup(t, "it-overflow-all-group", refSkipConfig(),
		integrationLeafItem(t, firstChannel, firstModel),
		integrationLeafItem(t, secondChannel, secondModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	assertContextOverflowResponse(t, recorder)
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("首个成员应只尝试一次, 实际 %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("第二个成员应只尝试一次, 实际 %d", got)
	}
}

func TestMixedContextOverflowReturnsOverflowMessage(t *testing.T) {
	setupFailoverTest(t)

	var firstHits, secondHits atomic.Int64
	first := deterministicRejectServer(t, contextOverflowRejectionBody, &firstHits)
	defer first.Close()
	second := deterministicRejectServer(t, ordinaryDeterministicRejectionBody, &secondHits)
	defer second.Close()

	firstModel := integrationUniqueName("it-overflow-mixed-first")
	secondModel := integrationUniqueName("it-overflow-mixed-second")
	firstChannel := createIntegrationChannel(t, "it-overflow-mixed-first-ch", model.ChannelProviderOpenAI, first.URL, firstModel)
	secondChannel := createIntegrationChannel(t, "it-overflow-mixed-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
	group := createIntegrationGroup(t, "it-overflow-mixed-group", refSkipConfig(),
		integrationLeafItem(t, firstChannel, firstModel),
		integrationLeafItem(t, secondChannel, secondModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	assertContextOverflowResponse(t, recorder)
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("上下文超限成员应只尝试一次, 实际 %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("普通确定性 4xx 成员应只尝试一次, 实际 %d", got)
	}
}

func TestNonOverflowDeterministic4xxKeepsNoAvailableChannels(t *testing.T) {
	setupFailoverTest(t)

	var firstHits, secondHits atomic.Int64
	first := deterministicRejectServer(t, ordinaryDeterministicRejectionBody, &firstHits)
	defer first.Close()
	second := deterministicRejectServer(t, ordinaryDeterministicRejectionBody, &secondHits)
	defer second.Close()

	firstModel := integrationUniqueName("it-overflow-none-first")
	secondModel := integrationUniqueName("it-overflow-none-second")
	firstChannel := createIntegrationChannel(t, "it-overflow-none-first-ch", model.ChannelProviderOpenAI, first.URL, firstModel)
	secondChannel := createIntegrationChannel(t, "it-overflow-none-second-ch", model.ChannelProviderOpenAI, second.URL, secondModel)
	group := createIntegrationGroup(t, "it-overflow-none-group", refSkipConfig(),
		integrationLeafItem(t, firstChannel, firstModel),
		integrationLeafItem(t, secondChannel, secondModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errNoAvailableChannels.Error()) {
		t.Fatalf("普通确定性 4xx 仍应返回暂无可用渠道, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), errContextWindowExceeded.Error()) {
		t.Fatalf("普通确定性 4xx 不得误报上下文超限: %s", recorder.Body.String())
	}
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("首个成员应只尝试一次, 实际 %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("第二个成员应只尝试一次, 实际 %d", got)
	}
}

func TestSingleOpencodeContextOverflowSanitizesAtMostOnce(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := deterministicRejectServer(t, contextOverflowRejectionBody, &hits)
	defer upstream.Close()

	modelName := integrationUniqueName("it-overflow-single-opencode")
	channel := createOpencodeIntegrationChannel(t, "it-overflow-single-opencode-ch", upstream.URL, modelName)
	group := createIntegrationGroup(t, "it-overflow-single-opencode-group", refSkipConfig(),
		integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	assertContextOverflowResponse(t, recorder)
	if got := hits.Load(); got != 2 {
		t.Fatalf("单成员应只清洗重试一次后失败, 实际调用 %d 次", got)
	}
	if elapsed >= 6*time.Second {
		t.Fatalf("单成员上下文超限应立即失败, 实际耗时 %v", elapsed)
	}
}
