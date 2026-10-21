package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

func contextFitRouteConfig() model.GroupRelayConfig {
	return model.GroupRelayConfig{
		MemberMaxAttempts:                     1,
		MemberRetryIntervalSeconds:            0,
		MemberNonStreamResponseTimeoutSeconds: 5,
		MemberStreamFirstEventTimeoutSeconds:  5,
		MemberCooldownSeconds:                 60,
		MemberAffinitySeconds:                 0,
		MaxRequestRounds:                      6,
		CooldownBackoffMultiplier:             2,
		CooldownMaxSeconds:                    120,
		ContextFitEnabled:                     true,
	}
}

func setIntegrationContextWindow(t *testing.T, channelID int, modelName string, window int) {
	t.Helper()
	limits := map[string]model.ChannelModelLimit{
		modelName: {ContextWindow: &window},
	}
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channelID, ModelLimits: &limits}, context.Background()); err != nil {
		t.Fatalf("设置渠道 %d 模型 %s 窗口失败: %v", channelID, modelName, err)
	}
}

func longContextBody(groupName string) string {
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":false}`,
		groupName, strings.Repeat("x", 64))
}

func chatUpstream(t *testing.T, hits *atomic.Int64, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-route-skip", content, 4, 6))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestContextSkipReleasesDirectMemberHold(t *testing.T) {
	t.Run("恢复候选", func(t *testing.T) {
		setupFailoverTest(t)
		var hits atomic.Int64
		upstream := chatUpstream(t, &hits, "should-not-serve")
		channel := createIntegrationChannel(t, "it-skip-probe-ch", model.ChannelProviderOpenAI, upstream.URL, "it-skip-probe-model")
		setIntegrationContextWindow(t, channel.ID, "it-skip-probe-model", 1)
		group := createIntegrationGroup(t, "it-skip-probe-g", contextFitRouteConfig(),
			integrationLeafItem(t, channel, "it-skip-probe-model"))
		itemID := itemIDByModelName(t, group, "it-skip-probe-model")

		probeChannelFunc = func(context.Context, model.Channel, string) error { return nil }
		seedRoute(t, group, itemID)

		engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
		recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), "", nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
			t.Fatalf("应返回上下文窗口不足, 实际 %d: %s", recorder.Code, recorder.Body.String())
		}

		snapshot := routeSnapshot(t, group.ID)
		if snapshot.ProbeItemID != 0 {
			t.Fatalf("直接成员候选占用未归还: ProbeItemID=%d", snapshot.ProbeItemID)
		}
		if len(snapshot.HalfOpens) != 0 {
			t.Fatalf("直接成员半开占用未归还: %v", snapshot.HalfOpens)
		}
		if hits.Load() != 0 {
			t.Fatalf("窗口不足时不应调用上游, 实际 %d 次", hits.Load())
		}
	})

	t.Run("紧急额度", func(t *testing.T) {
		setupFailoverTest(t)
		var hits atomic.Int64
		upstream := chatUpstream(t, &hits, "should-not-serve")
		channel := createIntegrationChannel(t, "it-skip-emergency-ch", model.ChannelProviderOpenAI, upstream.URL, "it-skip-emergency-model")
		setIntegrationContextWindow(t, channel.ID, "it-skip-emergency-model", 1)
		config := contextFitRouteConfig()
		group := createIntegrationGroup(t, "it-skip-emergency-g", config,
			integrationLeafItem(t, channel, "it-skip-emergency-model"))
		itemID := itemIDByModelName(t, group, "it-skip-emergency-model")
		config.EmergencyItemID = itemID
		if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, RelayConfig: &config}, context.Background()); err != nil {
			t.Fatalf("配置紧急成员失败: %v", err)
		}
		group, err := op.GroupGetByName(group.Name)
		if err != nil {
			t.Fatalf("读取分组失败: %v", err)
		}
		seedAllCooling(t, group)

		engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
		recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), "", nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
			t.Fatalf("应返回上下文窗口不足, 实际 %d: %s", recorder.Code, recorder.Body.String())
		}

		if _, active := emergencyActiveOf(t, group.ID); active != 0 {
			t.Fatalf("直接成员紧急额度未归还: %d", active)
		}
		if hits.Load() != 0 {
			t.Fatalf("窗口不足时不应调用上游, 实际 %d 次", hits.Load())
		}
	})
}

func TestStickyExcludedFallsBackToRegularRouting(t *testing.T) {
	setupFailoverTest(t)
	var highHits, lowHits atomic.Int64
	highUpstream := chatUpstream(t, &highHits, "should-not-serve")
	lowUpstream := chatUpstream(t, &lowHits, "served-by-low")
	highChannel := createIntegrationChannel(t, "it-sticky-skip-high", model.ChannelProviderOpenAI, highUpstream.URL, "it-sticky-skip-high-model")
	setIntegrationContextWindow(t, highChannel.ID, "it-sticky-skip-high-model", 1)
	lowChannel := createIntegrationChannel(t, "it-sticky-skip-low", model.ChannelProviderOpenAI, lowUpstream.URL, "it-sticky-skip-low-model")
	config := contextFitRouteConfig()
	config.SessionStickyEnabled = true
	config.SessionStickySeconds = 300
	group := createIntegrationGroup(t, "it-sticky-skip-g", config,
		integrationLeafItem(t, highChannel, "it-sticky-skip-high-model"),
		integrationLeafItem(t, lowChannel, "it-sticky-skip-low-model"))
	highID := itemIDByModelName(t, group, "it-sticky-skip-high-model")
	lowID := itemIDByModelName(t, group, "it-sticky-skip-low-model")
	session := "sess-sticky-skip"
	bindSessionSticky(group, sessionScopeKey(0, session), highID)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), session, nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "served-by-low") {
		t.Fatalf("粘合被排除后应回退到低优成员, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if highHits.Load() != 0 {
		t.Fatalf("窗口不足的高优成员不应调用上游, 实际 %d 次", highHits.Load())
	}
	if lowHits.Load() != 1 {
		t.Fatalf("低优成员应承载一次, 实际 %d 次", lowHits.Load())
	}
	if target, ok := stickyTargetOf(t, group.ID, session); !ok || target != lowID {
		t.Fatalf("成功后粘合应重建到低优成员 %d, 得到 %d/%v", lowID, target, ok)
	}
}

func TestRefChainStickyHonorsExclude(t *testing.T) {
	stubRelayEnv(t)
	resetStickyState()
	group := stickyTestGroup(300, stickyTestItem(21), stickyTestItem(22))
	group.ID = 2
	key := sessionScopeKey(0, "sess-ref-skip")
	bindSessionSticky(group, key, 21)

	if item := pickRefChainHop(group, key, 21); item.ID != 22 {
		t.Fatalf("引用链粘合命中排除成员时应回退选路, 实际 %d", item.ID)
	}
	if _, ok := stickyEntryOf(t, group.ID, key); ok {
		t.Fatal("引用链命中排除成员后应清除粘合")
	}
}

func TestManualPickHonorsExcludeAndCandidateCount(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(1, backoffConfig(10, 2, 30), 11, 12)
	group.Mode = model.GroupModeManual
	group.ActiveItemID = 11

	if item := pickGroupItem(group, 11); item.ID != 0 {
		t.Fatalf("手动模式应排除 ActiveItemID, 实际返回 %d", item.ID)
	}
	if item := pickGroupItem(group, 0); item.ID != 11 {
		t.Fatalf("未排除时应返回 ActiveItemID 11, 实际 %d", item.ID)
	}
	if got := routableCandidateCount(group); got != 1 {
		t.Fatalf("手动模式候选数 = %d, 期望 1", got)
	}

	nested := testFailoverGroup(3, backoffConfig(10, 2, 30), 31, 32)
	refGroup := model.Group{
		ID:          4,
		Name:        "group-4",
		Mode:        model.GroupModeManual,
		RelayConfig: backoffConfig(10, 2, 30),
		Items: []model.GroupItem{{
			ID:           41,
			GroupID:      4,
			RefGroupName: nested.Name,
		}},
		ActiveItemID: 41,
	}
	oldLookup := groupLookupFunc
	groupLookupFunc = func(name string) (model.Group, error) {
		if name == nested.Name {
			return nested, nil
		}
		return model.Group{}, fmt.Errorf("group %s not found", name)
	}
	t.Cleanup(func() { groupLookupFunc = oldLookup })
	if got := routableCandidateCount(refGroup); got != 2 {
		t.Fatalf("手动引用候选数 = %d, 期望 2", got)
	}

	failover := testFailoverGroup(2, backoffConfig(10, 2, 30), 21, 22)
	if got := routableCandidateCount(failover); got != 2 {
		t.Fatalf("故障转移候选数 = %d, 期望 2", got)
	}
}

func TestManualContextSkipTerminatesAtActiveCandidate(t *testing.T) {
	setupFailoverTest(t)
	var activeHits, backupHits atomic.Int64
	activeUpstream := chatUpstream(t, &activeHits, "should-not-serve")
	backupUpstream := chatUpstream(t, &backupHits, "should-not-serve-either")
	activeChannel := createIntegrationChannel(t, "it-manual-skip-active", model.ChannelProviderOpenAI, activeUpstream.URL, "it-manual-skip-active-model")
	setIntegrationContextWindow(t, activeChannel.ID, "it-manual-skip-active-model", 1)
	backupChannel := createIntegrationChannel(t, "it-manual-skip-backup", model.ChannelProviderOpenAI, backupUpstream.URL, "it-manual-skip-backup-model")
	group := createIntegrationGroup(t, "it-manual-skip-g", contextFitRouteConfig(),
		integrationLeafItem(t, activeChannel, "it-manual-skip-active-model"),
		integrationLeafItem(t, backupChannel, "it-manual-skip-backup-model"))
	activeID := itemIDByModelName(t, group, "it-manual-skip-active-model")

	mode := model.GroupModeManual
	if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, Mode: &mode}, context.Background()); err != nil {
		t.Fatalf("切换手动模式失败: %v", err)
	}
	if _, err := op.GroupActiveItemUpdate(group.ID, &model.GroupActiveItemUpdateRequest{ItemID: &activeID}, context.Background()); err != nil {
		t.Fatalf("设置当前成员失败: %v", err)
	}

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, longContextBody(group.Name), "", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
		t.Fatalf("手动模式首个候选不适配应立即终止, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if activeHits.Load() != 0 || backupHits.Load() != 0 {
		t.Fatalf("手动模式只应检查当前成员且不调用上游, 实际 active=%d backup=%d", activeHits.Load(), backupHits.Load())
	}
}

func TestContextSkipNestedManualRefTerminatesFirstRound(t *testing.T) {
	setupFailoverTest(t)
	var activeHits, backupHits atomic.Int64
	activeUpstream := chatUpstream(t, &activeHits, "should-not-serve")
	backupUpstream := chatUpstream(t, &backupHits, "should-not-serve-either")
	activeChannel := createIntegrationChannel(t, "it-nested-manual-active", model.ChannelProviderOpenAI, activeUpstream.URL, "it-nested-manual-active-model")
	setIntegrationContextWindow(t, activeChannel.ID, "it-nested-manual-active-model", 1)
	backupChannel := createIntegrationChannel(t, "it-nested-manual-backup", model.ChannelProviderOpenAI, backupUpstream.URL, "it-nested-manual-backup-model")
	setIntegrationContextWindow(t, backupChannel.ID, "it-nested-manual-backup-model", 1_000_000)

	manual := createIntegrationGroup(t, "it-nested-manual-g", contextFitRouteConfig(),
		integrationLeafItem(t, activeChannel, "it-nested-manual-active-model"),
		integrationLeafItem(t, backupChannel, "it-nested-manual-backup-model"))
	mode := model.GroupModeManual
	if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: manual.ID, Mode: &mode}, context.Background()); err != nil {
		t.Fatalf("切换手动模式失败: %v", err)
	}
	activeID := itemIDByModelName(t, manual, "it-nested-manual-active-model")
	if _, err := op.GroupActiveItemUpdate(manual.ID, &model.GroupActiveItemUpdateRequest{ItemID: &activeID}, context.Background()); err != nil {
		t.Fatalf("设置当前成员失败: %v", err)
	}
	top := createIntegrationGroup(t, "it-nested-manual-top", contextFitRouteConfig(), integrationRefItem(manual.Name))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, longContextBody(top.Name), "", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("嵌套手动引用应首轮从真实候选数终止, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, errContextWindowTooSmall.Error()) {
		t.Fatalf("应返回上下文窗口不足, 实际: %s", body)
	}
	if strings.Contains(body, errNoAvailableChannels.Error()) {
		t.Fatalf("不应空转到轮次上限的通用错误: %s", body)
	}
	if activeHits.Load() != 0 || backupHits.Load() != 0 {
		t.Fatalf("窗口不足时不应调用上游, 实际 active=%d backup=%d", activeHits.Load(), backupHits.Load())
	}
}

func TestContextSkipRefChainReleasesLeafHold(t *testing.T) {
	t.Run("恢复候选", func(t *testing.T) {
		setupFailoverTest(t)
		var leafHits atomic.Int64
		leafUpstream := chatUpstream(t, &leafHits, "should-not-serve")
		leafChannel := createIntegrationChannel(t, "it-ref-skip-leaf", model.ChannelProviderOpenAI, leafUpstream.URL, "it-ref-skip-leaf-model")
		setIntegrationContextWindow(t, leafChannel.ID, "it-ref-skip-leaf-model", 1)
		leafGroup := createIntegrationGroup(t, "it-ref-skip-leaf-g", contextFitRouteConfig(),
			integrationLeafItem(t, leafChannel, "it-ref-skip-leaf-model"))
		top := createIntegrationGroup(t, "it-ref-skip-top", contextFitRouteConfig(), integrationRefItem(leafGroup.Name))
		leafItemID := itemIDByModelName(t, leafGroup, "it-ref-skip-leaf-model")

		probeChannelFunc = func(context.Context, model.Channel, string) error { return nil }
		seedRoute(t, leafGroup, leafItemID)

		engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
		recorder := postRelayJSON(t, engine, path, longContextBody(top.Name), "", nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
			t.Fatalf("引用链叶子窗口不足应终止, 实际 %d: %s", recorder.Code, recorder.Body.String())
		}

		snapshot := routeSnapshot(t, leafGroup.ID)
		if snapshot.ProbeItemID != 0 {
			t.Fatalf("叶子组候选占用未归还: ProbeItemID=%d", snapshot.ProbeItemID)
		}
		if len(snapshot.HalfOpens) != 0 {
			t.Fatalf("叶子组半开占用未归还: %v", snapshot.HalfOpens)
		}
		if leafHits.Load() != 0 {
			t.Fatalf("窗口不足时不应调用上游, 实际 %d", leafHits.Load())
		}
	})

	t.Run("紧急额度", func(t *testing.T) {
		setupFailoverTest(t)
		var leafHits atomic.Int64
		leafUpstream := chatUpstream(t, &leafHits, "should-not-serve")
		leafChannel := createIntegrationChannel(t, "it-ref-emergency-leaf", model.ChannelProviderOpenAI, leafUpstream.URL, "it-ref-emergency-leaf-model")
		setIntegrationContextWindow(t, leafChannel.ID, "it-ref-emergency-leaf-model", 1)
		leafGroup := createIntegrationGroup(t, "it-ref-emergency-leaf-g", contextFitRouteConfig(),
			integrationLeafItem(t, leafChannel, "it-ref-emergency-leaf-model"))
		top := createIntegrationGroup(t, "it-ref-emergency-top", contextFitRouteConfig(), integrationRefItem(leafGroup.Name))
		refItemID := itemIDByModelName(t, top, leafGroup.Name)

		config := contextFitRouteConfig()
		config.EmergencyItemID = refItemID
		if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: top.ID, RelayConfig: &config}, context.Background()); err != nil {
			t.Fatalf("配置紧急成员失败: %v", err)
		}
		refreshed, err := op.GroupGetByName(top.Name)
		if err != nil {
			t.Fatalf("读取分组失败: %v", err)
		}
		seedAllCooling(t, refreshed)

		engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
		recorder := postRelayJSON(t, engine, path, longContextBody(refreshed.Name), "", nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), errContextWindowTooSmall.Error()) {
			t.Fatalf("紧急引用链叶子窗口不足应终止, 实际 %d: %s", recorder.Code, recorder.Body.String())
		}
		if _, active := emergencyActiveOf(t, refreshed.ID); active != 0 {
			t.Fatalf("顶层紧急额度未归还: %d", active)
		}
		if leafHits.Load() != 0 {
			t.Fatalf("窗口不足时不应调用上游, 实际 %d", leafHits.Load())
		}
	})
}
