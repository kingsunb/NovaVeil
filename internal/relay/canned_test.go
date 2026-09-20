package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

const cannedTestReply = "这是固定回复：你好"

// newCannedIntegrationGroup 建一个只含自定义固定回复渠道成员的分组, 返回分组与渠道。
func newCannedIntegrationGroup(t *testing.T) (model.Group, model.Channel) {
	t.Helper()
	channel := model.Channel{
		Name:       integrationUniqueName("it-canned"),
		Type:       model.ChannelProviderCustom,
		Enabled:    true,
		FixedReply: cannedTestReply,
		Models:     []model.ChannelModel{{Name: "it-canned-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建自定义渠道失败: %v", err)
	}
	group := createIntegrationGroup(t, "it-canned-group",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			MemberAffinitySeconds:                 0,
		},
		integrationLeafItem(t, channel, "it-canned-model"))
	return group, channel
}

// TestCannedFixedReplyNonStreamOpenAI 验证 OpenAI Chat 客户端非流式命中固定回复:
// 响应体为合法 chat.completion, 内容即渠道 FixedReply, 用量为正(零输出会被管线判无效换目标)。
func TestCannedFixedReplyNonStreamOpenAI(t *testing.T) {
	setupFailoverTest(t)
	group, _ := newCannedIntegrationGroup(t)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应应为合法 JSON: %v; body = %s", err, recorder.Body.String())
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content != cannedTestReply {
		t.Fatalf("应返回固定文案, 实际: %s", recorder.Body.String())
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason 应为 stop, 实际: %+v", parsed.Choices[0])
	}
	if parsed.Usage.CompletionTokens <= 0 {
		t.Fatalf("合成响应必须带正的 completion_tokens(否则管线判零输出换目标): %+v", parsed.Usage)
	}
}

// TestCannedFixedReplyStreamOpenAI 验证流式命中固定回复: SSE 携带内容增量块并以
// 协议终止块与 [DONE] 收尾, 内容即 FixedReply。
func TestCannedFixedReplyStreamOpenAI(t *testing.T) {
	setupFailoverTest(t)
	group, _ := newCannedIntegrationGroup(t)

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), cannedTestReply) {
		t.Fatalf("流式响应应包含固定文案: %s", recorder.Body.String())
	}
	// gin-contrib/sse 上游 v1.1.2 的 IO 格式为 "data:<payload>"（冒号后无空格），
	// 浏览器 EventSource 对冒号后可选空格兼容，此处锁定上游格式。
	if !strings.Contains(recorder.Body.String(), "data:[DONE]") {
		t.Fatalf("流式响应应以 [DONE] 收尾: %s", recorder.Body.String())
	}
}

// TestCannedFixedReplyAnthropicClient 验证跨协议: Anthropic 客户端命中同一渠道,
// 合成响应经管线转换为 Anthropic Message 格式且内容不变。
func TestCannedFixedReplyAnthropicClient(t *testing.T) {
	setupFailoverTest(t)
	group, _ := newCannedIntegrationGroup(t)

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应应为合法 Anthropic Message JSON: %v; body = %s", err, recorder.Body.String())
	}
	text := ""
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	if text != cannedTestReply {
		t.Fatalf("跨协议转换后应保留固定文案, 实际: %s", recorder.Body.String())
	}
}
