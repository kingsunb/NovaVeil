package relay

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func probeChatOKBody() []byte {
	return []byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}}`)
}

// TestProbeOpencodeAccountsClassifiesAndSeedsPool 验证批量体检的三类结论与池回填:
// 健康→verified 入池优先复用; 403→24h 冷却; 其余非 2xx 归为失败不冷却。
func TestProbeOpencodeAccountsClassifiesAndSeedsPool(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if strings.HasSuffix(account, "bad") {
			return http.StatusForbidden, []byte(`{"error":{"message":"FreeTierError"}}`)
		}
		if strings.HasSuffix(account, "down") {
			return http.StatusBadGateway, []byte(`{"error":{"message":"upstream down"}}`)
		}
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)

	report, err := ProbeOpencodeAccounts(context.Background(), channel.ID,
		[]string{"user_good", "user_good", "user_bad", "user_down"}, 0, 4, 0)
	if err != nil {
		t.Fatalf("批量体检失败: %v", err)
	}
	if report.Summary.Total != 3 || report.Summary.Healthy != 1 ||
		report.Summary.Rejected != 1 || report.Summary.Failed != 1 {
		t.Fatalf("汇总异常: %+v", report.Summary)
	}
	byAccount := make(map[string]AccountProbeResult, len(report.Results))
	for _, result := range report.Results {
		byAccount[result.Account] = result
	}
	good, ok := byAccount["user_good"]
	if !ok || good.Outcome != ocProbeOutcomeHealthy || good.Status != http.StatusOK {
		t.Fatalf("user_good 应为 healthy/200: %+v", good)
	}
	bad := byAccount["user_bad"]
	if bad.Outcome != ocProbeOutcomeRejected || bad.Status != http.StatusForbidden {
		t.Fatalf("user_bad 应为 rejected/403: %+v", bad)
	}
	down := byAccount["user_down"]
	if down.Outcome != ocProbeOutcomeError || down.Status != http.StatusBadGateway {
		t.Fatalf("user_down 应为 error/502: %+v", down)
	}

	snapshot := opencodeProxyPoolSnapshot(t, channel.ID)
	goodState, ok := snapshot["user_good"]
	if !ok || !goodState.verified || !goodState.cooldownUntil.IsZero() {
		t.Fatalf("user_good 应 verified 且无冷却: %+v", goodState)
	}
	badState, ok := snapshot["user_bad"]
	if !ok || !time.Now().Before(badState.cooldownUntil) {
		t.Fatalf("user_bad 应进入冷却: %+v", badState)
	}
	if _, exists := snapshot["user_down"]; exists {
		t.Fatalf("user_down 属基础设施失败, 不应入池: %+v", snapshot["user_down"])
	}
}

// TestProbeOpencodeAccountsGenerateAndValidate 验证随机生成模式与入参校验。
func TestProbeOpencodeAccountsGenerateAndValidate(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)

	report, err := ProbeOpencodeAccounts(context.Background(), channel.ID, nil, 3, 3, 0)
	if err != nil {
		t.Fatalf("生成模式体检失败: %v", err)
	}
	if report.Summary.Total != 3 || report.Summary.Healthy != 3 {
		t.Fatalf("生成 3 个账号应全部健康: %+v", report.Summary)
	}
	pattern := regexp.MustCompile(`^opencode[0-9a-f]{8}$`)
	seen := make(map[string]bool, 3)
	for _, result := range report.Results {
		if !pattern.MatchString(result.Account) {
			t.Fatalf("生成账号格式异常: %q", result.Account)
		}
		if seen[result.Account] {
			t.Fatalf("生成账号重复: %q", result.Account)
		}
		seen[result.Account] = true
	}

	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID, []string{"bad account!"}, 0, 0, 0); err == nil {
		t.Fatal("含空格账号应被拒绝")
	}
	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID, make([]string, ocProbeMaxAccounts+1), 0, 0, 0); err == nil {
		t.Fatal("超过单次上限应被拒绝")
	}

	bare := createIntegrationChannel(t, "account-probe-no-proxy", model.ChannelProviderOpenAI, "http://opencode.test/v1", "plain-model")
	if _, err := ProbeOpencodeAccounts(context.Background(), bare.ID, nil, 0, 0, 0); err == nil {
		t.Fatal("非 {account} 代理渠道应被拒绝")
	}
}
