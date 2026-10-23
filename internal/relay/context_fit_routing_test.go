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

func drainRouteSnapshots(stream chan RouteState) []RouteState {
	var snapshots []RouteState
	for {
		select {
		case snapshot, ok := <-stream:
			if !ok {
				return snapshots
			}
			snapshots = append(snapshots, snapshot)
		default:
			return snapshots
		}
	}
}

func assertNoPublishedCurrent(t *testing.T, snapshots []RouteState, groupID int, itemIDs ...int) {
	t.Helper()
	blocked := make(map[int]bool, len(itemIDs))
	for _, itemID := range itemIDs {
		blocked[itemID] = true
	}
	for _, snapshot := range snapshots {
		if snapshot.GroupID == groupID && blocked[snapshot.CurrentItemID] {
			t.Fatalf("被窗口跳过的成员 %d 不应发布为当前承载", snapshot.CurrentItemID)
		}
	}
}

func TestContextFitDoesNotPublishSkippedMemberAsCurrent(t *testing.T) {
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
	small2ID := itemIDByModelName(t, group, "ctx-small-2-model")
	large1ID := itemIDByModelName(t, group, "ctx-large-1-model")
	session := "ctx-four-members-session"
	bindSessionSticky(group, sessionScopeKey(0, session), small1ID)

	_, stream := OpenRouteStream()
	defer CloseRouteStream(stream)
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
	snapshots := drainRouteSnapshots(stream)
	assertNoPublishedCurrent(t, snapshots, group.ID, small1ID, small2ID)
	snapshot := routeSnapshot(t, group.ID)
	if snapshot.CurrentItemID != large1ID {
		t.Fatalf("最终当前承载应为首个 1M 成员 %d, 实际 %d", large1ID, snapshot.CurrentItemID)
	}
	if _, cooling := snapshot.Cooldowns[small1ID]; cooling {
		t.Fatalf("窗口跳过不应冷却成员 %d", small1ID)
	}
	if _, cooling := snapshot.Cooldowns[small2ID]; cooling {
		t.Fatalf("窗口跳过不应冷却成员 %d", small2ID)
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
	small1ID := itemIDByModelName(t, group, "ctx-only-small-1-model")
	small2ID := itemIDByModelName(t, group, "ctx-only-small-2-model")

	_, stream := OpenRouteStream()
	defer CloseRouteStream(stream)
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), "", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("应返回上下文窗口不足, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if small1Hits.Load() != 0 || small2Hits.Load() != 0 {
		t.Fatalf("全部窗口不足时不应调用上游, 实际 %d/%d", small1Hits.Load(), small2Hits.Load())
	}
	assertNoPublishedCurrent(t, drainRouteSnapshots(stream), group.ID, small1ID, small2ID)
}

func TestContextFitRefChainDoesNotPublishSkippedMemberAsCurrent(t *testing.T) {
	setupFailoverTest(t)
	var small1Hits, small2Hits, largeHits atomic.Int64
	small1 := chatUpstream(t, &small1Hits, "small-1")
	small2 := chatUpstream(t, &small2Hits, "small-2")
	large := chatUpstream(t, &largeHits, "large")
	small1Channel := createIntegrationChannel(t, "ctx-ref-small-1", model.ChannelProviderOpenAI, small1.URL, "ctx-ref-small-1-model")
	small2Channel := createIntegrationChannel(t, "ctx-ref-small-2", model.ChannelProviderOpenAI, small2.URL, "ctx-ref-small-2-model")
	largeChannel := createIntegrationChannel(t, "ctx-ref-large", model.ChannelProviderOpenAI, large.URL, "ctx-ref-large-model")
	setIntegrationContextWindow(t, small1Channel.ID, "ctx-ref-small-1-model", 200_000)
	setIntegrationContextWindow(t, small2Channel.ID, "ctx-ref-small-2-model", 200_000)
	setIntegrationContextWindow(t, largeChannel.ID, "ctx-ref-large-model", 1_000_000)

	inner := createIntegrationGroup(t, "ctx-ref-inner", contextFitRouteConfig(),
		integrationLeafItem(t, small1Channel, "ctx-ref-small-1-model"),
		integrationLeafItem(t, small2Channel, "ctx-ref-small-2-model"),
		integrationLeafItem(t, largeChannel, "ctx-ref-large-model"))
	small1ID := itemIDByModelName(t, inner, "ctx-ref-small-1-model")
	small2ID := itemIDByModelName(t, inner, "ctx-ref-small-2-model")
	largeID := itemIDByModelName(t, inner, "ctx-ref-large-model")
	top := createIntegrationGroup(t, "ctx-ref-top", contextFitRouteConfig(), integrationRefItem(inner.Name))

	_, stream := OpenRouteStream()
	defer CloseRouteStream(stream)
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(top.Name, 689_106), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("引用链应命中 1M 成员, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if small1Hits.Load() != 0 || small2Hits.Load() != 0 {
		t.Fatalf("引用链窗口不足成员不应调用上游, 实际 %d/%d", small1Hits.Load(), small2Hits.Load())
	}
	if largeHits.Load() != 1 {
		t.Fatalf("引用链应由首个 1M 成员承载, 实际 %d", largeHits.Load())
	}
	assertNoPublishedCurrent(t, drainRouteSnapshots(stream), inner.ID, small1ID, small2ID)
	snapshot := routeSnapshot(t, inner.ID)
	if snapshot.CurrentItemID != largeID {
		t.Fatalf("内层最终当前承载应为 1M 成员 %d, 实际 %d", largeID, snapshot.CurrentItemID)
	}
	if _, cooling := snapshot.Cooldowns[small1ID]; cooling {
		t.Fatalf("窗口跳过不应冷却成员 %d", small1ID)
	}
	if _, cooling := snapshot.Cooldowns[small2ID]; cooling {
		t.Fatalf("窗口跳过不应冷却成员 %d", small2ID)
	}
}

func TestContextFitRefChainAllSkippedDoesNotPublish(t *testing.T) {
	setupFailoverTest(t)
	var small1Hits, small2Hits atomic.Int64
	small1 := chatUpstream(t, &small1Hits, "small-1")
	small2 := chatUpstream(t, &small2Hits, "small-2")
	small1Channel := createIntegrationChannel(t, "ctx-ref-all-small-1", model.ChannelProviderOpenAI, small1.URL, "ctx-ref-all-small-1-model")
	small2Channel := createIntegrationChannel(t, "ctx-ref-all-small-2", model.ChannelProviderOpenAI, small2.URL, "ctx-ref-all-small-2-model")
	setIntegrationContextWindow(t, small1Channel.ID, "ctx-ref-all-small-1-model", 200_000)
	setIntegrationContextWindow(t, small2Channel.ID, "ctx-ref-all-small-2-model", 200_000)

	inner := createIntegrationGroup(t, "ctx-ref-all-small-inner", contextFitRouteConfig(),
		integrationLeafItem(t, small1Channel, "ctx-ref-all-small-1-model"),
		integrationLeafItem(t, small2Channel, "ctx-ref-all-small-2-model"))
	small1ID := itemIDByModelName(t, inner, "ctx-ref-all-small-1-model")
	small2ID := itemIDByModelName(t, inner, "ctx-ref-all-small-2-model")
	top := createIntegrationGroup(t, "ctx-ref-all-small-top", contextFitRouteConfig(), integrationRefItem(inner.Name))
	refID := itemIDByModelName(t, top, inner.Name)

	_, stream := OpenRouteStream()
	defer CloseRouteStream(stream)
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(top.Name, 689_106), "", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("引用链全部窗口不足应返回 400, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if small1Hits.Load() != 0 || small2Hits.Load() != 0 {
		t.Fatalf("引用链全部窗口不足不应调用上游, 实际 %d/%d", small1Hits.Load(), small2Hits.Load())
	}
	snapshots := drainRouteSnapshots(stream)
	assertNoPublishedCurrent(t, snapshots, inner.ID, small1ID, small2ID)
	assertNoPublishedCurrent(t, snapshots, top.ID, refID)
}

func TestContextFitIgnoresSyncedWindowForFiltering(t *testing.T) {
	setupFailoverTest(t)
	var syncedHits, manualHits atomic.Int64
	synced := chatUpstream(t, &syncedHits, "synced-window-ignored")
	manual := chatUpstream(t, &manualHits, "manual-window-used")
	syncedChannel := createIntegrationChannel(t, "ctx-synced-only", model.ChannelProviderOpenAI, synced.URL, "ctx-synced-only-model")
	manualChannel := createIntegrationChannel(t, "ctx-manual-large", model.ChannelProviderOpenAI, manual.URL, "ctx-manual-large-model")
	setIntegrationSyncedContextWindow(t, syncedChannel, "ctx-synced-only-model", 200_000)
	setIntegrationContextWindow(t, manualChannel.ID, "ctx-manual-large-model", 1_000_000)
	group := createIntegrationGroup(t, "ctx-synced-only-and-manual", contextFitRouteConfig(),
		integrationLeafItem(t, syncedChannel, "ctx-synced-only-model"),
		integrationLeafItem(t, manualChannel, "ctx-manual-large-model"))
	if snapshot, err := op.GroupGetByName(group.Name); err != nil {
		t.Fatalf("读取分组失败: %v", err)
	} else if got := routableCandidateCount(snapshot); got != 2 {
		t.Fatalf("候选数 = %d, 期望 2", got)
	}

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("仅同步窗口的成员应照常尝试并成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if syncedHits.Load() != 1 {
		t.Fatalf("同步 200K 不应参与过滤, 首成员应由上游判定, 实际 %d", syncedHits.Load())
	}
	if manualHits.Load() != 0 {
		t.Fatalf("首成员成功后不应命中后续手动 1M 成员, 实际 %d", manualHits.Load())
	}
}

func TestContextFitKnownSmallWindowStillSkippedWithUnknownCandidate(t *testing.T) {
	setupFailoverTest(t)
	var smallHits, unknownHits atomic.Int64
	small := chatUpstream(t, &smallHits, "small")
	unknown := chatUpstream(t, &unknownHits, "unknown")
	smallChannel := createIntegrationChannel(t, "ctx-known-small", model.ChannelProviderOpenAI, small.URL, "ctx-known-small-model")
	unknownChannel := createIntegrationChannel(t, "ctx-unknown-fallback", model.ChannelProviderOpenAI, unknown.URL, "ctx-unknown-fallback-model")
	setIntegrationContextWindow(t, smallChannel.ID, "ctx-known-small-model", 200_000)
	group := createIntegrationGroup(t, "ctx-small-and-unknown", contextFitRouteConfig(),
		integrationLeafItem(t, smallChannel, "ctx-known-small-model"),
		integrationLeafItem(t, unknownChannel, "ctx-unknown-fallback-model"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, contextFitBody(group.Name, 689_106), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("已知小窗口应跳过并由未知窗口成员承接, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if smallHits.Load() != 0 {
		t.Fatalf("已知 200K 窗口成员不应调用上游, 实际 %d", smallHits.Load())
	}
	if unknownHits.Load() != 1 {
		t.Fatalf("未知窗口成员应承载一次, 实际 %d", unknownHits.Load())
	}
	if strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("存在未知窗口候选时不应本地拒绝: %s", recorder.Body.String())
	}
}
