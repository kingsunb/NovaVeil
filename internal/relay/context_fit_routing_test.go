package relay

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

func contextFitBody(groupName string, tokens int) string {
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":false}`,
		groupName, strings.Repeat("x", tokens*4))
}

func setIntegrationSyncedContextWindow(t *testing.T, channel model.Channel, modelName string, window int) {
	t.Helper()
	models := []model.ChannelModel{{
		Name:          modelName,
		Source:        model.ChannelModelSourceManual,
		ContextWindow: window,
	}}
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Models: &models}, context.Background()); err != nil {
		t.Fatalf("设置渠道 %d 同步窗口失败: %v", channel.ID, err)
	}
}

func TestContextFitSkipsSmallWindowsAndReachesLargeWindow(t *testing.T) {
	setupFailoverTest(t)
	var small1Hits, small2Hits, large1Hits, large2Hits atomic.Int64
	small1 := chatUpstream(t, &small1Hits, "small-1")
	small2 := chatUpstream(t, &small2Hits, "small-2")
	large1 := chatUpstream(t, &large1Hits, "large-1")
	large2 := chatUpstream(t, &large2Hits, "large-2")

	small1Channel := createIntegrationChannel(t, "ctx-small-1", model.ChannelProviderOpenAI, small1.URL, "ctx-small-1-model")
	small2Channel := createIntegrationChannel(t, "ctx-small-2", model.ChannelProviderOpenAI, small2.URL, "ctx-small-2-model")
	large1Channel := createIntegrationChannel(t, "ctx-large-1", model.ChannelProviderOpenAI, large1.URL, "ctx-large-1-model")
	large2Channel := createIntegrationChannel(t, "ctx-large-2", model.ChannelProviderOpenAI, large2.URL, "ctx-large-2-model")
	setIntegrationContextWindow(t, small1Channel.ID, "ctx-small-1-model", 200_000)
	setIntegrationContextWindow(t, small2Channel.ID, "ctx-small-2-model", 200_000)
	setIntegrationContextWindow(t, large1Channel.ID, "ctx-large-1-model", 1_000_000)
	setIntegrationContextWindow(t, large2Channel.ID, "ctx-large-2-model", 1_000_000)

	config := contextFitRouteConfig()
	config.SessionStickyEnabled = true
	config.SessionStickySeconds = 300
	group := createIntegrationGroup(t, "ctx-four-members", config,
		integrationLeafItem(t, small1Channel, "ctx-small-1-model"),
		integrationLeafItem(t, small2Channel, "ctx-small-2-model"),
		integrationLeafItem(t, large1Channel, "ctx-large-1-model"),
		integrationLeafItem(t, large2Channel, "ctx-large-2-model"))
	small1ID := itemIDByModelName(t, group, "ctx-small-1-model")
	large1ID := itemIDByModelName(t, group, "ctx-large-1-model")
	session := "ctx-four-members-session"
	bindSessionSticky(group, sessionScopeKey(0, session), small1ID)

	requestID := nextRequestID()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), session, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("应成功选择 1M 成员, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if small1Hits.Load() != 0 || small2Hits.Load() != 0 {
		t.Fatalf("200K 成员不应调用上游, 实际 %d/%d", small1Hits.Load(), small2Hits.Load())
	}
	if large1Hits.Load() != 1 || large2Hits.Load() != 0 {
		t.Fatalf("应由首个 1M 成员承载, 实际 %d/%d", large1Hits.Load(), large2Hits.Load())
	}
	if target, ok := stickyTargetOf(t, group.ID, session); !ok || target != large1ID {
		t.Fatalf("粘合应迁移到首个 1M 成员 %d, 实际 %d/%v", large1ID, target, ok)
	}
	if state := requestStateOf(t, requestID); state.RelayMode != "passthrough" {
		t.Fatalf("chat 到 chat 应记录 passthrough, 实际 %q", state.RelayMode)
	}
}

func TestContextFitAllSmallWindowsRejectedBeforeDispatch(t *testing.T) {
	setupFailoverTest(t)
	var small1Hits, small2Hits atomic.Int64
	small1 := chatUpstream(t, &small1Hits, "small-1")
	small2 := chatUpstream(t, &small2Hits, "small-2")
	small1Channel := createIntegrationChannel(t, "ctx-only-small-1", model.ChannelProviderOpenAI, small1.URL, "ctx-only-small-1-model")
	small2Channel := createIntegrationChannel(t, "ctx-only-small-2", model.ChannelProviderOpenAI, small2.URL, "ctx-only-small-2-model")
	setIntegrationContextWindow(t, small1Channel.ID, "ctx-only-small-1-model", 200_000)
	setIntegrationContextWindow(t, small2Channel.ID, "ctx-only-small-2-model", 200_000)
	group := createIntegrationGroup(t, "ctx-only-small", contextFitRouteConfig(),
		integrationLeafItem(t, small1Channel, "ctx-only-small-1-model"),
		integrationLeafItem(t, small2Channel, "ctx-only-small-2-model"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), "", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("应返回上下文窗口不足, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if small1Hits.Load() != 0 || small2Hits.Load() != 0 {
		t.Fatalf("全部窗口不足时不应调用上游, 实际 %d/%d", small1Hits.Load(), small2Hits.Load())
	}
}

func TestContextFitSkipsUnknownWindowAndUsesSyncedWindow(t *testing.T) {
	setupFailoverTest(t)
	var unknownHits, knownHits atomic.Int64
	unknown := chatUpstream(t, &unknownHits, "unknown")
	known := chatUpstream(t, &knownHits, "known")
	unknownChannel := createIntegrationChannel(t, "ctx-unknown", model.ChannelProviderOpenAI, unknown.URL, "ctx-unknown-model")
	knownChannel := createIntegrationChannel(t, "ctx-synced", model.ChannelProviderOpenAI, known.URL, "ctx-synced-model")
	setIntegrationSyncedContextWindow(t, unknownChannel, "ctx-unknown-model", 0)
	setIntegrationSyncedContextWindow(t, knownChannel, "ctx-synced-model", 1_000_000)
	group := createIntegrationGroup(t, "ctx-unknown-and-synced", contextFitRouteConfig(),
		integrationLeafItem(t, unknownChannel, "ctx-unknown-model"),
		integrationLeafItem(t, knownChannel, "ctx-synced-model"))
	if snapshot, err := op.GroupGetByName(group.Name); err != nil {
		t.Fatalf("读取分组失败: %v", err)
	} else if got := routableCandidateCount(snapshot); got != 2 {
		t.Fatalf("候选数 = %d, 期望 2", got)
	}

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("应跳过未知窗口并命中同步 1M 窗口, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if unknownHits.Load() != 0 {
		t.Fatalf("未知窗口成员不应调用上游, 实际 %d", unknownHits.Load())
	}
	if knownHits.Load() != 1 {
		t.Fatalf("同步 1M 窗口成员应承载一次, 实际 %d", knownHits.Load())
	}
}
