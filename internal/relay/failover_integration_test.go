package relay

// 自动故障转移端到端集成测试: 通过 Forward gin 入口 + httptest 假上游驱动完整转发循环。
// 数据层使用一次性临时 SQLite 文件(纯 Go 驱动, 已是直接依赖), 渠道与分组经 op 层真实落库并进入缓存;
// 路由全局状态与注入点由既有 stubRelayEnv/resetStickyState 托管清理。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

var (
	integrationOnce sync.Once
	integrationErr  error
	// integrationNameSeq 集成资源命名序号: 同进程 -count=N 复跑时数据库只初始化一次,
	// 固定名会撞 groups.name/channels.name 唯一索引, 每次创建追加递增后缀保证唯一。
	integrationNameSeq atomic.Int64
)

// integrationUniqueName 为集成测试资源名追加进程内唯一后缀;
// 分组引用与请求体一律经由创建函数返回值取名, 因此既有断言不受影响。
func integrationUniqueName(name string) string {
	return fmt.Sprintf("%s-it%d", name, integrationNameSeq.Add(1))
}

// setupFailoverTest 准备一次集成测试: 首次调用时初始化临时 SQLite 数据库,
// 之后每条用例清空路由与会话粘合全局状态, 并把渠道查询指回 op 缓存供半开探测使用。
func setupFailoverTest(t *testing.T) {
	t.Helper()
	integrationOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nv-relay-integration-")
		if err != nil {
			integrationErr = err
			return
		}
		integrationErr = db.InitDB("sqlite", filepath.Join(dir, "integration.db"), false)
	})
	if integrationErr != nil {
		t.Fatalf("初始化集成数据库失败: %v", integrationErr)
	}
	stubRelayEnv(t)
	resetStickyState()
	channelLookupFunc = op.ChannelGet
}

// createIntegrationChannel 创建指向假上游的真实渠道记录并写入缓存。
// 模型以渠道模型行(channel_models)形式挂到渠道上, 返回的渠道携带回填后的模型 ID。
func createIntegrationChannel(t *testing.T, name string, provider model.ChannelProvider, baseURL, modelName string) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name:    integrationUniqueName(name),
		Type:    provider,
		Enabled: true,
		BaseURL: baseURL,
		Key:     "integration-key",
		Models:  []model.ChannelModel{{Name: modelName, Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道 %s 失败: %v", name, err)
	}
	return channel
}

// integrationChannelModelID 返回渠道上指定名称渠道模型的主键, 供分组成员引用。
func integrationChannelModelID(t *testing.T, channel model.Channel, modelName string) int {
	t.Helper()
	for _, channelModel := range channel.Models {
		if channelModel.Name == modelName {
			return channelModel.ID
		}
	}
	t.Fatalf("渠道 %s 不含渠道模型 %s", channel.Name, modelName)
	return 0
}

// integrationLeafItem 构造指向渠道模型的叶子成员。
func integrationLeafItem(t *testing.T, channel model.Channel, modelName string) model.GroupItem {
	t.Helper()
	return model.GroupItem{ChannelModelID: integrationChannelModelID(t, channel, modelName)}
}

// integrationRefItem 构造指向目标分组名的引用成员。
func integrationRefItem(targetGroupName string) model.GroupItem {
	return model.GroupItem{RefGroupName: targetGroupName}
}

// createIntegrationGroup 创建故障转移分组, 成员按传入顺序获得优先级 0..n-1。
func createIntegrationGroup(t *testing.T, name string, config model.GroupRelayConfig, items ...model.GroupItem) model.Group {
	t.Helper()
	for i := range items {
		items[i].Priority = i
	}
	model.NormalizeGroupRelayConfig(&config)
	group := model.Group{Name: integrationUniqueName(name), Mode: model.GroupModeFailover, RelayConfig: config, Items: items}
	if err := op.GroupCreate(&group, context.Background()); err != nil {
		t.Fatalf("创建分组 %s 失败: %v", name, err)
	}
	return group
}

// itemIDByModelName 返回分组内指定模型名叶子成员的 ID, 引用成员按被引用分组名匹配。
func itemIDByModelName(t *testing.T, group model.Group, modelName string) int {
	t.Helper()
	snapshot, err := op.GroupGetByName(group.Name)
	if err != nil {
		t.Fatalf("读取分组 %s 快照失败: %v", group.Name, err)
	}
	for _, item := range snapshot.Items {
		if item.ChannelModel != nil && item.ChannelModel.Name == modelName {
			return item.ID
		}
		if item.IsGroupRef() && item.RefGroupName == modelName {
			return item.ID
		}
	}
	t.Fatalf("分组 %s 不含成员 %s", group.Name, modelName)
	return 0
}

// newIntegrationEngine 构造只挂载 Forward 的最小 gin 引擎, 返回引擎与对应路径。
func newIntegrationEngine(format llm.APIFormat) (*gin.Engine, string) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	switch format {
	case llm.APIFormatAnthropicMessage:
		engine.POST("/v1/messages", Forward(format))
		return engine, "/v1/messages"
	default:
		engine.POST("/v1/chat/completions", Forward(format))
		return engine, "/v1/chat/completions"
	}
}

// postRelayJSON 以可取消上下文向转发入口发起一次请求并等待处理结束。
func postRelayJSON(t *testing.T, engine *gin.Engine, path, body, session string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Session-Id", session)
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// chatCompletionBody 构造带明确用量的完整 OpenAI Chat 非流式响应。
func chatCompletionBody(id, content string, prompt, completion int64) []byte {
	body := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "integration",
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"total_tokens":      prompt + completion,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return data
}

// writeUpstreamSSE 向流式响应写出一个 SSE 帧, event 为空时省略事件行。
func writeUpstreamSSE(t *testing.T, w http.ResponseWriter, event, data string) {
	t.Helper()
	if event != "" {
		if _, err := fmt.Fprintf(w, "event:%s\n", event); err != nil {
			t.Errorf("写出 SSE 事件行失败: %v", err)
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		t.Errorf("写出 SSE 数据帧失败: %v", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// sseFrame 是客户端收到的一个 SSE 帧。
type sseFrame struct {
	event string
	data  string
}

// frameType 解析帧正文中的协议事件类型。
func (f sseFrame) frameType() string {
	var parsed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(f.data), &parsed); err != nil {
		return ""
	}
	return parsed.Type
}

// parseSSEFrames 按空行切块解析客户端收到的完整帧序列。
func parseSSEFrames(t *testing.T, body []byte) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, block := range strings.Split(string(body), "\n\n") {
		var frame sseFrame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				frame.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				frame.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if frame.data != "" || frame.event != "" {
			frames = append(frames, frame)
		}
	}
	return frames
}

// routeSnapshot 在锁内克隆指定分组的路由状态快照。
func routeSnapshot(t *testing.T, groupID int) RouteState {
	t.Helper()
	routeMu.Lock()
	defer routeMu.Unlock()
	route := routes[groupID]
	if route == nil {
		t.Fatalf("分组 %d 尚无路由状态", groupID)
	}
	return cloneRouteState(route)
}

// stickyTargetOf 在锁内读取指定会话当前粘合的成员 ID。
func stickyTargetOf(t *testing.T, groupID int, session string) (int, bool) {
	t.Helper()
	routeMu.Lock()
	defer routeMu.Unlock()
	entry, ok := sessionStickies[groupID][sessionScopeKey(0, session)]
	if !ok {
		return 0, false
	}
	return entry.ItemID, true
}

// requestStateOf 返回指定 ID 的请求进程内状态。
func requestStateOf(t *testing.T, id uint64) RequestState {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	request, ok := requests[id]
	if !ok {
		t.Fatalf("请求状态 %d 不存在", id)
	}
	return *request
}

// nextRequestID 预告下一个将被分配的请求 ID。
func nextRequestID() uint64 {
	return idSeq.Load() + 1
}

// TestFailoverPrioritySwitchToHealthyMember 验证优先级故障转移:
// 高优成员按 MemberMaxAttempts 重试耗尽后进入冷却, 客户端请求由低优成员无感承载。
func TestFailoverPrioritySwitchToHealthyMember(t *testing.T) {
	setupFailoverTest(t)

	var highHits, lowHits atomic.Int64
	high := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		highHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"high priority upstream broken"}}`))
	}))
	defer high.Close()
	low := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lowHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-low-ok", "ok-from-low-priority", 5, 7))
	}))
	defer low.Close()

	highChannel := createIntegrationChannel(t, "it-failover-high", model.ChannelProviderOpenAI, high.URL, "it-model-high")
	lowChannel := createIntegrationChannel(t, "it-failover-low", model.ChannelProviderOpenAI, low.URL, "it-model-low")
	group := createIntegrationGroup(t, "it-failover-basic",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			MemberAffinitySeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, highChannel, "it-model-high"),
		integrationLeafItem(t, lowChannel, "it-model-low"))
	highItemID := itemIDByModelName(t, group, "it-model-high")
	lowItemID := itemIDByModelName(t, group, "it-model-low")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到成功响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ok-from-low-priority") {
		t.Fatalf("响应应由低优成员承载, 实际: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "broken") {
		t.Fatal("高优成员的错误不应透传给客户端")
	}
	if got := highHits.Load(); got != 2 {
		t.Fatalf("高优成员应重试至 MemberMaxAttempts=2 次, 实际 %d 次", got)
	}
	if got := lowHits.Load(); got != 1 {
		t.Fatalf("低优成员应恰好承载一次, 实际 %d 次", got)
	}

	snapshot := routeSnapshot(t, group.ID)
	if deadline, cooling := snapshot.Cooldowns[highItemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("高优成员应进入未来冷却, 得到 %d/%v", deadline, cooling)
	}
	if _, cooling := snapshot.Cooldowns[lowItemID]; cooling {
		t.Fatal("低优成员不应进入冷却")
	}
	if snapshot.CurrentItemID != lowItemID {
		t.Fatalf("当前路由应指向低优成员 %d, 实际 %d", lowItemID, snapshot.CurrentItemID)
	}
}

// TestZeroOutputStreamFailoverToNextMember 验证流式零输出切换:
// 高优成员返回 200 流但聚合用量明确 output==0 时整轮判无效并切换,
// 低优成员的正常流完整交付, 全程客户端无感知。
func TestZeroOutputStreamFailoverToNextMember(t *testing.T) {
	setupFailoverTest(t)

	var zeroHits, okHits atomic.Int64
	zero := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		zeroHits.Add(1)
		if !req.Stream {
			_, _ = w.Write(chatCompletionBody("chatcmpl-zero-a", "", 3, 0))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-zero-a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-zero-a","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer zero.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-ok-b","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-ok-b","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello-from-low"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-ok-b","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer ok.Close()

	zeroChannel := createIntegrationChannel(t, "it-zero-high", model.ChannelProviderOpenAI, zero.URL, "it-model-zero")
	okChannel := createIntegrationChannel(t, "it-zero-low", model.ChannelProviderOpenAI, ok.URL, "it-model-ok")
	group := createIntegrationGroup(t, "it-failover-zero-output",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			MemberAffinitySeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, zeroChannel, "it-model-zero"),
		integrationLeafItem(t, okChannel, "it-model-ok"))
	zeroItemID := itemIDByModelName(t, group, "it-model-zero")
	okItemID := itemIDByModelName(t, group, "it-model-ok")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("零输出切换后客户端仍应收到 200, 实际 %d", recorder.Code)
	}
	contentType := recorder.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("响应应为 SSE 流, 实际 Content-Type %q", contentType)
	}
	responseBody := recorder.Body.String()
	if !strings.Contains(responseBody, "chatcmpl-ok-b") || !strings.Contains(responseBody, "hello-from-low") {
		t.Fatalf("客户端应收到低优成员内容, 实际: %s", responseBody)
	}
	if strings.Contains(responseBody, "chatcmpl-zero-a") {
		t.Fatal("零输出成员的任何帧都不应交付客户端")
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) == 0 || frames[len(frames)-1].data != "[DONE]" {
		t.Fatalf("流应以 [DONE] 规范收尾, 实际末帧: %+v", frames[max(len(frames)-1, 0)])
	}

	if got := zeroHits.Load(); got != 1 {
		t.Fatalf("零输出成员应在首轮后被放弃, 实际尝试 %d 次", got)
	}
	if got := okHits.Load(); got != 1 {
		t.Fatalf("健康成员应恰好承载一次, 实际 %d 次", got)
	}
	snapshot := routeSnapshot(t, group.ID)
	if deadline, cooling := snapshot.Cooldowns[zeroItemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("零输出成员应进入冷却, 得到 %d/%v", deadline, cooling)
	}
	if snapshot.CurrentItemID != okItemID {
		t.Fatalf("当前路由应指向健康成员 %d, 实际 %d", okItemID, snapshot.CurrentItemID)
	}
}

// TestZeroOutputAllBadEndsWithClientCancel 验证唯一成员持续零输出时请求循环重试不交付:
// 用带超时的上下文模拟下游断开, 请求以取消终态结束且从未向客户端写出任何内容帧。
func TestZeroOutputAllBadEndsWithClientCancel(t *testing.T) {
	setupFailoverTest(t)

	var zeroHits atomic.Int64
	zero := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		zeroHits.Add(1)
		if !req.Stream {
			// 半开探测走非流式合成请求, 同样返回明确零输出。
			_, _ = w.Write(chatCompletionBody("chatcmpl-zero-only", "", 2, 0))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-zero-only","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-zero-only","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer zero.Close()

	channel := createIntegrationChannel(t, "it-zero-only", model.ChannelProviderOpenAI, zero.URL, "it-model-zero-only")
	group := createIntegrationGroup(t, "it-failover-zero-all-bad",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 1,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    8,
		},
		integrationLeafItem(t, channel, "it-model-zero-only"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)

	expectedID := nextRequestID()
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", ctx)
	elapsed := time.Since(begin)

	state := requestStateOf(t, expectedID)
	if state.Status != StatusCanceled {
		t.Fatalf("请求应以取消终态结束, 实际 %s(%s)", state.Status, state.Error)
	}
	if !strings.Contains(state.Error, "deadline exceeded") && !strings.Contains(state.Error, "context canceled") {
		t.Fatalf("取消原因应来自下游超时, 实际 %q", state.Error)
	}
	if elapsed < 2200*time.Millisecond {
		t.Fatalf("转发循环应持续重试直至下游断开, 实际仅耗时 %v", elapsed)
	}
	if got := zeroHits.Load(); got < 2 {
		t.Fatalf("重试期间应多次尝试上游, 实际仅 %d 次", got)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("全程不得向客户端写出内容帧, 实际写出: %s", recorder.Body.String())
	}
}

// TestMidStreamUpstreamDisconnectSilentTruncation 验证流中断静默截断:
// 上游发出 message_start + content_block_delta 后干净关闭连接(EOF 无错误、无任何协议终止帧),
// 截断的答案不得伪装成正常完成: 提交后的流失败不补发任何终止帧(错误帧/正常终止帧均不发),
// SSE 流以缺失 message_stop 结束, Codex/opencode 等客户端按「流缺终止事件即断开」
// 自动重连重试整个请求; 请求仍以失败终态记账并累计成员连击(见 errStreamEarlyClose)。
func TestMidStreamUpstreamDisconnectSilentTruncation(t *testing.T) {
	setupFailoverTest(t)

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_up_a","type":"message","role":"assistant","content":[],"model":"it-anthropic-a","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
		writeUpstreamSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
		// 直接断开连接, 不发送 message_stop 等任何协议终止帧。
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-anthropic-broken", model.ChannelProviderAnthropic, upstream.URL, "it-model-anthropic")
	group := createIntegrationGroup(t, "it-failover-mid-stream",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-model-anthropic"))

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := upstreamHits.Load(); got != 1 {
		t.Fatalf("上游应被请求一次, 实际 %d 次", got)
	}

	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) != 2 {
		t.Fatalf("应恰好收到 2 帧(两个上游帧), 失败收尾不得补发任何终止帧, 实际 %d 帧: %+v", len(frames), frames)
	}
	wantTypes := []string{"message_start", "content_block_delta"}
	for i, want := range wantTypes {
		if got := frames[i].frameType(); got != want {
			t.Fatalf("第 %d 帧类型 = %q, 期望 %q (data: %s)", i+1, got, want, frames[i].data)
		}
	}
	for _, frame := range frames {
		if frame.frameType() == "error" || frame.data == "[DONE]" || strings.Contains(frame.data, "message_stop") {
			t.Fatalf("静默截断不得补发终止帧, 实际: %s", frame.data)
		}
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("干净提前关闭应以失败终态记账, 实际 %s(%s)", state.Status, state.Error)
	}
	if !strings.Contains(state.Error, "closed before the terminal event") {
		t.Fatalf("失败原因应来自提前关闭哨兵, 实际 %q", state.Error)
	}
}

// TestStreamFinishWithoutDoneSentinelDelivered 验证缺 [DONE] 哨兵的完整流:
// 上游发出内容块与 finish_reason=stop 终止块后直接干净关闭连接(不发 [DONE], 如 MiniMax),
// 终止块已声明生成完整结束, 不按提前关闭判失败: 客户端收到合成 [DONE] 规范收尾, 请求按成功定稿。
func TestStreamFinishWithoutDoneSentinelDelivered(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-nodone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-nodone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-nodone","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		// 直接关闭连接, 不发送 [DONE] 哨兵。
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-nodone", model.ChannelProviderOpenAI, upstream.URL, "it-model-nodone")
	group := createIntegrationGroup(t, "it-failover-nodone",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
		},
		integrationLeafItem(t, channel, "it-model-nodone"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "hello") {
		t.Fatalf("客户端应收到完整内容, 实际: %s", recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) == 0 || frames[len(frames)-1].data != "[DONE]" {
		t.Fatalf("缺哨兵的完整流应由合成 [DONE] 规范收尾, 实际末帧: %+v", frames[max(len(frames)-1, 0)])
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("上游应恰好承载一次, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("缺 [DONE] 的完整流应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestAnthropicFinishWithoutMessageStopDelivered 验证 Anthropic 协议缺 message_stop 哨兵的完整流:
// 上游发出内容块与 message_delta(stop_reason=end_turn) 后直接干净关闭连接(不发 message_stop,
// 部分 Anthropic 兼容第三方代理如此), message_delta 已声明生成完整结束, 不按提前关闭判失败:
// 客户端收到合成的 message_delta + message_stop 规范收尾, 请求按成功定稿。
// 修复前该场景被误判为 errStreamEarlyClose 静默截断, opencode/Codex 等客户端按「流缺终止事件
// 即断开」无限重试, 表现为用户感知的"断流"。
func TestAnthropicFinishWithoutMessageStopDelivered(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_nostop","type":"message","role":"assistant","content":[],"model":"it-m-nostop","stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}`)
		writeUpstreamSSE(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeUpstreamSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
		writeUpstreamSSE(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeUpstreamSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		// 直接关闭连接, 不发送 message_stop 哨兵。
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-nostop", model.ChannelProviderAnthropic, upstream.URL, "it-m-nostop")
	group := createIntegrationGroup(t, "it-failover-nostop",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
		},
		integrationLeafItem(t, channel, "it-m-nostop"))

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "hello") {
		t.Fatalf("客户端应收到完整内容, 实际: %s", recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	// 末帧应为合成的 message_stop, 而非静默截断(无终止帧)。
	if len(frames) == 0 {
		t.Fatal("应收到流式帧, 实际为空")
	}
	lastFrame := frames[len(frames)-1]
	if lastFrame.frameType() != "message_stop" {
		t.Fatalf("缺 message_stop 的完整流应由合成 message_stop 规范收尾, 实际末帧类型 %q: %s",
			lastFrame.frameType(), lastFrame.data)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("完整响应不应触发重试, 上游应恰好承载一次, 实际 %d 次", got)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("缺 message_stop 的完整流应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestAnthropicBodyToOpenAIChatEndpoint 验证协议自动检测:
// 客户端将原生 Anthropic Messages 格式请求发往 /v1/chat/completions 端点时,
// NovaVeil 检测到 Anthropic 格式标记(顶层 system 字段、input_schema 工具定义)
// 并切换为 Anthropic 入站转换器, 避免 OpenAI Chat 解析器静默丢弃 system 和工具定义。
// 修复前该场景产出空响应({"id":"","choices":null,...}), 修复后上游收到完整 Anthropic 请求,
// 客户端收到正确格式的 Anthropic 响应。
func TestAnthropicBodyToOpenAIChatEndpoint(t *testing.T) {
	setupFailoverTest(t)

	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_detect","type":"message","role":"assistant","content":[{"type":"text","text":"hello back"}],"model":"it-m-detect","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-anthropic-detect", model.ChannelProviderAnthropic, upstream.URL, "it-m-detect")
	group := createIntegrationGroup(t, "it-failover-detect",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
		},
		integrationLeafItem(t, channel, "it-m-detect"))

	// 关键: 使用 OpenAI Chat 端点 (/v1/chat/completions) 发送 Anthropic 格式请求体。
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":256,"system":[{"type":"text","text":"You are a helpful assistant."}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"tools":[{"name":"calc","description":"A calculator","input_schema":{"type":"object","properties":{"expr":{"type":"string"}}}}],"stream":false}`, group.Name)

	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}

	// 响应应为 Anthropic 格式(type:"message"), 而非 OpenAI Chat 格式(object:"chat.completion")。
	respType := gjson.GetBytes(recorder.Body.Bytes(), "type").String()
	if respType != "message" {
		t.Fatalf("响应应为 Anthropic 格式(type=message), 实际 type=%q, body: %s", respType, recorder.Body.String())
	}
	content := gjson.GetBytes(recorder.Body.Bytes(), "content.0.text").String()
	if content != "hello back" {
		t.Fatalf("响应内容应为 'hello back', 实际: %s", recorder.Body.String())
	}

	// 上游应收到完整的 Anthropic 请求: 顶层 system 字段和 input_schema 工具定义都应保留。
	if !strings.Contains(upstreamBody, `"system"`) {
		t.Fatalf("上游请求应包含 system 字段, 实际: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"input_schema"`) {
		t.Fatalf("上游请求应包含 input_schema 工具定义, 实际: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "You are a helpful assistant") {
		t.Fatalf("上游请求应包含 system 文本, 实际: %s", upstreamBody)
	}
}

// TestAnthropicBodyToOpenAIChatEndpointStream 验证协议自动检测的流式场景:
// ZCode 等客户端以 Anthropic 格式发送流式请求到 /v1/chat/completions 端点,
// NovaVeil 应检测到 Anthropic 格式并以 Anthropic SSE 事件流返回(而非 OpenAI Chat 流)。
func TestAnthropicBodyToOpenAIChatEndpointStream(t *testing.T) {
	setupFailoverTest(t)

	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_detect_stream","type":"message","role":"assistant","content":[],"model":"it-m-detect-s","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}`)
		writeUpstreamSSE(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeUpstreamSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stream hello"}}`)
		writeUpstreamSSE(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeUpstreamSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		writeUpstreamSSE(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-anthropic-detect-s", model.ChannelProviderAnthropic, upstream.URL, "it-m-detect-s")
	group := createIntegrationGroup(t, "it-failover-detect-s",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
		},
		integrationLeafItem(t, channel, "it-m-detect-s"))

	// 关键: 使用 OpenAI Chat 端点发送 Anthropic 格式流式请求。
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":256,"system":"You are helpful.","messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)

	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("客户端应收到 200 流式响应, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 客户端应收到 Anthropic SSE 事件(message_start/content_block_delta), 而非 OpenAI Chat 流。
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) == 0 {
		t.Fatal("应收到流式帧, 实际为空")
	}
	firstType := frames[0].frameType()
	if firstType != "message_start" {
		t.Fatalf("首帧应为 Anthropic message_start, 实际 %q: %s", firstType, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "stream hello") {
		t.Fatalf("客户端应收到完整内容, 实际: %s", recorder.Body.String())
	}
	// 上游应收到 Anthropic 格式请求(system 字段保留)。
	if !strings.Contains(upstreamBody, `"system"`) {
		t.Fatalf("上游请求应包含 system 字段, 实际: %s", upstreamBody)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("流式请求应以成功终态定稿, 实际 %s(%s)", state.Status, state.Error)
	}
}

// TestStickyCooldownClearsSwitchesMember 验证会话粘合随冷却清除:
// 粘合成员进入冷却后, 同 X-Session-Id 的下一个请求自动切换到其他成员并重建粘合。
func TestStickyCooldownClearsSwitchesMember(t *testing.T) {
	setupFailoverTest(t)

	var highHits, lowHits atomic.Int64
	var highBroken atomic.Bool
	high := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		highHits.Add(1)
		if highBroken.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"sticky member down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-sticky-a", "served-by-high", 1, 3))
	}))
	defer high.Close()
	low := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lowHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-sticky-b", "served-by-low", 1, 3))
	}))
	defer low.Close()

	highChannel := createIntegrationChannel(t, "it-sticky-high", model.ChannelProviderOpenAI, high.URL, "it-model-sticky-high")
	lowChannel := createIntegrationChannel(t, "it-sticky-low", model.ChannelProviderOpenAI, low.URL, "it-model-sticky-low")
	group := createIntegrationGroup(t, "it-failover-sticky",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			MemberAffinitySeconds:                 0,
			SessionStickyEnabled:                  true,
			SessionStickySeconds:                  300,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, highChannel, "it-model-sticky-high"),
		integrationLeafItem(t, lowChannel, "it-model-sticky-low"))
	highItemID := itemIDByModelName(t, group, "it-model-sticky-high")
	lowItemID := itemIDByModelName(t, group, "it-model-sticky-low")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	session := "sess-cool-it"

	first := postRelayJSON(t, engine, path, body, session, nil)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "served-by-high") {
		t.Fatalf("首个请求应由高优成员承载, 实际 %d: %s", first.Code, first.Body.String())
	}
	if target, ok := stickyTargetOf(t, group.ID, session); !ok || target != highItemID {
		t.Fatalf("首个请求后粘合应指向高优成员 %d, 得到 %d/%v", highItemID, target, ok)
	}

	// 高优成员故障, 同会话的下一个请求必须自动切到低优成员。
	highBroken.Store(true)
	beforeLowHits := lowHits.Load()
	second := postRelayJSON(t, engine, path, body, session, nil)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "served-by-low") {
		t.Fatalf("粘合成员进入冷却后应自动切换到低优成员, 实际 %d: %s", second.Code, second.Body.String())
	}
	snapshot := routeSnapshot(t, group.ID)
	if deadline, cooling := snapshot.Cooldowns[highItemID]; !cooling || deadline <= time.Now().UnixMilli() {
		t.Fatalf("高优成员应进入冷却, 得到 %d/%v", deadline, cooling)
	}
	if target, ok := stickyTargetOf(t, group.ID, session); !ok || target != lowItemID {
		t.Fatalf("切换成功后粘合应重建到低优成员 %d, 得到 %d/%v", lowItemID, target, ok)
	}

	// 后续同会话请求直接命中低优成员, 不再触碰冷却中的高优成员。
	afterSecondHigh := highHits.Load()
	third := postRelayJSON(t, engine, path, body, session, nil)
	if third.Code != http.StatusOK || !strings.Contains(third.Body.String(), "served-by-low") {
		t.Fatalf("第三个请求应继续由低优成员承载, 实际 %d: %s", third.Code, third.Body.String())
	}
	if highHits.Load() != afterSecondHigh {
		t.Fatal("冷却中的高优成员不应再收到任何请求")
	}
	if lowHits.Load() != beforeLowHits+2 {
		t.Fatalf("低优成员应承载两次请求, 实际 %d 次", lowHits.Load()-beforeLowHits)
	}
}

// TestHalfOpenRecoveryRestoresClosed 验证半开恢复:
// 全部成员冷却到期后, 下一个请求触发并行合成探测, 最先成功者承载业务请求并恢复 CLOSED。
func TestHalfOpenRecoveryRestoresClosed(t *testing.T) {
	setupFailoverTest(t)

	var aBroken, bBroken atomic.Bool
	var aHits, bHits atomic.Int64
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aHits.Add(1)
		if aBroken.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"a down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-halfopen-a", "recovered-a", 2, 4))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHits.Add(1)
		if bBroken.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"b down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-halfopen-b", "recovered-b", 2, 4))
	}))
	defer b.Close()

	aChannel := createIntegrationChannel(t, "it-recover-a", model.ChannelProviderOpenAI, a.URL, "it-model-recover-a")
	bChannel := createIntegrationChannel(t, "it-recover-b", model.ChannelProviderOpenAI, b.URL, "it-model-recover-b")
	group := createIntegrationGroup(t, "it-failover-half-open",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 1,
			MemberAffinitySeconds:                 0,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    30,
		},
		integrationLeafItem(t, aChannel, "it-model-recover-a"),
		integrationLeafItem(t, bChannel, "it-model-recover-b"))
	aItemID := itemIDByModelName(t, group, "it-model-recover-a")
	bItemID := itemIDByModelName(t, group, "it-model-recover-b")

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)

	// 第一阶段: 两个成员都真实失败进入冷却; 用短超时上下文在等待期主动断开。
	aBroken.Store(true)
	bBroken.Store(true)
	warmupCtx, cancelWarmup := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelWarmup()
	postRelayJSON(t, engine, path, body, "", warmupCtx)

	waitFor(t, 5*time.Second, func() bool {
		snapshot := routeSnapshot(t, group.ID)
		_, aCooling := snapshot.Cooldowns[aItemID]
		_, bCooling := snapshot.Cooldowns[bItemID]
		return aCooling && bCooling
	})
	// 第二阶段: 冷却到期前把上游修复为健康。
	waitFor(t, 5*time.Second, func() bool {
		snapshot := routeSnapshot(t, group.ID)
		now := time.Now().UnixMilli()
		return snapshot.Cooldowns[aItemID] <= now && snapshot.Cooldowns[bItemID] <= now
	})
	aBroken.Store(false)
	bBroken.Store(false)

	// 第三阶段: 恢复请求应触发并行探测并成功交付。
	recovered := postRelayJSON(t, engine, path, body, "", nil)
	if recovered.Code != http.StatusOK {
		t.Fatalf("恢复请求应成功, 实际 %d: %s", recovered.Code, recovered.Body.String())
	}
	responseBody := recovered.Body.String()
	var winnerID int
	switch {
	case strings.Contains(responseBody, "recovered-a"):
		winnerID = aItemID
	case strings.Contains(responseBody, "recovered-b"):
		winnerID = bItemID
	default:
		t.Fatalf("响应应由某个被探测恢复的成员承载, 实际: %s", responseBody)
	}

	snapshot := routeSnapshot(t, group.ID)
	if len(snapshot.HalfOpens) != 0 {
		t.Fatalf("恢复后不应有成员停留在 HALF_OPEN, 实际 %v", snapshot.HalfOpens)
	}
	if snapshot.ProbeItemID != 0 {
		t.Fatalf("候选经业务确认后应清零, 实际 %d", snapshot.ProbeItemID)
	}
	if _, cooling := snapshot.Cooldowns[winnerID]; cooling {
		t.Fatal("探测成功的成员应解除冷却")
	}
	if level := snapshot.Levels[winnerID]; level != 0 {
		t.Fatalf("恢复成员的冷却等级应清零, 实际 %d", level)
	}
	if snapshot.CurrentItemID != winnerID {
		t.Fatalf("当前路由应指向恢复成员 %d, 实际 %d", winnerID, snapshot.CurrentItemID)
	}
}

// TestTerminalEventClosesStreamWithoutUpstreamClose 复现上游 #349:
// 上游发出协议终态帧后不关闭响应体(部分网关如此), 转发必须立即收尾,
// 不能阻塞在 events.Next() 上等到客户端断开才返回——否则已完整交付的
// 响应会被迟到的 context.Canceled 误判为取消。
func TestTerminalEventClosesStreamWithoutUpstreamClose(t *testing.T) {
	setupFailoverTest(t)

	upstreamClosed := make(chan struct{})
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { close(upstreamClosed) }()
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_up_349","type":"message","role":"assistant","content":[],"model":"it-m-349","stop_reason":null,"usage":{"input_tokens":3,"output_tokens":2}}}`)
		writeUpstreamSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		writeUpstreamSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		writeUpstreamSSE(t, w, "message_stop", `{"type":"message_stop"}`)
		// 终态帧之后故意保持连接打开: 模拟发完终止事件却不关体的上游。
		time.Sleep(3 * time.Second)
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-349-ch", model.ChannelProviderAnthropic, upstream.URL, "it-m-349")
	group := createIntegrationGroup(t, "it-349-g",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-m-349"))

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	if recorder.Code != http.StatusOK {
		t.Fatalf("应收到 200 流式响应, 实际 %d", recorder.Code)
	}
	// message_stop 后应立即收尾返回, 不等待上游 3 秒挂断。
	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("终态到达后应及时结束转发, 实际耗时 %v(疑似阻塞等上游关体)", elapsed)
	}
	frames := parseSSEFrames(t, recorder.Body.Bytes())
	if len(frames) == 0 {
		t.Fatal("不应交付空流")
	}
	lastFrame := frames[len(frames)-1]
	if !strings.Contains(lastFrame.data, `"type":"message_stop"`) {
		t.Fatalf("最后一帧应为上游真实的 message_stop, 实际: %s", lastFrame.data)
	}
	// 完整交付的响应不得被误判为取消/失败: 无合成补发帧(上游终态本身就是最后帧)。
	for _, f := range frames {
		if strings.Contains(f.event, "error") {
			t.Fatalf("正常完成的流不应包含错误帧: %+v", f)
		}
	}
	select {
	case <-upstreamClosed:
	default:
		// 上游 handler 尚在 sleep 属预期; 不影响断言。
	}
}
