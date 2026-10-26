package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// 后台体检作业的状态枚举。
const (
	ocProbeJobStateRunning   = "running"
	ocProbeJobStateDone      = "done"
	ocProbeJobStateCancelled = "cancelled"
)

const (
	// ocProbeJobKeepCount 进程内保留的最近作业数, 超出后淘汰最旧的。
	ocProbeJobKeepCount = 32
	// ocProbeJobMinDeadline 作业整体时限下界, 覆盖单账号超时的最小批次。
	ocProbeJobMinDeadline = 2 * time.Minute
	// ocProbeJobMaxDeadline 作业整体时限上界, 避免无界占用出口配额。
	ocProbeJobMaxDeadline = 30 * time.Minute
)

// OpencodeProbeJob 是后台体检作业的对外视图; 字段与一次体检报告同构, 便于前端直接展示。
type OpencodeProbeJob struct {
	JobID      string               `json:"job_id"`
	ChannelID  int                  `json:"channel_id"`
	Model      string               `json:"model"`
	State      string               `json:"state"`
	Total      int                  `json:"total"`
	Probed     int                  `json:"probed"`
	Healthy    int                  `json:"healthy"`
	Rejected   int                  `json:"rejected"`
	Failed     int                  `json:"failed"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt time.Time            `json:"finished_at"`
	Message    string               `json:"message,omitempty"`
	Results    []AccountProbeResult `json:"results,omitempty"`
	Summary    AccountProbeSummary  `json:"summary"`
}

// ocProbeJobEntry 是作业的进程内状态。
type ocProbeJobEntry struct {
	mu     sync.Mutex
	job    OpencodeProbeJob
	cancel context.CancelFunc
}

// ocProbeJobs 保存最近的后台体检作业。
var ocProbeJobs = struct {
	mu    sync.Mutex
	items map[string]*ocProbeJobEntry
	order []string
}{items: map[string]*ocProbeJobEntry{}}

// StartOpencodeAccountProbe 启动后台体检作业并立即返回。
// 作业上下文独立于调用方的 HTTP 请求, 因此请求返回后体检继续在后台跑完;
// 作业整体时限到点后, 在途探测被取消, 未开始的账号记为已取消。
func StartOpencodeAccountProbe(ctx context.Context, channelID int, accounts []string, count, concurrency, timeoutMS int, modelName string) (OpencodeProbeJob, error) {
	plan, err := prepareAccountProbe(ctx, channelID, accounts, count, modelName)
	if err != nil {
		return OpencodeProbeJob{}, err
	}
	if concurrency <= 0 {
		concurrency = ocProbeDefaultConcurrency
	}
	if concurrency > ocProbeMaxConcurrency {
		concurrency = ocProbeMaxConcurrency
	}
	if timeoutMS <= 0 {
		timeoutMS = 30_000
	}
	if timeoutMS > ocProbeMaxTimeoutMS {
		timeoutMS = ocProbeMaxTimeoutMS
	}
	perAccount := time.Duration(timeoutMS) * time.Millisecond
	rounds := (len(plan.accounts) + concurrency - 1) / concurrency
	deadline := perAccount * time.Duration(rounds)
	if deadline < ocProbeJobMinDeadline {
		deadline = ocProbeJobMinDeadline
	}
	if deadline > ocProbeJobMaxDeadline {
		deadline = ocProbeJobMaxDeadline
	}
	jobCtx, cancel := context.WithTimeout(context.Background(), deadline)
	entry := &ocProbeJobEntry{
		job: OpencodeProbeJob{
			JobID:     newOcProbeJobID(),
			ChannelID: plan.channel.ID,
			Model:     plan.modelName,
			State:     ocProbeJobStateRunning,
			Total:     len(plan.accounts),
			StartedAt: time.Now(),
		},
		cancel: cancel,
	}
	ocProbeJobs.mu.Lock()
	ocProbeJobs.items[entry.job.JobID] = entry
	ocProbeJobs.order = append(ocProbeJobs.order, entry.job.JobID)
	for len(ocProbeJobs.order) > ocProbeJobKeepCount {
		oldest := ocProbeJobs.order[0]
		ocProbeJobs.order = ocProbeJobs.order[1:]
		if stale, ok := ocProbeJobs.items[oldest]; ok {
			stale.cancel()
			delete(ocProbeJobs.items, oldest)
		}
	}
	ocProbeJobs.mu.Unlock()
	go entry.run(jobCtx, plan, concurrency, perAccount)
	return entry.snapshot(), nil
}

// GetOpencodeAccountProbeJob 返回作业状态; 作业不存在时 ok 为 false。
func GetOpencodeAccountProbeJob(jobID string) (OpencodeProbeJob, bool) {
	ocProbeJobs.mu.Lock()
	entry, ok := ocProbeJobs.items[jobID]
	ocProbeJobs.mu.Unlock()
	if !ok {
		return OpencodeProbeJob{}, false
	}
	return entry.snapshot(), true
}

// ocProbeJobRunner 是后台作业的执行入口; 测试替换它以免真实外呼。
var ocProbeJobRunner = runAccountProbe

// run 执行作业并回填进度与终态。
func (e *ocProbeJobEntry) run(ctx context.Context, plan *ocProbePlan, concurrency int, perAccount time.Duration) {
	report := ocProbeJobRunner(ctx, plan, concurrency, perAccount, func(result AccountProbeResult) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.job.Probed++
		switch result.Outcome {
		case ocProbeOutcomeHealthy:
			e.job.Healthy++
		case ocProbeOutcomeRejected:
			e.job.Rejected++
		default:
			e.job.Failed++
		}
	})
	e.mu.Lock()
	e.job.Results = report.Results
	e.job.Summary = report.Summary
	e.job.FinishedAt = time.Now()
	if ctx.Err() != nil {
		e.job.State = ocProbeJobStateCancelled
		e.job.Message = "体检超时, 未完成的账号已取消"
	} else {
		e.job.State = ocProbeJobStateDone
	}
	e.mu.Unlock()
	e.cancel()
}

// snapshot 返回作业的当前视图。
func (e *ocProbeJobEntry) snapshot() OpencodeProbeJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	view := e.job
	if len(e.job.Results) > 0 {
		view.Results = append([]AccountProbeResult(nil), e.job.Results...)
	}
	return view
}

// newOcProbeJobID 生成作业号。
func newOcProbeJobID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("probe-%d", time.Now().UnixNano())
	}
	return "probe-" + hex.EncodeToString(buf)
}
