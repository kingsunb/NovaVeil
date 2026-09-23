package eval

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
)

const (
	EvalConcurrency  = 4
	evalTimeout      = 10 * time.Minute
	evalSaveTimeout  = 10 * time.Second
	evalTickInterval = 2 * time.Second
	// evalQueueReapInterval 控制运行中任务租约回收的周期；过期 running 由
	// op.ModelEvalQueueResetRunning 基于 15 分钟租约判定，这里每分钟扫一次。
	// 单实例进程启动不走这条租约，见 Scheduler.Start。
	evalQueueReapInterval = time.Minute
)

// Default 进程内调度器，供 handler 通过包级 Notify/Subscribe 访问。
var Default *Scheduler

// Scheduler 评估队列常驻调度器：优先级有序出队 + 固定并发槽。
type Scheduler struct {
	ctx      context.Context
	cancel   context.CancelFunc
	notify   chan struct{}
	running  atomic.Int32
	loopWG   sync.WaitGroup
	workerWG sync.WaitGroup
	// stateMu 串行化 Start/Stop 的状态迁移。Stop 在持锁期间等待 loop 与 worker
	// 全部退出后才把 started 置回 false，因此 Start 不可能在旧循环还在运行、
	// 或 stopOnce 式"只停一次"失效的窗口内创建第二套循环/取消函数。
	stateMu sync.Mutex
	started bool

	subMu sync.Mutex
	subs  map[chan []model.ModelEvalQueueTask]struct{}

	// inflight 是本进程 Pop 出去、尚未确认终态的任务 id。
	// 停机只退回这里面仍为 running 的行，不碰其它实例的 running，也不把已 done 的再入队。
	inflightMu sync.Mutex
	inflight   map[int64]struct{}
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		notify: make(chan struct{}, 1),
		subs:   make(map[chan []model.ModelEvalQueueTask]struct{}),
	}
}

func Notify() {
	if Default != nil {
		Default.Notify()
	}
}

func Publish() {
	if Default != nil {
		Default.Publish()
	}
}

func Subscribe() ([]model.ModelEvalQueueTask, chan []model.ModelEvalQueueTask) {
	if Default != nil {
		return Default.Subscribe()
	}
	return []model.ModelEvalQueueTask{}, nil
}

func Unsubscribe(ch chan []model.ModelEvalQueueTask) {
	if Default != nil {
		Default.Unsubscribe(ch)
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.started {
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	// 单实例 SQLite 立即回收本进程留下的 running；多实例只回收过期租约。
	if err := op.ModelEvalQueueResetRunningOnStart(s.ctx); err != nil {
		s.cancel()
		s.ctx = nil
		s.cancel = nil
		return err
	}
	s.started = true
	s.Publish()
	s.loopWG.Add(1)
	go s.loop()
	return nil
}

func (s *Scheduler) Stop() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.started {
		return nil
	}
	// 先取消 ctx 让 loop 退出；worker 用的是 ctx 派生上下文，也会被取消。
	// 未发出或被取消的评估保持 running，等 worker 退出后只把本进程的未完成行退回 queued。
	// 已经 MarkDone 的任务不在这批更新里，不会再打一次上游。
	if s.cancel != nil {
		s.cancel()
	}
	s.loopWG.Wait()
	s.workerWG.Wait()
	if err := s.requeueInflight(); err != nil {
		return err
	}
	s.started = false
	s.cancel = nil
	return nil
}

func (s *Scheduler) track(id int64) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight == nil {
		s.inflight = make(map[int64]struct{})
	}
	s.inflight[id] = struct{}{}
}

func (s *Scheduler) untrack(id int64) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	delete(s.inflight, id)
}

// requeueInflight 在 worker 全部退出后，把本进程仍标为 running 的任务退回 queued。
// 用独立超时：此时 s.ctx 已经取消，不能再拿它写库。
func (s *Scheduler) requeueInflight() error {
	s.inflightMu.Lock()
	ids := make([]int64, 0, len(s.inflight))
	for id := range s.inflight {
		ids = append(ids, id)
	}
	s.inflightMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := op.ModelEvalQueueRequeueRunning(ctx, ids); err != nil {
		return err
	}
	s.inflightMu.Lock()
	for _, id := range ids {
		delete(s.inflight, id)
	}
	s.inflightMu.Unlock()
	return nil
}

func (s *Scheduler) Notify() {
	if s == nil {
		return
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Scheduler) loop() {
	defer s.loopWG.Done()
	ticker := time.NewTicker(evalTickInterval)
	defer ticker.Stop()
	reapTicker := time.NewTicker(evalQueueReapInterval)
	defer reapTicker.Stop()
	for {
		s.dispatch()
		select {
		case <-s.ctx.Done():
			return
		case <-s.notify:
		case <-ticker.C:
		case <-reapTicker.C:
			s.reapExpiredRunning()
		}
	}
}

// reapExpiredRunning 周期回收租约过期的 running 任务：任一实例都可执行，
// 但 WHERE 条件(X 租约）保证不会重置健康实例仍在执行的评估。
func (s *Scheduler) reapExpiredRunning() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
	defer cancel()
	if err := op.ModelEvalQueueResetRunning(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warnf("eval queue reap expired running: %v", err)
		}
		return
	}
}

func (s *Scheduler) dispatch() {
	if s.ctx.Err() != nil {
		return
	}
	need := EvalConcurrency - int(s.running.Load())
	if need <= 0 {
		return
	}
	tasks, err := op.ModelEvalQueuePopNext(s.ctx, need)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warnf("eval queue pop: %v", err)
		}
		return
	}
	if len(tasks) == 0 {
		return
	}
	for i := range tasks {
		task := tasks[i]
		s.running.Add(1)
		s.workerWG.Add(1)
		go s.runWorker(task)
	}
	s.Publish()
}

func (s *Scheduler) runWorker(task model.ModelEvalQueueTask) {
	// defer 按注册逆序执行(执行顺序: recover → untrack → running+Notify → workerWG.Done)。
	// Notify 必须在 running 递减之后触发, 否则 loop 被唤醒时看到的 running 仍占满
	// 并发槽, 本次唤醒无效、队列可能迟迟不派发下一个任务(RELI-03 defer LIFO)。
	s.track(task.ID)
	defer s.workerWG.Done()
	defer func() {
		s.running.Add(-1)
		s.Notify()
	}()
	// abandoned 为真表示任务仍是 running（停机取消，或 panic 后 MarkDone 失败）。
	// 这类 id 留在 inflight，等 Stop 退回 queued；已定稿的从集合去掉。
	abandoned := false
	defer func() {
		if !abandoned {
			s.untrack(task.ID)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("eval worker panicked: %v\n%s", r, debug.Stack())
			// 兜底把队列行置为失败: 若只记日志, 该行将永久停留在 running,
			// 重启 reset 期间也不再被派发(RELI-02)。MarkDone 失败则留给 Stop / 下次启动回收。
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), evalSaveTimeout)
			if err := op.ModelEvalQueueMarkDone(saveCtx, task.ID, 0, fmt.Sprintf("eval worker panic: %v", r)); err != nil {
				log.Warnf("eval queue mark done after panic: %v", err)
				abandoned = true
			}
			cancel()
		}
	}()
	abandoned = s.executeTask(task)
	s.Publish()
}

// executeTask 跑完一条评估。返回 true 表示队列行仍是 running（停机取消，或 MarkDone
// 失败），调用方必须把 id 留在 inflight，由 Stop 退回 queued。返回 false 表示
// MarkDone 已成功，这次尝试结束，Stop 不会再入队。
func (s *Scheduler) executeTask(task model.ModelEvalQueueTask) bool {
	if s.ctx.Err() != nil {
		// 停机已经开始且上游请求还没发出：不要打上游。
		return true
	}
	started := time.Now()
	modelDeleted := false
	record := model.ModelEval{
		ModelEvalSummary: model.ModelEvalSummary{
			ChannelID:      task.ChannelID,
			ChannelModelID: task.ChannelModelID,
			ChannelName:    task.ChannelName,
			ChannelType:    task.ChannelType,
			ModelName:      task.ModelName,
			CreatedAt:      started,
		},
		Prompt: model.ModelEvalPrompt,
	}
	evalCtx, cancel := context.WithTimeout(s.ctx, evalTimeout)
	result, testErr := relay.TestChannelKeyFailover(evalCtx, task.ChannelID, task.ModelName, model.ModelEvalPrompt)
	cancel()
	if testErr != nil && errors.Is(testErr, context.Canceled) {
		// 停机取消不等于评估完成。不写失败历史、不 MarkDone，避免把未完成的任务
		// 当成已结束（那样就不会重试），也避免和 Stop 的「只退回 running」竞态。
		return true
	}
	record.CompletedAt = time.Now()
	record.LatencyMS = time.Since(started).Milliseconds()
	if testErr != nil {
		record.Outcome = model.ModelEvalError
		if errors.Is(testErr, context.DeadlineExceeded) {
			record.Error = "评估超时（最长等待 10 分钟）"
		} else {
			record.Error = testErr.Error()
		}
		if channel, err := op.ChannelGetCore(task.ChannelID); err == nil {
			if channel.Key != "" {
				record.Error = strings.ReplaceAll(record.Error, channel.Key, "[REDACTED]")
			}
			for _, key := range channel.Keys {
				if key.Key != "" {
					record.Error = strings.ReplaceAll(record.Error, key.Key, "[REDACTED]")
				}
			}
			// 免费渠道确定性失败(401/403/404)累计达阈值即自动删除该模型: 凭据失效/
			// 模型不存在这类失败重试无意义, 直接清理避免在历次评估中反复报错。
			if channel.IsFree && isFatalEvalStatus(testErr) {
				cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(s.ctx), evalSaveTimeout)
				deleted, fatalErr := op.ChannelModelRecordFatalEvalFailure(cleanCtx, task.ChannelModelID)
				cleanCancel()
				if fatalErr != nil {
					log.Warnf("eval fatal failure cleanup: %v", fatalErr)
				} else if deleted {
					modelDeleted = true
					record.Error += "（该免费模型累计确定性失败达到阈值，已自动删除）"
				}
			}
		}
	} else {
		record.Content = result.Content
		record.Outcome = model.ModelEvalContentOutcome(result.Content)
		record.LatencyMS = result.ElapsedMS
		record.PromptTokens = result.PromptTokens
		record.CompletionTokens = result.CompletionTokens
	}

	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(s.ctx), evalSaveTimeout)
	defer saveCancel()

	if err := op.ModelEvalCreate(saveCtx, &record); err != nil {
		log.Warnf("eval save history: %v", err)
		if markErr := op.ModelEvalQueueMarkDone(saveCtx, task.ID, 0, record.Error); markErr != nil {
			log.Warnf("eval queue mark done: %v", markErr)
			return true
		}
		return false
	}
	// 模型已因累计确定性失败被删除: 不再写针它的排序条目(会留下 channel_model_id 悬空的
	// 孤儿 rank), 也不裁剪其历史; 评估历史本身已在上面落库, 作为最后一次失败的审计留痕。
	if !modelDeleted {
		if _, err := op.ModelEvalPruneTarget(saveCtx, record.ChannelID, record.ModelName); err != nil {
			log.Warnf("eval prune target: %v", err)
		}
		rank := &model.ModelEvalRank{
			ChannelID:        record.ChannelID,
			ChannelModelID:   record.ChannelModelID,
			ChannelName:      record.ChannelName,
			ChannelType:      record.ChannelType,
			ModelName:        record.ModelName,
			Outcome:          record.Outcome,
			Error:            record.Error,
			Content:          record.Content,
			ContentTruncated: record.ContentTruncated,
			PromptTokens:     record.PromptTokens,
			CompletionTokens: record.CompletionTokens,
			LatencyMS:        record.LatencyMS,
			SourceEvalID:     record.ID,
		}
		if err := op.ModelEvalRankUpsert(saveCtx, rank); err != nil {
			log.Warnf("eval rank upsert: %v", err)
		}
	}
	if err := op.ModelEvalQueueMarkDone(saveCtx, task.ID, record.ID, record.Error); err != nil {
		log.Warnf("eval queue mark done: %v", err)
		return true
	}
	return false
}
