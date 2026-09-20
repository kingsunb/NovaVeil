package task

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
)

type taskEntry struct {
	name       string
	interval   time.Duration
	fn         func()
	runOnStart bool
	ticker     *time.Ticker
	stopCh     chan struct{}
	stopOnce   sync.Once
	updateCh   chan time.Duration
	running    atomic.Bool // 单实例守卫: 任务执行超过周期间隔时跳过本 Tick, 防止同任务自身重叠
}

var (
	tasks   = make(map[string]*taskEntry)
	tasksMu sync.RWMutex

	// lifecycleCtx/lifecycleCancel 为所有后台任务提供共享生命周期 context。
	// StopAll 取消该 context 后, 任务函数通过 LifecycleContext() 感知停机并尽快收尾。
	lifecycleCtxMu  sync.Mutex
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	// runningWG 跟踪所有已启动的任务执行 goroutine(guardedCall)。
	// StopAll 关闭 stopCh 后等待该 WaitGroup, 确保没有任务 goroutine 在 DB 关闭后仍在运行。
	runningWG sync.WaitGroup
	// runningWGMu/runningStopped 同步 goGuardedCall 的 Add 与 StopAll 的 Wait:
	// 仅靠 stopCh 存在竞态——ticker 事件可能已在 select 中胜出, 在 Wait 开始后才调用
	// Add(1), 使本轮执行逃脱 StopAll 的等待并在 DB 关闭后继续写库(审计 RELI-01)。
	runningWGMu    sync.Mutex
	runningStopped bool

	// runStopCh 在 StopAll 时关闭, 使 RUN() 能解除阻塞并返回。
	runStopCh   chan struct{}
	runStopOnce sync.Once
)

func init() {
	lifecycleCtx, lifecycleCancel = context.WithCancel(context.Background())
	runStopCh = make(chan struct{})
}

// LifecycleContext 返回后台任务的生命周期 context。任务函数应基于此 context
// 派生自己的超时 context, 而非使用 context.Background(): 停机时 StopAll 取消
// 该 context, 任务函数可感知并尽快返回, 不在 DB 关闭后继续写入。
func LifecycleContext() context.Context {
	lifecycleCtxMu.Lock()
	defer lifecycleCtxMu.Unlock()
	return lifecycleCtx
}

// resetLifecycle 重建生命周期 context, 供测试在 StopAll 后重置状态。仅用于测试。
func resetLifecycle() {
	lifecycleCtxMu.Lock()
	defer lifecycleCtxMu.Unlock()
	lifecycleCtx, lifecycleCancel = context.WithCancel(context.Background())
	runStopCh = make(chan struct{})
	runStopOnce = sync.Once{}
	runningWGMu.Lock()
	runningStopped = false
	runningWGMu.Unlock()
}

// Register 注册一个定时任务
// runOnStart: 是否在启动时立即执行一次
func Register(name string, interval time.Duration, runOnStart bool, fn func()) {
	if interval <= 0 {
		log.Debugf("task %s not registered: interval is 0", name)
		return
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

	if _, exists := tasks[name]; exists {
		log.Warnf("task %s already registered, skipping", name)
		return
	}

	tasks[name] = &taskEntry{
		name:       name,
		interval:   interval,
		fn:         fn,
		runOnStart: runOnStart,
		stopCh:     make(chan struct{}),
		updateCh:   make(chan time.Duration, 4), // 带缓冲，避免 Update 非阻塞 send 丢失间隔更新
	}
	log.Debugf("task %s registered with interval %v, runOnStart: %v", name, interval, runOnStart)
}

// Update 更新任务的执行间隔
// 当 interval 为 0 时，删除任务
func Update(name string, interval time.Duration) {
	tasksMu.Lock()
	entry, exists := tasks[name]
	if !exists {
		tasksMu.Unlock()
		log.Warnf("task %s not found", name)
		return
	}

	if interval <= 0 {
		delete(tasks, name)
		tasksMu.Unlock()
		entry.stopOnce.Do(func() { close(entry.stopCh) })
		log.Infof("task %s removed: interval is 0", name)
		return
	}
	tasksMu.Unlock()

	select {
	case entry.updateCh <- interval:
		log.Infof("task %s interval updated to %v", name, interval)
	default:
		log.Warnf("task %s update channel full, skipping", name)
	}
}

// RUN 启动所有注册的任务, 阻塞主协程直到 StopAll 被调用。
func RUN() {
	tasksMu.RLock()
	for _, entry := range tasks {
		go runTask(entry)
	}
	tasksMu.RUnlock()

	// 阻塞主协程, 直到 StopAll 关闭 runStopCh
	<-runStopCh
}

// StopAll 停止所有定时任务: 先取消生命周期 context 让运行中的任务感知停机,
// 再关闭每个任务的 stopCh 停止 ticker, 最后等待所有正在执行的任务 goroutine 终态,
// 防止数据库关闭后后台任务继续触发写操作。
func StopAll() error {
	// 先取消生命周期 context: 运行中的任务函数通过 LifecycleContext() 感知后尽快返回。
	lifecycleCtxMu.Lock()
	cancel := lifecycleCancel
	lifecycleCtxMu.Unlock()
	if cancel != nil {
		cancel()
	}

	tasksMu.Lock()
	entries := make([]*taskEntry, 0, len(tasks))
	for _, entry := range tasks {
		entries = append(entries, entry)
	}
	tasksMu.Unlock()

	// 先关闭 stop 屏障: 之后的任何 goGuardedCall 都必须看到 runningStopped 并放弃 Add,
	// 已在屏障前 Add 的执行被 Wait 一并等待。锁序保证 Add 绝不会发生在 Wait 返回之后。
	runningWGMu.Lock()
	runningStopped = true
	runningWGMu.Unlock()

	for _, entry := range entries {
		entry.stopOnce.Do(func() { close(entry.stopCh) })
	}

	// 等待所有正在执行的任务 goroutine 结束, 确保没有任务在 DB 关闭后继续写入。
	runningWG.Wait()

	// 解除 RUN() 的阻塞, 使主协程能够优雅返回。
	runStopOnce.Do(func() { close(runStopCh) })
	return nil
}

// safeCall 在独立 goroutine 中执行任务，捕获 panic 防止单个任务的异常导致整个进程退出
func safeCall(entry *taskEntry) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("task %s panicked: %v\n%s", entry.name, r, debug.Stack())
		}
	}()
	entry.fn()
}

// guardedCall 在 safeCall 之上加单实例守卫: 上一轮尚未结束时跳过本轮触发。
// 慢于周期的任务(慢盘/慢网络)否则会自身重叠, 共享内存状态的任务尤其危险。
func guardedCall(entry *taskEntry) {
	if !entry.running.CompareAndSwap(false, true) {
		log.Warnf("task %s skipped: previous run still in progress", entry.name)
		return
	}
	defer entry.running.Store(false)
	safeCall(entry)
}

// goGuardedCall 在独立 goroutine 中执行 guardedCall 并通过 runningWG 跟踪,
// 使 StopAll 能等待所有正在执行的任务 goroutine 终态。
// Add 与 goroutine 启动必须在 runningWGMu 临界区内完成: 若 Add 之后、启动之前
// StopAll 拿到锁并 Wait, 计数虽为 1 但永远不会 Done, StopAll 将永久阻塞;
// 若不加锁, Add 又可能发生在 StopAll 的 Wait 之后, 让任务逃脱停机等待。
func goGuardedCall(entry *taskEntry) {
	runningWGMu.Lock()
	if runningStopped {
		runningWGMu.Unlock()
		return
	}
	runningWG.Add(1)
	go func() {
		defer runningWG.Done()
		guardedCall(entry)
	}()
	runningWGMu.Unlock()
}

func runTask(entry *taskEntry) {
	// 根据配置决定是否在启动时立即执行
	if entry.runOnStart {
		goGuardedCall(entry)
	}

	entry.ticker = time.NewTicker(entry.interval)
	defer func() {
		if entry.ticker != nil {
			entry.ticker.Stop()
		}
	}()

	for {
		select {
		case <-entry.ticker.C:
			goGuardedCall(entry)
		case newInterval := <-entry.updateCh:
			entry.ticker.Stop()
			entry.interval = newInterval
			entry.ticker = time.NewTicker(newInterval)
		case <-entry.stopCh:
			return
		}
	}
}
