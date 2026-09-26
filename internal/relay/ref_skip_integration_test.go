package relay

// 引用链解析失败立即跳过语义的端到端集成测试(对齐 OmniRoute skipped_before_dispatch):
// 目标分组全冷却、目标分组名不存在、两引用互相成环三种结构性不可用场景下,
// 请求不等待、不计失败连击、不给引用成员上冷却, 立即切换到顶层下一优先级健康成员交付。
// 两个分组的 MemberRetryIntervalSeconds 都配成 5 秒: 旧实现会先空等一个间隔再切换,
// 断言整条链路总耗时 <2 秒即可证明没有任何等待。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

// refSkipConfig 返回重试间隔拉满到 5 秒的 Relay 配置, 用于放大旧实现的空等代价。
func refSkipConfig() model.GroupRelayConfig {
	return model.GroupRelayConfig{
		MemberMaxAttempts:                     2,
		MemberRetryIntervalSeconds:            5,
		MemberNonStreamResponseTimeoutSeconds: 5,
		MemberStreamFirstEventTimeoutSeconds:  5,
		MemberCooldownSeconds:                 60,
		MemberAffinitySeconds:                 0,
		CooldownBackoffMultiplier:             2,
		CooldownMaxSeconds:                    120,
	}
}

// refSkipHealthyUpstream 启动一个返回固定内容的健康假上游。
func refSkipHealthyUpstream(t *testing.T, id, content string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody(id, content, 3, 5))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// assertRefSkipServedAndUntouched 断言请求由健康成员承载、耗时远小于重试间隔,
// 且指定引用成员未被写入 Cooldowns 与 PostCommitStrikes。
func assertRefSkipServedAndUntouched(t *testing.T, recorder *httptest.ResponseRecorder, elapsed time.Duration,
	auto model.Group, healthyContent string, healthyHits *atomic.Int64, wantHits int64, refItemID int) {
	t.Helper()
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), healthyContent) {
		t.Fatalf("请求应立即由顶层下一优先级成员承载, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("结构性跳过不应等待重试间隔(5 秒), 实际耗时 %v", elapsed)
	}
	if got := healthyHits.Load(); got != wantHits {
		t.Fatalf("健康成员应恰好承载 %d 次, 实际 %d 次", wantHits, got)
	}
	snapshot := routeSnapshot(t, auto.ID)
	if _, cooling := snapshot.Cooldowns[refItemID]; cooling {
		t.Fatal("结构性跳过不应给引用成员写入冷却")
	}
	if strikes := snapshot.PostCommitStrikes[refItemID]; strikes != 0 {
		t.Fatalf("结构性跳过不应累计引用成员的提交后失败连击, 实际 %d", strikes)
	}
}

// TestRefSkipCoolingTargetImmediateSwitch 目标分组全冷却:
// 引用被立即跳过, 由顶层下一优先级健康直连成员承载, 引用自身不留任何惩罚记录。
func TestRefSkipCoolingTargetImmediateSwitch(t *testing.T) {
	setupFailoverTest(t)

	subServer, subHits := refSkipHealthyUpstream(t, "chatcmpl-refskip-dead", "should-not-happen")
	lowServer, lowHits := refSkipHealthyUpstream(t, "chatcmpl-refskip-low", "refskip-direct-low")

	config := refSkipConfig()
	subChannel := createIntegrationChannel(t, "it-refskip-sub-ch", model.ChannelProviderOpenAI, subServer.URL, "it-refskip-sub-model")
	lowChannel := createIntegrationChannel(t, "it-refskip-low-ch", model.ChannelProviderOpenAI, lowServer.URL, "it-refskip-low-model")
	sub := createIntegrationGroup(t, "it-refskip-sub-g", config, integrationLeafItem(t, subChannel, "it-refskip-sub-model"))
	auto := createIntegrationGroup(t, "it-refskip-auto-g", config,
		integrationRefItem(sub.Name),
		integrationLeafItem(t, lowChannel, "it-refskip-low-model"))
	refItemID := itemIDByModelName(t, auto, sub.Name)
	lowItemID := itemIDByModelName(t, auto, "it-refskip-low-model")
	subLeafID := itemIDByModelName(t, sub, "it-refskip-sub-model")

	// 目标分组唯一成员置为冷却中(未到期): 引用解析拿不到任何可选成员。
	seedAllCooling(t, sub)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)

	begin := time.Now()
	first := postRelayJSON(t, engine, path, body, "", nil)
	assertRefSkipServedAndUntouched(t, first, time.Since(begin), auto, "refskip-direct-low", lowHits, 1, refItemID)
	if got := subHits.Load(); got != 0 {
		t.Fatalf("全冷却的目标分组不应收到任何请求, 实际 %d 次", got)
	}
	if deadline, cooling := routeSnapshot(t, sub.ID).Cooldowns[subLeafID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("目标分组成员应保持原有冷却记录不被触碰, 得到 %d/%v", deadline, cooling)
	}

	// 第二个新请求重新评估该引用: 仍走立即跳过并再次由低优成员承载, 排除标记不跨请求残留。
	begin = time.Now()
	second := postRelayJSON(t, engine, path, body, "", nil)
	assertRefSkipServedAndUntouched(t, second, time.Since(begin), auto, "refskip-direct-low", lowHits, 2, refItemID)
	if _, cooling := routeSnapshot(t, auto.ID).Cooldowns[lowItemID]; cooling {
		t.Fatal("承载请求的低优成员不应进入冷却")
	}
}

// TestRefSkipMissingTargetImmediateSwitch 目标分组名解析不到:
// 悬空引用被立即跳过, 由顶层下一优先级健康成员承载, 不等待不计失败。
func TestRefSkipMissingTargetImmediateSwitch(t *testing.T) {
	setupFailoverTest(t)

	lowServer, lowHits := refSkipHealthyUpstream(t, "chatcmpl-refskip-missing-low", "refskip-missing-low")

	config := refSkipConfig()
	lowChannel := createIntegrationChannel(t, "it-refskip-miss-low-ch", model.ChannelProviderOpenAI, lowServer.URL, "it-refskip-miss-low-model")
	vanished := createIntegrationGroup(t, "it-refskip-vanished-g", config)
	auto := createIntegrationGroup(t, "it-refskip-miss-auto-g", config,
		integrationRefItem(vanished.Name),
		integrationLeafItem(t, lowChannel, "it-refskip-miss-low-model"))
	refItemID := itemIDByModelName(t, auto, vanished.Name)

	// 分组创建校验要求引用目标存在, 故创建后注入"目标名解析不到"的假查询模拟悬空引用。
	refStubGroups(t, nil)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	assertRefSkipServedAndUntouched(t, recorder, time.Since(begin), auto, "refskip-missing-low", lowHits, 1, refItemID)
}

// TestRefSkipMutualCycleImmediateSwitch 两引用互相成环:
// 运行期 visited 集合命中后立即跳过本轮顶层引用, 由顶层下一优先级健康成员承载。
func TestRefSkipMutualCycleImmediateSwitch(t *testing.T) {
	setupFailoverTest(t)

	lowServer, lowHits := refSkipHealthyUpstream(t, "chatcmpl-refskip-cycle-low", "refskip-cycle-low")
	dummyA, hitsA := refSkipHealthyUpstream(t, "chatcmpl-refskip-cyc-a", "should-not-happen-a")
	dummyB, hitsB := refSkipHealthyUpstream(t, "chatcmpl-refskip-cyc-b", "should-not-happen-b")

	config := refSkipConfig()
	channelA := createIntegrationChannel(t, "it-refskip-cyc-a-ch", model.ChannelProviderOpenAI, dummyA.URL, "it-refskip-cyc-a-model")
	channelB := createIntegrationChannel(t, "it-refskip-cyc-b-ch", model.ChannelProviderOpenAI, dummyB.URL, "it-refskip-cyc-b-model")
	lowChannel := createIntegrationChannel(t, "it-refskip-cycle-low-ch", model.ChannelProviderOpenAI, lowServer.URL, "it-refskip-cycle-low-model")
	cycA := createIntegrationGroup(t, "it-refskip-cyc-a-g", config, integrationLeafItem(t, channelA, "it-refskip-cyc-a-model"))
	cycB := createIntegrationGroup(t, "it-refskip-cyc-b-g", config, integrationLeafItem(t, channelB, "it-refskip-cyc-b-model"))
	auto := createIntegrationGroup(t, "it-refskip-cycle-auto-g", config,
		integrationRefItem(cycA.Name),
		integrationLeafItem(t, lowChannel, "it-refskip-cycle-low-model"))
	refItemID := itemIDByModelName(t, auto, cycA.Name)

	// 配置校验禁止成环, 故注入改造后的分组副本构造运行期互引: a→b→a。
	oldLookup := groupLookupFunc
	groupLookupFunc = func(name string) (model.Group, error) {
		group, err := op.GroupGetByName(name)
		if err != nil {
			return model.Group{}, err
		}
		switch group.Name {
		case cycA.Name:
			group.Items = []model.GroupItem{{ID: 990001, GroupID: group.ID, RefGroupName: cycB.Name}}
		case cycB.Name:
			group.Items = []model.GroupItem{{ID: 990002, GroupID: group.ID, RefGroupName: cycA.Name}}
		}
		return group, nil
	}
	t.Cleanup(func() { groupLookupFunc = oldLookup })

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	assertRefSkipServedAndUntouched(t, recorder, time.Since(begin), auto, "refskip-cycle-low", lowHits, 1, refItemID)
	if hitsA.Load() != 0 || hitsB.Load() != 0 {
		t.Fatalf("成环链路上的哑上游不应收到任何请求, 实际 a=%d b=%d", hitsA.Load(), hitsB.Load())
	}
	for _, group := range []model.Group{cycA, cycB} {
		snapshot := routeSnapshot(t, group.ID)
		if len(snapshot.Cooldowns) != 0 || len(snapshot.PostCommitStrikes) != 0 {
			t.Fatalf("链路中间分组 %s 不应留下冷却或连击记录, 得到 %v / %v", group.Name, snapshot.Cooldowns, snapshot.PostCommitStrikes)
		}
	}
}

// TestRefSkipAllBrokenNoHotSpin 两个兄弟引用同时结构性损坏(目标都解析不到):
// 单个 exclude 变量记不住多条死链, 无防护时会交替重选形成紧循环烧 CPU。
// 防护语义: 跳过次数超过成员总数后退避一个间隔再重新评估, 等待期间 ctx 取消即结束。
// 断言: 解析查询次数有界(非紧循环)、总耗时受控、请求以取消收尾且从未写出内容。
func TestRefSkipAllBrokenNoHotSpin(t *testing.T) {
	for _, seconds := range []float64{5, 0} {
		t.Run(fmt.Sprintf("%gs", seconds), func(t *testing.T) {
			testRefSkipAllBrokenNoHotSpin(t, seconds)
		})
	}
}

func testRefSkipAllBrokenNoHotSpin(t *testing.T, seconds float64) {
	t.Helper()
	setupFailoverTest(t)

	config := refSkipConfig()
	config.MemberRetryIntervalSeconds = seconds
	ghostA := createIntegrationGroup(t, "it-refskip-ghost-a-g", config)
	ghostB := createIntegrationGroup(t, "it-refskip-ghost-b-g", config)
	auto := createIntegrationGroup(t, "it-refskip-allbad-g", config,
		integrationRefItem(ghostA.Name),
		integrationRefItem(ghostB.Name))
	_ = itemIDByModelName(t, auto, ghostA.Name)

	// 运行期让所有分组名解析不到: 两条引用全部结构性损坏。
	refStubGroups(t, nil)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)

	var lookups atomic.Int64
	oldLookup := groupLookupFunc
	groupLookupFunc = func(name string) (model.Group, error) {
		lookups.Add(1)
		return oldLookup(name)
	}
	t.Cleanup(func() { groupLookupFunc = oldLookup })

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", ctx)
	elapsed := time.Since(begin)
	if elapsed > 3*time.Second {
		t.Fatalf("防护应在一个退避窗口内随 ctx 取消结束, 实际耗时 %v", elapsed)
	}
	if calls := lookups.Load(); calls > 20 {
		t.Fatalf("解析查询次数应有界(无热旋), 实际 %d 次", calls)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("全坏场景不应向客户端写出任何内容, 实际 %d 字节", recorder.Body.Len())
	}
}
