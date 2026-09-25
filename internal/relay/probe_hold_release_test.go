package relay

// 提交后失败路径的占用归还回归测试:
// 半开恢复候选(ProbeItemID)胜出的请求在流已提交给客户端后失败时,
// 定稿路径必须与兄弟路径(stopRequested / RPM 等待取消 / 400 清洗重试)一样
// releaseRefChainHops 归还候选占用与紧急并发额度。缺失时 ProbeItemID 永久滞留,
// pickGroupItem 从此对该分组返回空——整组选路钉死, 仅删成员/删组/重启可恢复。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// TestPostCommitClientGoneReleasesProbeHold 场景: 唯一成员冷却到期 -> 整批切 HALF_OPEN
// 并行探测(探测桩恒成功) -> 胜出者置 ProbeItemID 作为业务二次确认候选 -> 业务流已向客户端
// 写入内容后模拟客户端断开(clientGone, 与提交后连击豁免同一分支) -> 断言候选占用已归还。
func TestPostCommitClientGoneReleasesProbeHold(t *testing.T) {
	setupFailoverTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-hold","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-hold","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-hold","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`)
		writeUpstreamSSE(t, w, "", "[DONE]")
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, hardeningName("it-hold-ch"), model.ChannelProviderOpenAI, upstream.URL, "it-model-hold")
	config := hardeningGroupConfig()
	config.MemberMaxAttempts = 1
	group := createIntegrationGroup(t, hardeningName("it-hardening-probe-hold"), config,
		integrationLeafItem(t, channel, "it-model-hold"))
	itemID := itemIDByModelName(t, group, "it-model-hold")

	// 探测桩恒成功: 唯一成员冷却到期后, 选路走整批 HALF_OPEN 并行探测, 胜出即置 ProbeItemID。
	// stubRelayEnv 在 setupFailoverTest 内保存了真实探测实现并在测试结束时恢复, 此处可安全替换。
	probeChannelFunc = func(ctx context.Context, channel model.Channel, modelName string) error {
		return nil
	}

	now := time.Now().UnixMilli()
	routeMu.Lock()
	routes[group.ID] = &RouteState{
		GroupID:           group.ID,
		Cooldowns:         map[int]int64{itemID: now - 1000},
		Levels:            map[int]int{},
		HalfOpens:         map[int]int64{},
		PostCommitStrikes: map[int]int{},
		emergencyCounts:   map[int]int{},
		emergencyBlocks:   map[int]int64{},
	}
	routeMu.Unlock()

	// 客户端在第 2 次写之后断开: 已提交的流以 clientGone 豁免定稿(不计提交后连击),
	// 与 TestForwardClientGoneExemptFromStrikes 驱动的是同一收尾分支。
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

	// 核心断言: 候选占用必须归还。修复缺失时 ProbeItemID 滞留为胜出成员 ID。
	snapshot := routeSnapshot(t, group.ID)
	if snapshot.ProbeItemID != 0 {
		t.Fatalf("提交后失败必须归还探测候选占用: ProbeItemID=%d 滞留, pickGroupItem 将永久返回空, 整组钉死", snapshot.ProbeItemID)
	}
	if len(snapshot.HalfOpens) != 0 {
		t.Fatalf("提交后失败必须清半开标记, 残留 %v", snapshot.HalfOpens)
	}
	if snapshot.EmergencyActive != 0 {
		t.Fatalf("提交后失败必须归还紧急兜底并发额度, 仍占 %d", snapshot.EmergencyActive)
	}
}
