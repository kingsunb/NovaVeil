package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

// createFreeLaneChannel 构造免费档渠道并注入免费档通道属性。
func createFreeLaneChannel(t *testing.T, baseURL, modelName string) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name:    integrationUniqueName("it-free-log-ch"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: baseURL,
		Key:     "integration-key",
		Models:  []model.ChannelModel{{Name: modelName, Source: model.ChannelModelSourceManual, UpstreamProtocol: model.UpstreamProtocolChat}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建免费档渠道失败: %v", err)
	}
	// 用户创建会被强制降级为非免费渠道, 测试内把免费档标记写回库并刷新缓存。
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).
		Update("opencode_compat", true).Error; err != nil {
		t.Fatalf("回注免费档标记失败: %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("刷新渠道缓存失败: %v", err)
	}
	return channel
}

// freeLaneSSEUpstream 返回免费档要求的流式 Chat 响应。
func freeLaneSSEUpstream(t *testing.T, hits *atomic.Int64, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"`+content+`"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFreeLaneGlobalWindowAppliesToRoutingAndAttemptLog(t *testing.T) {
	setupFailoverTest(t)
	original := opencodeFreeContextWindow
	opencodeFreeContextWindow = func() int { return 1_000_000 }
	t.Cleanup(func() { opencodeFreeContextWindow = original })

	var hits atomic.Int64
	upstream := freeLaneSSEUpstream(t, &hits, "free-lane-ok")
	channel := createFreeLaneChannel(t, upstream.URL+"/inference", "deepseek-v4-flash-free")
	group := createIntegrationGroup(t, "it-free-log-fit", contextFitRouteConfig(),
		integrationLeafItem(t, channel, "deepseek-v4-flash-free"))

	id := nextRequestID()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("应成功转发, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("上游应被调用 1 次, 实际 %d", hits.Load())
	}

	state := requestStateOf(t, id)
	if state.ContextFitState != "fits" {
		t.Fatalf("请求级状态 = %q, want fits", state.ContextFitState)
	}
	if state.EstimatedTokens <= 0 {
		t.Fatalf("请求级预估 token 应大于 0, got %d", state.EstimatedTokens)
	}
	if state.ContextWindowMax != 1_000_000 {
		t.Fatalf("请求级生效窗口 = %d, want 1000000", state.ContextWindowMax)
	}
	if len(state.Attempts) != 1 {
		t.Fatalf("尝试记录数 = %d, want 1", len(state.Attempts))
	}
	attempt := state.Attempts[0]
	if attempt.EffectiveWindow != 1_000_000 {
		t.Fatalf("尝试级生效窗口 = %d, want 1000000", attempt.EffectiveWindow)
	}
	if attempt.EstimatedTokens <= 0 || attempt.Density <= 0 {
		t.Fatalf("尝试级预估/密度异常: tokens=%d density=%v", attempt.EstimatedTokens, attempt.Density)
	}
	if attempt.DensitySource != "default" {
		t.Fatalf("未见真实流量时密度来源应为 default, got %q", attempt.DensitySource)
	}
}

func TestFreeLaneGlobalWindowSkipsOversizeMember(t *testing.T) {
	setupFailoverTest(t)
	original := opencodeFreeContextWindow
	opencodeFreeContextWindow = func() int { return 1 }
	t.Cleanup(func() { opencodeFreeContextWindow = original })

	var hits atomic.Int64
	upstream := freeLaneSSEUpstream(t, &hits, "should-not-serve")
	channel := createFreeLaneChannel(t, upstream.URL+"/inference", "deepseek-v4-flash-free")
	group := createIntegrationGroup(t, "it-free-log-skip", contextFitRouteConfig(),
		integrationLeafItem(t, channel, "deepseek-v4-flash-free"))

	id := nextRequestID()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), "", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("应返回上下文窗口不足, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("窗口不足不应调用上游, 实际 %d", hits.Load())
	}

	state := requestStateOf(t, id)
	if state.ContextFitState != "skipped" {
		t.Fatalf("请求级状态 = %q, want skipped", state.ContextFitState)
	}
	if state.ContextSkipped < 1 {
		t.Fatalf("请求级跳过成员数 = %d, want >=1", state.ContextSkipped)
	}
	if state.ContextWindowMax != 1 {
		t.Fatalf("请求级生效窗口 = %d, want 1", state.ContextWindowMax)
	}
	if len(state.Attempts) != 0 {
		t.Fatalf("被跳过时不应产生尝试记录, got %d", len(state.Attempts))
	}
}
