package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestNormalizeDeveloperRoles 验证 developer 角色归一化: 首条→system, 中途→user。
func TestNormalizeDeveloperRoles(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"developer","content":"instructions"},
		{"role":"user","content":"q1"},
		{"role":"assistant","content":"a1"},
		{"role":"developer","content":"mid instructions"},
		{"role":"user","content":"q2"}
	]}`)
	out := normalizeChatRoles(body)
	roles := gjson.GetBytes(out, "messages.#.role").Array()
	if len(roles) != 5 {
		t.Fatalf("消息数量不应变化, 实际 %d", len(roles))
	}
	want := []string{"system", "user", "assistant", "user", "user"}
	for i, r := range roles {
		if r.String() != want[i] {
			t.Fatalf("第 %d 条角色应为 %s, 实际 %s", i, want[i], r.String())
		}
	}
	if gjson.GetBytes(out, "messages.0.content").String() != "instructions" {
		t.Fatal("归一化不得改动消息内容")
	}
}

// TestNormalizeMidSystemFolded 会话中途的 system 消息折叠进首条 system 并移至最前:
// Claude Code 类客户端会在 messages 中段注入 role=system 的轮次指令, 上游仅接受前导 system。
func TestNormalizeMidSystemFolded(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"q1"},
		{"role":"system","content":"S2"},
		{"role":"assistant","content":"a1"},
		{"role":"system","content":[{"type":"text","text":"S3"}]},
		{"role":"user","content":"q2"}
	]}`)
	out := normalizeChatRoles(body)
	roles := gjson.GetBytes(out, "messages.#.role").Array()
	want := []string{"system", "user", "assistant", "user"}
	if len(roles) != len(want) {
		t.Fatalf("折叠后消息数应为 %d, 实际 %d: %s", len(want), len(roles), out)
	}
	for i, r := range roles {
		if r.String() != want[i] {
			t.Fatalf("第 %d 条角色应为 %s, 实际 %s", i, want[i], r.String())
		}
	}
	sysText := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(sysText, "S2") || !strings.Contains(sysText, "S3") {
		t.Fatalf("首条 system 应包含被折叠的两段文本, 实际: %s", sysText)
	}
}

// TestNonRetryable400FailsFastAcrossMembers 复现 Codex developer 角色被上游拒绝的场景:
// 两个成员都返回确定性 400 时, 请求应在每个成员各试一次后立即终止并把明确错误交还下游,
// 而不是进入冷却-等待-探测循环烧掉轮次预算(重试间隔配 5 秒放大旧行为的空等代价)。
func TestNonRetryable400FailsFastAcrossMembers(t *testing.T) {
	setupFailoverTest(t)

	badBody := `{"error":{"message":"[1214] Incorrect role information","type":"server_error"}}`
	var hitsA, hitsB atomic.Int64
	newBadUpstream := func(counter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(badBody))
		}))
	}
	serverA := newBadUpstream(&hitsA)
	defer serverA.Close()
	serverB := newBadUpstream(&hitsB)
	defer serverB.Close()

	config := refSkipConfig()
	chA := createIntegrationChannel(t, "it-400-a-ch", model.ChannelProviderOpenAI, serverA.URL, "it-400-model-a")
	chB := createIntegrationChannel(t, "it-400-b-ch", model.ChannelProviderOpenAI, serverB.URL, "it-400-model-b")
	group := createIntegrationGroup(t, "it-400-g", config,
		integrationLeafItem(t, chA, "it-400-model-a"),
		integrationLeafItem(t, chB, "it-400-model-b"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}],"stream":false}`, group.Name)

	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	if elapsed >= 8*time.Second {
		t.Fatalf("确定性 400 应快速终止, 实际耗时 %v(疑似冷却-等待循环)", elapsed)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("应把上游 400 交还下游, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 下游错误消息已统一: 全部成员不可用时只返回"暂无可用渠道", 上游详情仅保留在内部状态。
	if !strings.Contains(recorder.Body.String(), "暂无可用渠道") {
		t.Fatalf("下游应收到统一文案\"暂无可用渠道\", 实际: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Incorrect role information") {
		t.Fatalf("上游错误详情不得泄漏给下游: %s", recorder.Body.String())
	}
	if hitsA.Load() != 2 || hitsB.Load() != 2 {
		t.Fatalf("两成员应各被打两次(原始+清洗重试), 实际 A=%d B=%d", hitsA.Load(), hitsB.Load())
	}

	// 归一化回归: 同样的 developer 角色, 上游正常时应被转换后成功交付。
	var okHits atomic.Int64
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		raw, _ := readBody(r)
		if !gjson.GetBytes(raw, "messages.0.role").Exists() ||
			gjson.GetBytes(raw, "messages.#.role").Array()[0].String() == "developer" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	defer okServer.Close()
	chC := createIntegrationChannel(t, "it-400-ok-ch", model.ChannelProviderOpenAI, okServer.URL, "it-400-model-c")
	groupOK := createIntegrationGroup(t, "it-400-ok-g", config,
		integrationLeafItem(t, chC, "it-400-model-c"))
	// 跨协议场景(与真实 Codex 一致): Responses 入站带 developer 角色, 转换为 Chat 出站时归一化。
	engineOK, pathOK := newIntegrationEngine(llm.APIFormatOpenAIResponse)
	bodyOK := fmt.Sprintf(`{"model":%q,"input":[{"role":"developer","content":[{"type":"input_text","text":"be nice"}]},{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`, groupOK.Name)
	recorderOK := postRelayJSON(t, engineOK, pathOK, bodyOK, "", nil)
	if recorderOK.Code != http.StatusOK || okHits.Load() != 1 {
		t.Fatalf("developer 角色应被归一化后成功交付, 实际 %d hits=%d: %s", recorderOK.Code, okHits.Load(), recorderOK.Body.String())
	}
}

// readBody 读取并返回请求体字节。
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 1<<16)
	n, err := r.Body.Read(buf)
	return buf[:n], err
}

// context 引用保持(测试文件内未直接使用的导入占位)。
var _ = context.Background

// TestNormalizeChatContent 锁定 null/缺失 content 补为空串, 非空与数组不变。
func TestNormalizeChatContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":null},{"role":"assistant","content":"hi"}]}`)
	out := normalizeChatContent(body)
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "" {
		t.Fatalf("null content 应补为空串: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "hi" {
		t.Fatalf("非 null content 不应改变: %s", out)
	}
	body2 := []byte(`{"messages":[{"role":"user"}]}`)
	out2 := normalizeChatContent(body2)
	if got := gjson.GetBytes(out2, "messages.0.content").String(); got != "" {
		t.Fatalf("缺失 content 应补为空串: %s", out2)
	}
	body3 := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out3 := normalizeChatContent(body3)
	if !gjson.GetBytes(out3, "messages.0.content").IsArray() {
		t.Fatalf("数组 content 不应改变: %s", out3)
	}
}

// TestMergeAssistantTurns 锁定交错文本与工具调用合并为同一回合。
func TestMergeAssistantTurns(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"let me look"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","content":"done","tool_call_id":"c1"}
	]}`)
	out := mergeAssistantTurns(body)
	rows := gjson.GetBytes(out, "messages").Array()
	if len(rows) != 3 {
		t.Fatalf("应合并为 3 条消息, 实际 %d", len(rows))
	}
	if got := rows[1].Get("content").String(); got != "let me look" {
		t.Fatalf("文本应保留: %q", got)
	}
	if len(rows[1].Get("tool_calls").Array()) != 1 {
		t.Fatal("工具调用应并入同一回合")
	}
}

// TestMergeAssistantTurnsKeepsLegacyFunctionCall 合并时保留旧版 function_call。
func TestMergeAssistantTurnsKeepsLegacyFunctionCall(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"calling"},
		{"role":"assistant","function_call":{"name":"read","arguments":"{}"}}
	]}`)
	out := mergeAssistantTurns(body)
	rows := gjson.GetBytes(out, "messages").Array()
	if len(rows) != 2 {
		t.Fatalf("应合并为 2 条, 实际 %d", len(rows))
	}
	if got := rows[1].Get("function_call.name").String(); got != "read" {
		t.Fatalf("function_call 应保留: %s", rows[1].Raw)
	}
}
