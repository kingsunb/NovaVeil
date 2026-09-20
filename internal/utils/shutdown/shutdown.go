package shutdown

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type logger interface {
	Infof(template string, args ...interface{})
	Errorf(template string, args ...interface{})
	Warnf(template string, args ...interface{})
}

const defaultHookTimeout = 10 * time.Second

var (
	mu sync.Mutex

	ilog       logger
	funcs      []hookFunc
	finalizers []func() error

	// shutdownDone is created on the first shutdown attempt and remains closed
	// for the lifetime of the current initialization cycle.
	shutdownDone chan struct{}
	hookTimeout  = defaultHookTimeout

	// hookWG 跟踪所有被 runHookWithTimeout 启动、可能因超时仍在运行的钩子 goroutine。
	// 最终器(如 db.Close)开始前必须等它归零，避免关闭资源与仍活动的钩子并发执行。
	hookWG sync.WaitGroup
)

// 常规钩子超时后，最终器开始前还愿意等待这些 goroutine 的最长时间。
// 超过该上限则跳过最终器，把资源释放交给进程退出时的操作系统兜底，
// 绝不带着仍在运行的钩子进程内关闭数据库。
const outstandingHooksWaitTimeout = 10 * time.Minute

// hookFunc 绑定单个停机钩子与其专属超时; timeout<=0 表示使用全局默认。
type hookFunc struct {
	fn      func() error
	timeout time.Duration
}

func Init(log logger) {
	mu.Lock()
	defer mu.Unlock()

	ilog = log
	funcs = make([]hookFunc, 0)
	finalizers = nil
	shutdownDone = nil
}

func Register(fn func() error) {
	RegisterWithTimeout(fn, 0)
}

// RegisterWithTimeout 注册带专属超时的停机钩子。timeout<=0 回退到全局默认。
// 长任务(eval/任务泵)停止需要比默认 10s 更宽的有界超时, 又不能让停机无限悬挂。
func RegisterWithTimeout(fn func() error, timeout time.Duration) {
	mu.Lock()
	defer mu.Unlock()

	// A shutdown snapshot must not be changed while it is executing.
	if shutdownDone != nil {
		return
	}
	funcs = append(funcs, hookFunc{fn: fn, timeout: timeout})
}

// RegisterFinalizer 注册常规钩子全部结束（包括等待超时钩子 goroutine 归零）后
// 同步执行的最终器，用于数据库 Close 这类必须排他的资源释放。
func RegisterFinalizer(fn func() error) {
	mu.Lock()
	defer mu.Unlock()

	if shutdownDone != nil {
		return
	}
	finalizers = append(finalizers, fn)
}

func Listen() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	mu.Lock()
	log := ilog
	mu.Unlock()
	if log != nil {
		log.Infof("Program started, press Ctrl+C to exit")
	}

	sig := <-quit
	signal.Stop(quit)
	if log != nil {
		log.Warnf("Received exit signal: %v", sig)
	}

	runShutdownHooks()
	os.Exit(0)
}

func Shutdown() {
	runShutdownHooks()
}

func runShutdownHooks() {
	mu.Lock()
	if shutdownDone != nil {
		done := shutdownDone
		mu.Unlock()
		<-done
		return
	}

	done := make(chan struct{})
	shutdownDone = done
	hooks := append([]hookFunc(nil), funcs...)
	fins := append([]func() error(nil), finalizers...)
	timeout := hookTimeout
	log := ilog
	mu.Unlock()

	defer close(done)

	// Hooks execute in strict reverse registration order. A timeout only
	// releases shutdown sequencing; the hook goroutine cannot be cancelled.
	for i := len(hooks) - 1; i >= 0; i-- {
		hookTimeout := timeout
		if hooks[i].timeout > 0 {
			hookTimeout = hooks[i].timeout
		}
		if err := runHookWithTimeout(hooks[i].fn, hookTimeout); err != nil && log != nil {
			log.Errorf("Closing functions execution failed: %v", err)
		}
	}

	// 常规钩子全部走完后，仍可能有超时未返回的钩子 goroutine 在写数据库/文件。
	// 最终器（db.Close 等）必须等它们归零后再执行；等待超时则跳过最终器，
	// 由进程退出时 OS 兜底释放，而不是带着活动写入关闭资源。
	allHooksDone := waitOutstandingHooks(outstandingHooksWaitTimeout)
	if allHooksDone {
		for i, fn := range fins {
			if err := callHook(fn); err != nil && log != nil {
				log.Errorf("Shutdown finalizer %d failed: %v", i, err)
			}
		}
	} else if log != nil {
		log.Errorf("Timed-out shutdown hooks are still running after %s; skipping finalizers (relying on OS cleanup on exit)", outstandingHooksWaitTimeout)
	}
	if log != nil {
		log.Infof("Shutdown completed successfully")
	}
}

func waitOutstandingHooks(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = outstandingHooksWaitTimeout
	}
	done := make(chan struct{})
	go func() {
		hookWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func runHookWithTimeout(fn func() error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}

	result := make(chan error, 1)
	hookWG.Add(1)
	go func() {
		defer hookWG.Done()
		result <- callHook(fn)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-result:
		return err
	case <-timer.C:
		return fmt.Errorf("shutdown hook timed out after %s", timeout)
	}
}

func callHook(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("shutdown hook panicked: %v", recovered)
		}
	}()
	if fn == nil {
		return fmt.Errorf("shutdown hook is nil")
	}
	return fn()
}
