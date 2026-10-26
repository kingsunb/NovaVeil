package relay

import (
	"context"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// stubOcProbeCatalog 替换目录取名入口, 避免测试里真实外呼。
func stubOcProbeCatalog(t *testing.T) {
	t.Helper()
	original := opencodeProbeCatalog
	opencodeProbeCatalog = func(context.Context, model.Channel) (map[string]helper.OpencodeModelInfo, error) {
		return map[string]helper.OpencodeModelInfo{}, nil
	}
	t.Cleanup(func() { opencodeProbeCatalog = original })
}

// TestRunAccountProbeCancelsRemaining 验证作业时限到点后未体检的账号记为已取消,
// 而不是静默消失。
func TestRunAccountProbeCancelsRemaining(t *testing.T) {
	plan := &ocProbePlan{
		channel:     model.Channel{ID: 1},
		modelName:   "m",
		displayName: "m",
		accounts:    []string{"cancel-a", "cancel-b", "cancel-c"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := runAccountProbe(ctx, plan, 1, time.Second, nil)
	if report.Summary.Total != len(plan.accounts) {
		t.Fatalf("汇总总数异常: %+v", report.Summary)
	}
	for _, result := range report.Results {
		if result.Outcome != ocProbeOutcomeCancelled {
			t.Fatalf("未体检的账号应记为已取消: %+v", result)
		}
	}
}

// TestStartOpencodeAccountProbeRunsInBackground 验证体检改为后台作业:
// 接口立即返回且作业仍在跑, 结束后状态与结论可查。
func TestStartOpencodeAccountProbeRunsInBackground(t *testing.T) {
	setupFailoverTest(t)
	stubOcProbeCatalog(t)
	channel := createOpencodeProxyTestChannel(t, "http://user.{account}:pass@127.0.0.1:1", model.ChannelKey{Key: "probe-key"})

	original := ocProbeJobRunner
	t.Cleanup(func() { ocProbeJobRunner = original })
	release := make(chan struct{})
	ocProbeJobRunner = func(ctx context.Context, plan *ocProbePlan, concurrency int, perAccount time.Duration, onProgress func(AccountProbeResult)) *AccountProbeReport {
		<-release
		result := AccountProbeResult{Account: plan.accounts[0], Outcome: ocProbeOutcomeHealthy, Status: 200}
		onProgress(result)
		return &AccountProbeReport{
			ChannelID: plan.channel.ID,
			Model:     plan.modelName,
			Results:   []AccountProbeResult{result},
			Summary:   AccountProbeSummary{Total: 1, Healthy: 1},
		}
	}

	job, err := StartOpencodeAccountProbe(context.Background(), channel.ID, []string{"probe-acct"}, 0, 1, 1000, "")
	if err != nil {
		t.Fatalf("启动后台体检失败: %v", err)
	}
	if job.State != ocProbeJobStateRunning || job.Total != 1 || job.JobID == "" {
		t.Fatalf("作业初始状态异常: %+v", job)
	}
	select {
	case <-release:
		t.Fatal("接口返回后作业应继续在后台运行")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, ok := GetOpencodeAccountProbeJob(job.JobID)
		if !ok {
			t.Fatal("作业状态丢失")
		}
		if status.State != ocProbeJobStateRunning {
			if status.State != ocProbeJobStateDone {
				t.Fatalf("作业终态异常: %+v", status)
			}
			if status.Healthy != 1 || status.Probed != 1 || len(status.Results) != 1 {
				t.Fatalf("作业结果异常: %+v", status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("作业未在时限内结束")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
