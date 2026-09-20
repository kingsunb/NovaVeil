package relay

import (
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// TestNewTestRequestMaxTokens 验证面板/评估两条路径使用不同的输出上限:
// 面板诊断默认 4096, 评估路径 100000(审计 REL-06)。
func TestNewTestRequestMaxTokens(t *testing.T) {
	panelReq, err := newTestRequest(llm.APIFormatOpenAIChatCompletion, "m", "hi", 0)
	if err != nil {
		t.Fatalf("panel request: %v", err)
	}
	if got := gjson.GetBytes(panelReq.Body, "max_tokens").Int(); got != testPanelMaxTokens {
		t.Fatalf("panel default max_tokens = %d, want %d", got, testPanelMaxTokens)
	}

	evalReq, err := newTestRequest(llm.APIFormatOpenAIChatCompletion, "m", "hi", testMaxTokens)
	if err != nil {
		t.Fatalf("eval request: %v", err)
	}
	if got := gjson.GetBytes(evalReq.Body, "max_tokens").Int(); got != testMaxTokens {
		t.Fatalf("eval max_tokens = %d, want %d", got, testMaxTokens)
	}

	newChatReq, err := newTestChatRequest("m", "hi")
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	if got := gjson.GetBytes(newChatReq.Body, "max_tokens").Int(); got != testPanelMaxTokens {
		t.Fatalf("chat request max_tokens = %d, want %d", got, testPanelMaxTokens)
	}
}
