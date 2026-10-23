package relay

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
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
	if downState, exists := snapshot["user_down"]; exists {
		// 生成账号落账后是池的普通候选: 基础设施失败只影响授信与冷却, 不应被标记 verified。
		if downState.verified || !downState.cooldownUntil.IsZero() {
			t.Fatalf("user_down 不应被授信或冷却: %+v", downState)
		}
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

// TestOpencodeAccountLedgerStates 验证台账三态派生、筛选与删除:
// 可用=健康探测; 未测试=仅录入; 不可用=冷却中或被拒无在效成功。
func TestOpencodeAccountLedgerStates(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	now := time.Now()

	if err := op.UpsertOpencodeGeneratedAccounts(channel.ID, []string{"acct_ok", "acct_untested"}); err != nil {
		t.Fatalf("落账失败: %v", err)
	}
	if err := op.SaveOpencodeProbeResult(channel.ID, "acct_ok", model.OpencodeAccountOutcomeHealthy, 200, now); err != nil {
		t.Fatalf("写探测结论失败: %v", err)
	}
	if err := op.SaveOpencodeProxyCooldown(channel.ID, "acct_cooling", now.Add(time.Hour)); err != nil {
		t.Fatalf("写冷却失败: %v", err)
	}
	if err := op.SaveOpencodeProbeResult(channel.ID, "acct_rejected", model.OpencodeAccountOutcomeRejected, 403, now); err != nil {
		t.Fatalf("写探测结论失败: %v", err)
	}

	ledger, err := ListOpencodeAccountStates(channel.ID, "all")
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if ledger.Usable != 1 || ledger.Unusable != 2 || ledger.Untested != 1 {
		t.Fatalf("三态计数异常: %+v", ledger)
	}
	states := make(map[string]string, len(ledger.Items))
	for _, item := range ledger.Items {
		states[item.Account] = item.State
	}
	if states["acct_ok"] != "usable" || states["acct_cooling"] != "unusable" ||
		states["acct_untested"] != "untested" || states["acct_rejected"] != "unusable" {
		t.Fatalf("状态派生异常: %+v", states)
	}

	filtered, err := ListOpencodeAccountStates(channel.ID, "untested")
	if err != nil {
		t.Fatalf("筛选失败: %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Account != "acct_untested" {
		t.Fatalf("untested 筛选异常: %+v", filtered.Items)
	}

	deleted, err := DeleteOpencodeAccountsFromLedger(channel.ID, []string{"acct_untested"})
	if err != nil || deleted != 1 {
		t.Fatalf("删除应移除 1 行: deleted=%d err=%v", deleted, err)
	}
	if ledger2, _ := ListOpencodeAccountStates(channel.ID, "all"); ledger2.Total != 3 {
		t.Fatalf("删除后台账应剩 3 行: %d", ledger2.Total)
	}
}

// TestOpencodeAutoProbeDueAccounts 验证到期选择: 未探测立即到期, 周期内跳过, 上限截断。
func TestOpencodeAutoProbeDueAccounts(t *testing.T) {
	now := time.Now()
	rows := []model.OpencodeProxyAccount{
		{Account: "fresh", LastProbeAt: now.Add(-time.Hour)},
		{Account: "untested"},
		{Account: "stale", LastProbeAt: now.Add(-25 * time.Hour)},
	}
	due := ocAutoProbeDueAccounts(rows, now, 24*time.Hour)
	if len(due) != 2 || due[0] != "untested" || due[1] != "stale" {
		t.Fatalf("到期选择异常: %v", due)
	}
	many := make([]model.OpencodeProxyAccount, 0, ocAutoProbeMaxPerChannel+5)
	for i := 0; i < ocAutoProbeMaxPerChannel+5; i++ {
		many = append(many, model.OpencodeProxyAccount{Account: fmt.Sprintf("acct_%03d", i)})
	}
	if got := ocAutoProbeDueAccounts(many, now, 24*time.Hour); len(got) != ocAutoProbeMaxPerChannel {
		t.Fatalf("应按上限截断: %d", len(got))
	}
}
