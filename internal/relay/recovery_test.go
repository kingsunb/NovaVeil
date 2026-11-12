package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// stubRelayEnv 备份路由全局状态并替换为空环境, 测试结束自动还原注入点。
// 还原前先短暂等待在途异步探测退出, 并在 routeMu 临界区内写回全局变量:
// 泄漏的探测 goroutine 对注入点与 routes 的读取均在锁内或早于其收尾临界区,
// 以此建立 happens-before, 避免清理还原与在途 goroutine 构成数据竞争。
func stubRelayEnv(t *testing.T) {
	t.Helper()
	oldProbe := probeChannelFunc
	oldLookup := channelLookupFunc
	oldRoutes := routes
	routes = make(map[int]*RouteState)
	t.Cleanup(func() {
		deadline := time.Now().Add(250 * time.Millisecond)
		for {
			routeMu.Lock()
			pending := 0
			for _, route := range routes {
				pending += len(route.HalfOpens)
			}
			if pending == 0 {
				routeMu.Unlock()
				break
			}
			routeMu.Unlock()
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		routeMu.Lock()
		defer routeMu.Unlock()
		probeChannelFunc = oldProbe
		channelLookupFunc = oldLookup
		routes = oldRoutes
	})
	channelLookupFunc = func(id int) (model.Channel, error) { return model.Channel{ID: id, Enabled: true}, nil }
}

// testFailoverGroup 构造故障转移分组, ids 为按优先级升序排列的成员 ID。
func testFailoverGroup(id int, config model.GroupRelayConfig, ids ...int) model.Group {
	items := make([]model.GroupItem, 0, len(ids))
	for i, itemID := range ids {
		items = append(items, model.GroupItem{ID: itemID, GroupID: id, ChannelModel: &model.ChannelModel{ChannelID: 1000 + itemID, Name: fmt.Sprintf("model-%d", itemID)}, Priority: i})
	}
	return model.Group{ID: id, Name: fmt.Sprintf("group-%d", id), Mode: model.GroupModeFailover, RelayConfig: config, Items: items}
}

// backoffConfig 返回指定退避参数的 Relay 配置。
func backoffConfig(cooldownSeconds int, multiplier float64, maxSeconds int) model.GroupRelayConfig {
	config := model.DefaultGroupRelayConfig()
	config.MemberCooldownSeconds = cooldownSeconds
	config.CooldownBackoffMultiplier = multiplier
	config.CooldownMaxSeconds = maxSeconds
	config.RequestProbeEnabled = true // 既有用例覆盖开启请求探测的恢复路径。
	return config
}

// seedExpired 把指定成员置为冷却到期的 OPEN 状态。
func seedExpired(route *RouteState, now int64, itemIDs ...int) {
	for _, itemID := range itemIDs {
		route.Cooldowns[itemID] = now - 1000
	}
}

// assertCooldownApprox 校验冷却截止与期望时长误差不超过一秒。
func assertCooldownApprox(t *testing.T, name string, deadline, now, wantMillis int64) {
	t.Helper()
	delta := deadline - now
	if delta < wantMillis-1000 || delta > wantMillis+1000 {
		t.Fatalf("%s 冷却时长 = %dms, 期望约 %dms", name, delta, wantMillis)
	}
}

// TestPickGroupItemHalfOpenAtomicNoDuplicateProbe 验证并发选路时到期成员整批原子切换 HALF_OPEN 且不重复探测。
func TestPickGroupItemHalfOpenAtomicNoDuplicateProbe(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(1, backoffConfig(10, 2, 30), 11, 12)
	now := time.Now().UnixMilli()
	routes[group.ID] = &RouteState{
		GroupID:   group.ID,
		Cooldowns: make(map[int]int64),
		Levels:    make(map[int]int),
		HalfOpens: make(map[int]int64),
	}
	seedExpired(routes[group.ID], now, 11, 12)

	var probes atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		probes.Add(1)
		<-release
		return nil
	}
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	const concurrency = 8
	var wg sync.WaitGroup
	results := make(chan model.GroupItem, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- pickGroupItem(group, 0)
		}()
	}

	// 等待两个成员都恰好被探测一次。
	deadline := time.Now().Add(5 * time.Second)
	for probes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if probes.Load() != 2 {
		t.Fatalf("每个成员只应探测一次, 实际探测 %d 次", probes.Load())
	}

	// 全部成员处于 HALF_OPEN 时其他请求应直接让路且不触发新探测。
	if item := pickGroupItem(group, 0); item.ID != 0 {
		t.Fatalf("成员探测中时应返回零值成员, 实际 %d", item.ID)
	}
	if probes.Load() != 2 {
		t.Fatalf("HALF_OPEN 成员不应被重复探测, 实际 %d 次", probes.Load())
	}

	once.Do(func() { close(release) })
	wg.Wait()
	close(results)

	winners := 0
	for item := range results {
		if item.ID != 0 {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发下应有且仅有一个请求获得候选, 实际 %d 个", winners)
	}

	route := routes[group.ID]
	if route.ProbeItemID == 0 {
		t.Fatal("最先成功者应成为业务确认候选")
	}
	if _, probing := route.HalfOpens[route.ProbeItemID]; !probing {
		t.Fatal("候选成员应保持 HALF_OPEN 直到业务确认")
	}
	if len(route.HalfOpens) != 1 {
		t.Fatalf("其余候选应停止探测, 当前仍在探测的成员数 %d", len(route.HalfOpens))
	}
}

// TestRecoverExpiredItemsFirstSuccessWins 验证并行探测取最先成功者为候选, 其余成员保持原冷却。
func TestPendingProbeCandidateBlocksLateRecoveryBatch(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(20, backoffConfig(10, 2, 30), 201, 202)
	now := time.Now().UnixMilli()
	routes[group.ID] = &RouteState{
		GroupID:     group.ID,
		ProbeItemID: 201,
		Cooldowns:   map[int]int64{201: now - 1000, 202: now - 1000},
		Levels:      make(map[int]int),
		HalfOpens:   map[int]int64{201: now},
	}

	var probes atomic.Int32
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		probes.Add(1)
		return nil
	}

	if item := pickGroupItem(group, 0); item.ID != 0 {
		t.Fatalf("late request must wait for pending candidate, got member %d", item.ID)
	}
	ProbeExpiredItems(group)
	if got := probes.Load(); got != 0 {
		t.Fatalf("pending candidate must block request and background probes, got %d", got)
	}
	route := routes[group.ID]
	if route.ProbeItemID != 201 || len(route.HalfOpens) != 1 || route.HalfOpens[201] == 0 {
		t.Fatalf("pending candidate state changed unexpectedly: %+v", route)
	}
}

func TestRecoverExpiredItemsFirstSuccessWins(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(2, backoffConfig(10, 2, 30), 21, 22)
	now := time.Now().UnixMilli()
	expiredDeadline := now - 1000
	routes[group.ID] = &RouteState{
		GroupID:   group.ID,
		Cooldowns: map[int]int64{21: expiredDeadline, 22: expiredDeadline},
		Levels:    make(map[int]int),
		HalfOpens: make(map[int]int64),
	}

	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		if modelName == "model-21" {
			time.Sleep(20 * time.Millisecond)
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}

	item := pickGroupItem(group, 0)
	if item.ID != 21 {
		t.Fatalf("应返回最先成功的候选成员 21, 实际 %d", item.ID)
	}
	route := routes[group.ID]
	if route.ProbeItemID != 21 {
		t.Fatalf("候选应为成员 21, 实际 %d", route.ProbeItemID)
	}
	if _, probing := route.HalfOpens[21]; !probing {
		t.Fatal("候选成员应处于 HALF_OPEN")
	}
	if _, probing := route.HalfOpens[22]; probing {
		t.Fatal("被取消的成员不应停留在 HALF_OPEN")
	}
	if route.Cooldowns[22] != expiredDeadline {
		t.Fatalf("被取消成员应保持原冷却记录, 实际 %d", route.Cooldowns[22])
	}
	if level := route.Levels[21]; level != 0 {
		t.Fatalf("探测成功不应提升冷却等级, 实际等级 %d", level)
	}
}

// TestCandidateBusinessFailureReopensWithBackoff 验证业务二次确认失败的重新 OPEN 与保持 CLOSED 分支。
func TestCandidateBusinessFailureReopensWithBackoff(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(3, backoffConfig(10, 3, 100), 31)
	group.RelayConfig.MemberMaxAttempts = 1
	now := time.Now().UnixMilli()
	routes[group.ID] = &RouteState{
		GroupID:     group.ID,
		ProbeItemID: 31,
		Cooldowns:   map[int]int64{31: now - 1000},
		Levels:      map[int]int{31: 1},
		HalfOpens:   map[int]int64{31: now},
	}

	if !recordRouteFailure(group, 31, 1) {
		t.Fatal("候选确认失败应立即换渠道重扫")
	}
	route := routes[group.ID]
	if level := route.Levels[31]; level != 2 {
		t.Fatalf("达到阈值应等级加一, 实际等级 %d", level)
	}
	assertCooldownApprox(t, "达阈值候选", route.Cooldowns[31], now, 90_000)
	if _, probing := route.HalfOpens[31]; probing {
		t.Fatal("重新 OPEN 后不应停留在 HALF_OPEN")
	}
	if route.ProbeItemID != 0 {
		t.Fatalf("重新 OPEN 后应清除候选, 实际 %d", route.ProbeItemID)
	}

	// 未达阈值时保持 CLOSED 仅解除探测占用。
	group2 := testFailoverGroup(4, backoffConfig(10, 3, 100), 41)
	group2.RelayConfig.MemberMaxAttempts = 2
	routes[group2.ID] = &RouteState{
		GroupID:     group2.ID,
		ProbeItemID: 41,
		Cooldowns:   map[int]int64{41: now - 1000},
		Levels:      make(map[int]int),
		HalfOpens:   map[int]int64{41: now},
	}
	if !recordRouteFailure(group2, 41, 1) {
		t.Fatal("未达阈值也应换渠道重扫而非重试同一成员")
	}
	route2 := routes[group2.ID]
	if _, cooling := route2.Cooldowns[41]; cooling {
		t.Fatal("未达阈值的候选应保持 CLOSED")
	}
	if route2.Levels[41] != 0 {
		t.Fatalf("未达阈值不应提升冷却等级, 实际等级 %d", route2.Levels[41])
	}
}

// TestWholeBatchFailureBacksOffAndCaps 验证整批探测失败等级加一、倍数退避且不超过上限。
func TestWholeBatchFailureBacksOffAndCaps(t *testing.T) {
	stubRelayEnv(t)
	group := testFailoverGroup(5, backoffConfig(10, 2, 30), 51, 52)
	routes[group.ID] = &RouteState{
		GroupID:   group.ID,
		Cooldowns: make(map[int]int64),
		Levels:    make(map[int]int),
		HalfOpens: make(map[int]int64),
	}
	failure := errors.New("probe failed")
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		return failure
	}

	now := time.Now().UnixMilli()
	seedExpired(routes[group.ID], now, 51, 52)
	if item := pickGroupItem(group, 0); item.ID != 0 {
		t.Fatalf("整批失败应返回零值成员, 实际 %d", item.ID)
	}
	route := routes[group.ID]
	for _, itemID := range []int{51, 52} {
		if level := route.Levels[itemID]; level != 1 {
			t.Fatalf("成员 %d 应升级到等级 1, 实际 %d", itemID, level)
		}
		assertCooldownApprox(t, fmt.Sprintf("等级 1 成员 %d", itemID), route.Cooldowns[itemID], now, 20_000)
	}
	if len(route.HalfOpens) != 0 {
		t.Fatalf("整批失败后不应有成员停留在 HALF_OPEN, 实际 %d 个", len(route.HalfOpens))
	}

	// 第二轮: 冷却再次到期后退避到更高等级并被上限封顶。
	now = time.Now().UnixMilli()
	seedExpired(routes[group.ID], now, 51, 52)
	if item := pickGroupItem(group, 0); item.ID != 0 {
		t.Fatalf("第二轮整批失败应返回零值成员, 实际 %d", item.ID)
	}
	route = routes[group.ID]
	for _, itemID := range []int{51, 52} {
		if level := route.Levels[itemID]; level != 2 {
			t.Fatalf("成员 %d 应升级到等级 2, 实际 %d", itemID, level)
		}
		assertCooldownApprox(t, fmt.Sprintf("封顶成员 %d", itemID), route.Cooldowns[itemID], now, 30_000)
	}
}

// TestPickGroupItemRequestProbeDefaultDisabledUsesExpiredDirectly 验证默认关闭请求探测后由真实业务直接恢复首个到期成员。
func TestPickGroupItemRequestProbeDefaultDisabledUsesExpiredDirectly(t *testing.T) {
	stubRelayEnv(t)
	config := backoffConfig(10, 2, 30)
	config.RequestProbeEnabled = false
	group := testFailoverGroup(6, config, 61, 62)
	now := time.Now().UnixMilli()
	routes[group.ID] = &RouteState{
		GroupID:   group.ID,
		Cooldowns: make(map[int]int64),
		Levels:    make(map[int]int),
		HalfOpens: make(map[int]int64),
	}
	seedExpired(routes[group.ID], now, 61, 62)

	var probes atomic.Int32
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		probes.Add(1)
		return nil
	}

	item := pickGroupItem(group, 0)
	if item.ID != 61 {
		t.Fatalf("应直接返回优先级最高的到期成员 61, 实际 %d", item.ID)
	}
	route := routes[group.ID]
	if route.ProbeItemID != 61 || route.HalfOpens[61] == 0 {
		t.Fatalf("应占用成员 61 等待业务确认, 实际 ProbeItemID=%d HalfOpens=%v", route.ProbeItemID, route.HalfOpens)
	}
	if probes.Load() != 0 {
		t.Fatalf("关闭请求探测后不应发起合成探测, 实际 %d 次", probes.Load())
	}

	recordRouteSuccess(group, 61)
	route = routes[group.ID]
	if route.ProbeItemID != 0 || len(route.HalfOpens) != 0 {
		t.Fatalf("业务成功后应释放探测占用, 实际 ProbeItemID=%d HalfOpens=%v", route.ProbeItemID, route.HalfOpens)
	}
}

// TestPickGroupItemRequestProbeDefaultDisabledSkipsExpiredWhenClosedExists 验证默认关闭时有 CLOSED 成员不后台探测到期成员。
func TestPickGroupItemRequestProbeDefaultDisabledSkipsExpiredWhenClosedExists(t *testing.T) {
	stubRelayEnv(t)
	config := backoffConfig(10, 2, 30)
	config.RequestProbeEnabled = false
	group := testFailoverGroup(7, config, 71, 72)
	now := time.Now().UnixMilli()
	routes[group.ID] = &RouteState{
		GroupID:   group.ID,
		Cooldowns: map[int]int64{72: now - 1000},
		Levels:    make(map[int]int),
		HalfOpens: make(map[int]int64),
	}

	var probes atomic.Int32
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		probes.Add(1)
		return nil
	}

	item := pickGroupItem(group, 0)
	if item.ID != 71 {
		t.Fatalf("应优先返回 CLOSED 成员 71, 实际 %d", item.ID)
	}
	route := routes[group.ID]
	if probes.Load() != 0 {
		t.Fatalf("关闭请求探测后不应探测到期成员, 实际 %d 次", probes.Load())
	}
	if len(route.HalfOpens) != 0 || route.ProbeItemID != 0 {
		t.Fatalf("不应占用到期成员, 实际 ProbeItemID=%d HalfOpens=%v", route.ProbeItemID, route.HalfOpens)
	}
	if _, cooling := route.Cooldowns[72]; !cooling {
		t.Fatal("到期成员应保留冷却记录等待业务请求")
	}
}

// TestCooldownMillisBackoffAndCap 直接校验退避公式的倍乘与上限截断。
func TestCooldownMillisBackoffAndCap(t *testing.T) {
	cases := []struct {
		name   string
		config model.GroupRelayConfig
		level  int
		want   int64
	}{
		{"等级 0 取基础值", backoffConfig(10, 2, 30), 0, 10_000},
		{"等级 1 倍乘", backoffConfig(10, 2, 30), 1, 20_000},
		{"等级 3 封顶", backoffConfig(10, 2, 30), 3, 30_000},
		{"倍数小于 1 视为不退避", backoffConfig(10, 0, 30), 2, 10_000},
		{"上限未配置时不封顶", backoffConfig(10, 2, 0), 2, 40_000},
	}
	for _, testCase := range cases {
		if got := cooldownMillis(testCase.config, testCase.level); got != testCase.want {
			t.Fatalf("%s: cooldownMillis = %dms, 期望 %dms", testCase.name, got, testCase.want)
		}
	}
}
