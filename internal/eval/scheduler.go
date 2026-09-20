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
	if err := op.ModelEvalQueueResetRunning(s.ctx); err != nil {
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
	if s.cancel != nil {
		s.cancel()
	}
	s.loopWG.Wait()
	s.workerWG.Wait()
	s.started = false
	s.cancel = nil
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
	// defer 按注册逆序执行(执行顺序: recover → running+Nofity → workerWG.Done)。
	// Notify 必须在 running 递减之后触发, 否则 loop 被唤醒时看到的 running 仍占满
	// 并发槽, 本次唤醒无效、队列可能迟迟不派发下一个任务(RELI-03 defer LIFO)。
	defer s.workerWG.Done()
	defer func() {
		s.running.Add(-1)
		s.Notify()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("eval worker panicked: %v\n%s", r, debug.Stack())
			// 兜底把队列行置为失败: 若只记日志, 该行将永久停留在 running,
			// 重启 reset 期间也不再被派发(RELI-02)。
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), evalSaveTimeout)
			if err := op.ModelEvalQueueMarkDone(saveCtx, task.ID, 0, fmt.Sprintf("eval worker panic: %v", r)); err != nil {
				log.Warnf("eval queue mark done after panic: %v", err)
			}
			cancel()
		}
	}()
	s.executeTask(task)
	s.Publish()
}

func (s *Scheduler) executeTask(task model.ModelEvalQueueTask) {
	started := time.Now()
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
	record.CompletedAt = time.Now()
	record.LatencyMS = time.Since(started).Milliseconds()
	if testErr != nil {
		record.Outcome = model.ModelEvalError
		switch {
		case errors.Is(testErr, context.DeadlineExceeded):
			record.Error = "评估超时（最长等待 10 分钟）"
		case errors.Is(testErr, context.Canceled):
			record.Error = "评估请求已取消"
		default:
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
		}
		return
	}
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
	if err := op.ModelEvalQueueMarkDone(saveCtx, task.ID, record.ID, record.Error); err != nil {
		log.Warnf("eval queue mark done: %v", err)
	}
}
