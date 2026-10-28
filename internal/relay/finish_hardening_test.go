package relay

// 发布前核心加固批次回归测试, 覆盖五类场景:
//  1. cloneRouteState 对 PostCommitStrikes 的深克隆: 并发连击写入与锁外序列化不得构成数据竞争;
//  2. 每请求全局尝试轮次上限与整体安全截止时间: 超限即以客户端协议错误收尾, 不再无限循环;
//  3. 上游流提前 EOF(errStreamEarlyEof): 同成员首享一次免记账立即重试, 第二次才正常计失败;
//  4. 转发收尾 clientGone 豁免: 客户端断开不记提交后连击也不计渠道失败, token 计量保留;
//  5. 合法空输出终态保险丝: 截断终态下 output==0 放行交付, 其余组合照旧拦截。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

// hardeningGroupConfig 返回加固测试通用的故障转移分组配置。
func hardeningGroupConfig() model.GroupRelayConfig {
	return model.GroupRelayConfig{
		MemberMaxAttempts:                     1,
		MemberRetryIntervalSeconds:            1,
		MemberNonStreamResponseTimeoutSeconds: 5,
		MemberStreamFirstEventTimeoutSeconds:  5,
		MemberCooldownSeconds:                 60,
		CooldownBackoffMultiplier:             2,
		CooldownMaxSeconds:                    120,
	}
}

// hardeningNameSeq 为本文件的渠道/分组名生成进程内唯一后缀:
// 集成库按进程仅初始化一次且跨 go test -count 重复执行复用,
// 静态资源名会在第二次运行时触发唯一约束冲突, 故全部动态命名。
var hardeningNameSeq atomic.Int64

func hardeningName(base string) string {
	return fmt.Sprintf("%s-%d", base, hardeningNameSeq.Add(1))
}

// TestCloneRouteStatePostCommitStrikesConcurrentSafe 是 P0 并发回归:
// 连击写入(锁内)与 SSE 消费者锁外 json.Marshal 迭代共享 map 时, 缺失克隆会直接
// "concurrent map iteration and map write" fatal。修复后 cloneRouteState 必须对
// PostCommitStrikes 深克隆, 本用例高频并发两侧操作验证无竞争且快照真正独立。
func TestCloneRouteStatePostCommitStrikesConcurrentSafe(t *testing.T) {
	stubRelayEnv(t)
	group := model.Group{ID: 9201, Name: "hardening-clone-race", Mode: model.GroupModeFailover}
	group.RelayConfig = model.DefaultGroupRelayConfig()
	group.Items = []model.GroupItem{
		{ID: 71, GroupID: group.ID, ChannelModel: &model.ChannelModel{ChannelID: 1, Name: "m-a"}},
		{ID: 72, GroupID: group.ID, ChannelModel: &model.ChannelModel{ChannelID: 2, Name: "m-b"}},
		{ID: 73, GroupID: group.ID, ChannelModel: &model.ChannelModel{ChannelID: 3, Name: "m-c"}},
	}

	routeMu.Lock()
	routes[group.ID] = &RouteState{
		GroupID:           group.ID,
		Cooldowns:         make(map[int]int64),
		Levels:            make(map[int]int),
		HalfOpens:         make(map[int]int64),
		PostCommitStrikes: make(map[int]int),
		emergencyCounts:   make(map[int]int),
		emergencyBlocks:   make(map[int]int64),
	}
	live := routes[group.ID]
	stream := make(chan RouteState, 4096)
	routeStreams[stream] = struct{}{}
	routeMu.Unlock()
	t.Cleanup(func() {
		routeMu.Lock()
		defer routeMu.Unlock()
		if _, ok := routeStreams[stream]; ok {
			delete(routeStreams, stream)
			close(stream)
		}
	})

	const ops = 1500
	var wg sync.WaitGroup

	// 模拟路由 SSE 消费者: 锁外序列化发布的快照。缺失克隆时该迭代与写入方共享底层 map。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for message := range stream {
			data, err := json.Marshal(message)
			if err != nil {
				t.Errorf("序列化发布消息失败: %v", err)
				return
			}
			if !strings.Contains(string(data), "post_commit_strikes") {
				t.Error("发布消息应包含 post_commit_strikes 字段")
				return
			}
		}
	}()

	// 模拟多个请求并发上报提交后失败(锁内写 map 并触发发布)。
	for w := range 4 {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				recordPostCommitFailure(group, 71+(i+offset)%3)
			}
		}(w)
	}

	// 模拟前端全量快照读取路径: 锁内克隆, 锁外序列化。
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				routeMu.Lock()
				snapshot := cloneRouteState(live)
				routeMu.Unlock()
				if _, err := json.Marshal(snapshot); err != nil {
					t.Errorf("序列化克隆快照失败: %v", err)
					return
				}
			}
		}()
	}

	// 全部写入与克隆结束后注销并关闭路由流, 让消费者 goroutine 随之退出。
	routeMu.Lock()
	if _, ok := routeStreams[stream]; ok {
		delete(routeStreams, stream)
		close(stream)
	}
	routeMu.Unlock()
	wg.Wait()

	// 克隆必须是深拷贝: 改动活状态的连击表不得穿透到既有快照。
	routeMu.Lock()
	snapshot := cloneRouteState(live)
	live.PostCommitStrikes[99999] = 7
	routeMu.Unlock()
	if _, shared := snapshot.PostCommitStrikes[99999]; shared {
		t.Fatal("克隆后的 PostCommitStrikes 与活状态共享底层 map")
	}
}

// TestGlobalRequestCapsDefaultsAndHelpers 校验新配置字段的默认值、归一化钳制与辅助判定函数。
func TestGlobalRequestCapsDefaultsAndHelpers(t *testing.T) {
	defaults := model.DefaultGroupRelayConfig()
	if defaults.MaxRequestRounds != 600 || defaults.MaxRequestSeconds != 0 {
		t.Fatalf("默认配置应为 rounds=600 seconds=0(不限时), 实际 %d/%d", defaults.MaxRequestRounds, defaults.MaxRequestSeconds)
	}

	var zero model.GroupRelayConfig
	model.NormalizeGroupRelayConfig(&zero)
	if zero.MaxRequestRounds != 600 || zero.MaxRequestSeconds != 0 {
		t.Fatalf("零值配置应补默认值, 实际 %d/%d", zero.MaxRequestRounds, zero.MaxRequestSeconds)
	}

	negative := model.GroupRelayConfig{MaxRequestRounds: -3, MaxRequestSeconds: -1}
	model.NormalizeGroupRelayConfig(&negative)
	if negative.MaxRequestRounds != 600 || negative.MaxRequestSeconds != 0 {
		t.Fatalf("负值应被钳回默认值(0=不限时), 实际 %d/%d", negative.MaxRequestRounds, negative.MaxRequestSeconds)
	}

	// 显式 0 表示不限时, 归一化不得覆盖; 轮次上限仍补默认。
	explicitUnlimited := model.GroupRelayConfig{MemberMaxAttempts: 2, MaxRequestSeconds: 0}
	model.NormalizeGroupRelayConfig(&explicitUnlimited)
	if explicitUnlimited.MaxRequestSeconds != 0 || explicitUnlimited.MaxRequestRounds != 600 {
		t.Fatalf("显式 0 上限应保持不限时且轮次补默认, 实际 %d/%d", explicitUnlimited.MaxRequestRounds, explicitUnlimited.MaxRequestSeconds)
	}

	if got := maxRequestRounds(model.GroupRelayConfig{}); got != 600 {
		t.Fatalf("配置缺失时轮次上限应回退默认 600, 实际 %d", got)
	}
	if got := maxRequestRounds(model.GroupRelayConfig{MaxRequestRounds: 7}); got != 7 {
		t.Fatalf("合法配置应原样生效, 实际 %d", got)
	}

	stale := time.Now().Add(-2 * time.Second)
	fresh := time.Now()
	if requestDeadlineExceeded(model.GroupRelayConfig{}, stale) {
		t.Fatal("MaxRequestSeconds=0 表示不限时, 不应判超限")
	}
	if !requestDeadlineExceeded(model.GroupRelayConfig{MaxRequestSeconds: 1}, stale) {
		t.Fatal("超过 1 秒上限应判超限")
	}
	if requestDeadlineExceeded(model.GroupRelayConfig{MaxRequestSeconds: 1}, fresh) {
		t.Fatal("未达上限不应判超限")
	}
	if !requestDeadlineExceeded(model.GroupRelayConfig{MaxRequestSeconds: -1}, time.Now().Add(-700*time.Second)) {
		t.Fatal("负值按非法配置回退默认上限(0=不限时)后不应判超限")
	}
}

// TestReadStreamWindowEarlyEofSentinel 验证提前 EOF 哨兵:
// 流在任何内容信号与协议终止事件之前耗尽(含空流)时返回包装 errStreamEarlyEof 的错误,
// 不再把空窗口当作可交付响应; 终态分类同步归入 early_eof。
func TestReadStreamWindowEarlyEofSentinel(t *testing.T) {
	structuralOnly := &fakeStream{events: []*httpclient.StreamEvent{
		sseEvent(`{"choices":[{"delta":{"role":"assistant"}}]}`),
	}}
	window, ended, terminated, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), structuralOnly)
	if !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("结构帧后提前 EOF 应返回 errStreamEarlyEof, 得到: %v", err)
	}
	if window != nil || ended || terminated {
		t.Fatal("错误路径下不得交付窗口事件且 ended/terminated 必须为 false")
	}

	empty := &fakeStream{}
	if _, _, _, err := readStreamWindow(context.Background(), llm.APIFormatOpenAIChatCompletion, openai.NewInboundTransformer(), empty); !errors.Is(err, errStreamEarlyEof) {
		t.Fatalf("空流应同样判提前 EOF, 得到: %v", err)
	}

	wrapped := fmt.Errorf("%w: tail frame", errStreamEarlyEof)
	if ClassifyError(wrapped) != ErrClassEarlyEof {
		t.Fatal("ClassifyError 应把提前 EOF 归入 early_eof 分类")
	}
	if classifyRound(wrapped, context.Background(), context.Background()) != ErrClassEarlyEof {
		t.Fatal("classifyRound 应把提前 EOF 归入 early_eof 分类")
	}
}

// TestForwardStopsWhenRequestRoundsExhausted 验证全局尝试轮次上限:
// 两个成员各失败一次即耗尽 MaxRequestRounds=2, 第三轮循环在选路前以客户端协议错误
// 格式收尾, 不再触碰任何上游。
func TestForwardStopsWhenRequestRoundsExhausted(t *testing.T) {
	setupFailoverTest(t)

	var aHits, bHits atomic.Int64
	broken := func(hits *atomic.Int64) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream broken"}}`))
		}
	}
	a := httptest.NewServer(broken(&aHits))
	defer a.Close()
	b := httptest.NewServer(broken(&bHits))
	defer b.Close()

	aChannel := createIntegrationChannel(t, hardeningName("it-round-a"), model.ChannelProviderOpenAI, a.URL, "it-model-round-a")
	bChannel := createIntegrationChannel(t, hardeningName("it-round-b"), model.ChannelProviderOpenAI, b.URL, "it-model-round-b")
	config := hardeningGroupConfig()
	config.MaxRequestRounds = 2
	group := createIntegrationGroup(t, hardeningName("it-hardening-round-cap"), config,
		integrationLeafItem(t, aChannel, "it-model-round-a"),
		integrationLeafItem(t, bChannel, "it-model-round-b"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("轮次超限应以 400 收尾, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 下游错误消息已统一: 全部成员不可用时只返回"暂无可用渠道", 超限原因仅保留在内部终态。
	if !strings.Contains(recorder.Body.String(), "暂无可用渠道") {
		t.Fatalf("客户端错误体应为统一文案\"暂无可用渠道\", 实际: %s", recorder.Body.String())
	}
	if got := aHits.Load(); got != 1 {
		t.Fatalf("成员 A 应恰好尝试一次, 实际 %d 次", got)
	}
	if got := bHits.Load(); got != 1 {
		t.Fatalf("成员 B 应恰好尝试一次, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("请求应以失败终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
	if !strings.Contains(state.Error, "请求尝试轮次超限") {
		t.Fatalf("终态错误应携带超限原因, 实际 %q", state.Error)
	}
	if len(state.Attempts) != 2 {
		t.Fatalf("尝试轨迹应只有两轮, 实际 %d 条", len(state.Attempts))
	}
}

// TestForwardStopsWhenOverallDeadlineExceeded 验证整体安全截止时间:
// 单成员持续失败且每次等待重试, MaxRequestSeconds=1 到点后在下一轮循环入口拒绝继续,
// 不再发起新的上游调用并以客户端协议错误收尾。
func TestForwardStopsWhenOverallDeadlineExceeded(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"slow broken upstream"}}`))
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-deadline-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-deadline")
	config := hardeningGroupConfig()
	config.MemberMaxAttempts = 5 // 未达阈值走"等待后重试同一成员", 让截止时间成为唯一出口。
	config.MaxRequestRounds = 50
	config.MaxRequestSeconds = 1
	group := createIntegrationGroup(t, hardeningName("it-hardening-deadline"), config,
		integrationLeafItem(t, channel, "it-model-deadline"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("总时长超限应以 400 收尾, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 下游错误消息已统一: 全部成员不可用时只返回"暂无可用渠道", 超时原因仅保留在内部终态。
	if !strings.Contains(recorder.Body.String(), "暂无可用渠道") {
		t.Fatalf("客户端错误体应为统一文案\"暂无可用渠道\", 实际: %s", recorder.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("截止到点后不得再次触碰上游, 实际尝试 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed || !strings.Contains(state.Error, "请求总时长超限") {
		t.Fatalf("终态应为总时长超限失败, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestForwardEarlyEofGetsOneFreeRetryThenCounts 验证提前 EOF 免费重试:
// 上游每次都在产出内容前断开。第一次提前 EOF 对同成员免记账立即重试;
// 第二次起正常计入渠道失败并累计连败, 达阈值进入冷却。
func TestForwardEarlyEofGetsOneFreeRetryThenCounts(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 置 SSE 头后不写任何帧直接返回: 客户端在收到任何事件前即 EOF。
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-eof-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-eof")
	config := hardeningGroupConfig()
	config.MemberMaxAttempts = 2
	group := createIntegrationGroup(t, hardeningName("it-hardening-early-eof"), config,
		integrationLeafItem(t, channel, "it-model-eof"))
	itemID := itemIDByModelName(t, group, "it-model-eof")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()

	// 时间线: r1 提前 EOF → 免费重试(hit2); r2 计失败#1 → 等 1 秒; r3 计失败#2 → 冷却+排除;
	// r4 无可选成员进入等待, 1.8 秒处被下游取消。免费重试缺失时 1.8 秒内只会命中上游两次。
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()
	postRelayJSON(t, engine, path, body, "", ctx)

	if got := hits.Load(); got != 3 {
		t.Fatalf("应命中上游 3 次(免费重试 + 两次记账失败), 实际 %d 次", got)
	}
	snapshot := routeSnapshot(t, group.ID)
	if deadline, cooling := snapshot.Cooldowns[itemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("第二次提前 EOF 应让成员进入冷却, 得到 %d/%v", deadline, cooling)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusCanceled {
		t.Fatalf("请求最终以下游断开定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// failingWriter 在放行指定次数的写入后模拟客户端断开, 用于驱动转发期 clientGone 分支。
type failingWriter struct {
	gin.ResponseWriter
	failAfter int
	writes    int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.failAfter {
		return 0, errors.New("simulated client gone")
	}
	return w.ResponseWriter.Write(p)
}

// TestForwardClientGoneExemptFromStrikes 验证 clientGone 豁免:
// 已提交后客户端断开(写失败)时, 成员不计提交后失败连击(即使达到阈值也不冷却)。
func TestForwardClientGoneExemptFromStrikes(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-gone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-gone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial-answer"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-gone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-gone-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-gone")
	config := hardeningGroupConfig()
	config.MemberMaxAttempts = 1 // 任一连击即达阈值: 若未豁免会立刻冷却, 断言即可区分。
	group := createIntegrationGroup(t, hardeningName("it-hardening-client-gone"), config,
		integrationLeafItem(t, channel, "it-model-gone"))
	itemID := itemIDByModelName(t, group, "it-model-gone")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Writer = &failingWriter{ResponseWriter: c.Writer, failAfter: 2}
		c.Next()
	})
	engine.POST("/v1/chat/completions", Forward(llm.APIFormatOpenAIChatCompletion))
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	expectedID := nextRequestID()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("写失败轮次应以失败终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
	if state.Usage.CompletionTokens != 7 {
		t.Fatalf("已产生的 token 计量应保留, 实际: %+v", state.Usage)
	}
	snapshot := routeSnapshot(t, group.ID)
	if _, cooling := snapshot.Cooldowns[itemID]; cooling {
		t.Fatal("clientGone 豁免下成员不得因提交后连击进入冷却")
	}
	if strikes := snapshot.PostCommitStrikes[itemID]; strikes != 0 {
		t.Fatalf("clientGone 轮次不应累积提交后连击, 实际 %d", strikes)
	}
}

// TestLegalEmptyTerminalDeliveredIllegalStillBlocked 验证合法空终止保险丝:
// finish_reason=length 且明确 output==0 的连通性 ping 类响应放行交付;
// stop 终态下的 output==0 维持无效判定, 照常换轮重试直至冷却, 不交付脏数据。
func TestLegalEmptyTerminalDeliveredIllegalStillBlocked(t *testing.T) {
	setupFailoverTest(t)

	var lengthHits, stopHits atomic.Int64
	length := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lengthHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-maxlen","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`))
	}))
	defer length.Close()
	stop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stopHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-stopzero","object":"chat.completion","created":1700000000,"model":"integration","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`))
	}))
	defer stop.Close()

	lengthChannel := createIntegrationChannel(t, hardeningName("it-empty-len-ch"), model.ChannelProviderOpenAI, length.URL, "it-model-empty-len")
	stopChannel := createIntegrationChannel(t, hardeningName("it-empty-stop-ch"), model.ChannelProviderOpenAI, stop.URL, "it-model-empty-stop")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	requestBody := func(name string) string {
		return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, name)
	}

	// 合法空终止: 放行交付, 不触发任何重试。
	deliver := createIntegrationGroup(t, hardeningName("it-hardening-empty-length"), hardeningGroupConfig(),
		integrationLeafItem(t, lengthChannel, "it-model-empty-len"))
	deliverID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, requestBody(deliver.Name), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("length+output0 应放行交付, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("交付正文应保留上游终态, 实际: %s", recorder.Body.String())
	}
	if got := lengthHits.Load(); got != 1 {
		t.Fatalf("合法空终止不应重试, 实际尝试 %d 次", got)
	}
	deliverState := requestStateOf(t, deliverID)
	if deliverState.Status != StatusSuccess {
		t.Fatalf("合法空终止应以成功终态定稿, 实际 %s(%s)", deliverState.Status, deliverState.Error)
	}

	// 非法组合(stop + output==0): 整轮判无效重试, 达阈值进入冷却, 不向客户端交付脏数据。
	illegal := createIntegrationGroup(t, hardeningName("it-hardening-empty-stop"), hardeningGroupConfig(),
		integrationLeafItem(t, stopChannel, "it-model-empty-stop"))
	itemID := itemIDByModelName(t, illegal, "it-model-empty-stop")
	illegalID := nextRequestID()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	postRelayJSON(t, engine, path, requestBody(illegal.Name), "", ctx)

	if got := stopHits.Load(); got != 1 {
		t.Fatalf("stop+output0 首轮即判无效, 实际尝试 %d 次", got)
	}
	snapshot := routeSnapshot(t, illegal.ID)
	if deadline, cooling := snapshot.Cooldowns[itemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("stop+output0 达阈值后成员应进入冷却, 得到 %d/%v", deadline, cooling)
	}
	failedState := requestStateOf(t, illegalID)
	if failedState.Status == StatusSuccess {
		t.Fatal("stop+output0 的脏响应不得被判成功交付")
	}
}
