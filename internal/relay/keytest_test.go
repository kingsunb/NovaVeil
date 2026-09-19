package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// TestChannelKeyTestCandidatesOrdering 验证逐密钥测试候选的构造:
// 多 Key 渠道按配置顺序全量纳入并跳过明文为空的残缺条目; 旧式单 Key 渠道回退单个无 ID 候选。
func TestChannelKeyTestCandidatesOrdering(t *testing.T) {
	channel := model.Channel{
		Key: "legacy",
		Keys: []model.ChannelKey{
			{ID: "id-a", Key: "k1", Remark: "r1"},
			{ID: "id-b", Key: "", Remark: "残缺条目"},
			{ID: "id-c", Key: "k3"},
		},
	}
	candidates := channelKeyTestCandidates(channel)
	if len(candidates) != 2 || candidates[0].ID != "id-a" || candidates[1].ID != "id-c" {
		t.Fatalf("候选应按配置顺序跳过空明文条目, 实际: %+v", candidates)
	}
	legacy := channelKeyTestCandidates(model.Channel{Key: "legacy-key"})
	if len(legacy) != 1 || legacy[0].Key != "legacy-key" || legacy[0].ID != "" {
		t.Fatalf("旧式单 Key 应回退为单个无 ID 候选, 实际: %+v", legacy)
	}
}

// TestEffectiveTestChannelExplicitKey 验证 TestChannel 的密钥解析:
// 指定 ID 时强制用该把密钥且不被冷却替换; 未找到时返回中文错误; 留空时回退探测路径跳过冷却 Key。
func TestEffectiveTestChannelExplicitKey(t *testing.T) {
	resetChannelKeyHealth()
	t.Cleanup(resetChannelKeyHealth)
	// 探测路径对已持久化的停用渠道立即拒绝（硬化 zer0sanity 见 relay.keyselect.go）,
	// 因此测试用 channel 必须显式启用, 否则探测路径在冷却前就被 errChannelDisabled 阻断。
	channel := model.Channel{
		ID:      91,
		Key:     "legacy",
		Enabled: true,
		Keys:    []model.ChannelKey{{ID: "id-a", Key: "secret-a"}, {ID: "id-b", Key: "secret-b"}},
	}
	effective, _, selected, err := effectiveTestChannel(channel, "id-b")
	if err != nil || selected.ID != "id-b" || effective.Key != "secret-b" || effective.Keys != nil {
		t.Fatalf("指定密钥应精确生效: selected=%+v effective.Key=%q err=%v", selected, effective.Key, err)
	}
	if _, _, _, err := effectiveTestChannel(channel, "missing"); err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("未找到密钥应返回中文错误, 实际: %v", err)
	}

	// id-a 冷却后, 留空走探测路径应跳到 id-b。
	markChannelKeyCooldown(channel.ID, "id-a", 60)
	if _, _, selected, err := effectiveTestChannel(channel, ""); err != nil || selected.ID != "id-b" {
		t.Fatalf("留空应跳过冷却中的 id-a, 实际: selected=%+v err=%v", selected, err)
	}
	// 显式指定冷却中的 Key 不被替换: 管理端逐 Key 验证需要真实结果而非静默换 Key。
	if _, _, selected, err := effectiveTestChannel(channel, "id-a"); err != nil || selected.ID != "id-a" {
		t.Fatalf("显式指定不应受冷却影响, 实际: selected=%+v err=%v", selected, err)
	}
}

// newKeyTestUpstream 构造假上游: Authorization 携带 good-key 的请求返回一条完整 Chat 补全,
// 其余请求按 401 拒绝, 供逐密钥测试按密钥有效性区分结果。
func newKeyTestUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("Authorization"), "good-key") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-keytest","object":"chat.completion","created":1,"model":"it-keytest","choices":[{"index":0,"message":{"role":"assistant","content":"走路"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
			return
		}
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
}

// TestChannelKeysPerKeyResults 验证逐密钥测试端到端: 每把密钥独立发起上游请求,
// 好密钥成功返回回复摘要, 坏密钥记录失败原因, 顺序与配置一致且互不影响。
func TestChannelKeysPerKeyResults(t *testing.T) {
	setupFailoverTest(t)
	upstream := newKeyTestUpstream(t)
	defer upstream.Close()

	channel := model.Channel{
		Name:    integrationUniqueName("it-keytest"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys: []model.ChannelKey{
			{Key: "good-key", Remark: "主用"},
			{Key: "bad-key", Remark: "备用"},
		},
		Models: []model.ChannelModel{{Name: "it-keytest-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	results, err := TestChannelKeys(context.Background(), channel.ID, "it-keytest-model", "走路还是坐车?")
	if err != nil {
		t.Fatalf("TestChannelKeys: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("应返回 2 条逐密钥结果, 实际 %d: %+v", len(results), results)
	}
	if !results[0].OK || results[0].Content != "走路" || results[0].Label != "#1(主用)" || results[0].KeyID == "" {
		t.Fatalf("第一把密钥应成功且补齐 ID: %+v", results[0])
	}
	if results[1].OK || results[1].Error == "" || results[1].Label != "#2(备用)" || results[1].Content != "" {
		t.Fatalf("第二把密钥应失败并记录原因: %+v", results[1])
	}
}

// TestChannelKeysLegacySingleKey 验证旧式单 Key 渠道: 无 ID 候选同样可测, 标签为空由前端兜底。
func TestChannelKeysLegacySingleKey(t *testing.T) {
	setupFailoverTest(t)
	upstream := newKeyTestUpstream(t)
	defer upstream.Close()

	channel := model.Channel{
		Name:    integrationUniqueName("it-keytest-legacy"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Key:     "legacy-good-key",
		Models:  []model.ChannelModel{{Name: "it-keytest-legacy-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	results, err := TestChannelKeys(context.Background(), channel.ID, "it-keytest-legacy-model", "")
	if err != nil {
		t.Fatalf("TestChannelKeys: %v", err)
	}
	if len(results) != 1 || !results[0].OK || results[0].Content != "走路" || results[0].KeyID != "" || results[0].Label != "" {
		t.Fatalf("旧式单 Key 应返回单条成功结果: %+v", results)
	}
}

// TestTestChannelExplicitKeyEndToEnd 验证单模型测试指定密钥: 上游收到的认证头来自所选密钥。
func TestTestChannelExplicitKeyEndToEnd(t *testing.T) {
	setupFailoverTest(t)
	upstream := newKeyTestUpstream(t)
	defer upstream.Close()

	channel := model.Channel{
		Name:    integrationUniqueName("it-keytest-single"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys: []model.ChannelKey{
			{Key: "bad-key", Remark: "已失效"},
			{Key: "good-key", Remark: "可用"},
		},
		Models: []model.ChannelModel{{Name: "it-keytest-single-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	stored, err := op.ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}

	// 默认路径固定第一把健康 Key, 命中已失效密钥, 测试应失败。
	if _, err := TestChannel(context.Background(), channel.ID, "it-keytest-single-model", "ping", ""); err == nil {
		t.Fatalf("默认第一把密钥已失效, 测试应失败")
	}
	// 显式指定第二把密钥, 应成功返回回复。
	result, err := TestChannel(context.Background(), channel.ID, "it-keytest-single-model", "ping", stored.Keys[1].ID)
	if err != nil {
		t.Fatalf("指定可用密钥应成功: %v", err)
	}
	if result.Content != "走路" || result.Model != "it-keytest-single-model" || result.ElapsedMS > time.Minute.Milliseconds() {
		t.Fatalf("结果摘要不符合预期: %+v", result)
	}
	// 未保存的密钥 ID 应返回中文错误。
	if _, err := TestChannel(context.Background(), channel.ID, "it-keytest-single-model", "ping", "no-such-id"); err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("未知密钥应返回中文错误, 实际: %v", err)
	}
}

// TestTestChannelRecordsToStream 验证单模型测试会作为终态请求进入日志流:
// 成功/失败各一条, 客户端标记为面板测试, 尝试轨迹/密钥标签/用量齐全;
// 且探针不计入客户端调用统计(不污染 client_stats)。
func TestTestChannelRecordsToStream(t *testing.T) {
	setupFailoverTest(t)
	upstream := newKeyTestUpstream(t)
	defer upstream.Close()

	channel := model.Channel{
		Name:    integrationUniqueName("it-teststream"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys: []model.ChannelKey{
			{Key: "bad-key", Remark: "已失效"},
			{Key: "good-key", Remark: "可用"},
		},
		Models: []model.ChannelModel{{Name: "it-teststream-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	stored, err := op.ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}

	// 成功探针: 显式指定可用密钥; 失败探针: 指定失效密钥。
	if _, err := TestChannel(context.Background(), channel.ID, "it-teststream-model", "ping", stored.Keys[1].ID); err != nil {
		t.Fatalf("可用密钥应成功: %v", err)
	}
	if _, err := TestChannel(context.Background(), channel.ID, "it-teststream-model", "ping", stored.Keys[0].ID); err == nil {
		t.Fatalf("失效密钥应失败")
	}

	snapshot, stream := OpenRequestStream()
	CloseRequestStream(stream)
	var successSeen, failedSeen bool
	for _, req := range snapshot {
		if req.ClientIP != testProbeClientIP || req.TargetChannel != channel.Name {
			continue
		}
		if req.TargetModel != "it-teststream-model" || len(req.Attempts) != 1 {
			t.Fatalf("探针应带单条尝试轨迹: %+v", req)
		}
		switch req.Status {
		case StatusSuccess:
			successSeen = true
			if req.Attempts[0].Outcome != AttemptSuccess || req.KeyLabel != "#2(可用)" {
				t.Fatalf("成功探针的轨迹/密钥标签不符: %+v", req)
			}
			if req.Usage.PromptTokens != int64(2) || req.Usage.CompletionTokens != int64(1) {
				t.Fatalf("成功探针应记录上游用量: %+v", req.Usage)
			}
			if !strings.Contains(ResponseBody(req.ID), "走路") {
				t.Fatalf("追踪接口应能拉到探针响应体: %q", ResponseBody(req.ID))
			}
		case StatusFailed:
			failedSeen = true
			if req.Class == "" || req.Error == "" || req.Attempts[0].Outcome != AttemptFailed {
				t.Fatalf("失败探针应归类并记录原因: %+v", req)
			}
		}
	}
	if !successSeen || !failedSeen {
		t.Fatalf("日志流应同时可见成功与失败探针: success=%v failed=%v", successSeen, failedSeen)
	}

	// 失败探针应进「最近失败」摘要, 可按唯一模型名定位。
	found := false
	for _, summary := range FailureSummaries(50, "") {
		if summary.Model == "it-teststream-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("失败探针应落入失败摘要")
	}

	// 探针不走 newRequestState: 客户端统计里不应出现面板测试标记。
	for _, stat := range op.ClientStatList() {
		if stat.IP == testProbeClientIP {
			t.Fatalf("探针不应计入客户端调用统计: %+v", stat)
		}
	}
}

// TestSendKeyTestRequestPathConsistency 验证逐密钥探针的上游请求路径与单模型探针一致:
// 修复后逐密钥探针以 openai_chat 作为代表客户端协议, openai 渠道走透传 /v1/chat/completions,
// 与单模型探针(sendChannelTestRequest)路径相同。
func TestSendKeyTestRequestPathConsistency(t *testing.T) {
	setupFailoverTest(t)
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-keypath","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	channel := model.Channel{
		Name:    integrationUniqueName("it-keytest-path"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys:    []model.ChannelKey{{Key: "good-key", Remark: "主用"}},
		Models:  []model.ChannelModel{{Name: "it-keytest-path-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	results, err := TestChannelKeys(context.Background(), channel.ID, "it-keytest-path-model", "ping")
	if err != nil {
		t.Fatalf("TestChannelKeys: %v", err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("逐密钥测试应成功: %+v", results)
	}
	// 逐密钥探针路径应与单模型探针一致, 均走 /v1/chat/completions。
	if capturedPath != "/v1/chat/completions" {
		t.Fatalf("逐密钥探针路径应为 /v1/chat/completions(与单模型探针一致), 实际 %s", capturedPath)
	}
}
