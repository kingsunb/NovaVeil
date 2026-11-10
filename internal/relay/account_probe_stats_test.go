package relay

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// TestAccountProbeOnlyLogsNotUsage 验证账号体检只保留日志展示, 不写用量分桶。
func TestAccountProbeOnlyLogsNotUsage(t *testing.T) {
	setupFailoverTest(t)
	beforeID := idSeq.Load()
	defer cleanupProbeRequests(beforeID)

	channel := model.Channel{ID: 41, Name: "account-probe-stats", Type: model.ChannelProviderOpenAI}
	_, _, target := testRequestTarget(channel, "account-probe-stats-model", "")
	request := startTestRequest("account-probe-stats-model", []byte("{}"), target, nil)
	request.AccountProbe = true
	finishTestRequest(request, "{}", &llm.Usage{PromptTokens: 120, CompletionTokens: 30}, nil)

	var buckets int64
	if err := db.GetDB().Model(&model.UsageBucket{}).
		Where("model_name = ?", "account-probe-stats-model").
		Count(&buckets).Error; err != nil {
		t.Fatalf("读取用量分桶失败: %v", err)
	}
	if buckets != 0 {
		t.Fatalf("账号体检不应写入用量分桶, 实际 %d 条", buckets)
	}
	if request.Usage.PromptTokens != 120 || request.Usage.CompletionTokens != 30 {
		t.Fatalf("日志展示用量应保留, 实际 %+v", request.Usage)
	}
}
