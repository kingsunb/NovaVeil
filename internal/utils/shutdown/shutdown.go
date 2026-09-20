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

	ilog  logger
	funcs []hookFunc

	// shutdownDone is created on the first shutdown attempt and remains closed
	// for the lifetime of the current initialization cycle.
	shutdownDone chan struct{}
	hookTimeout  = defaultHookTimeout
)

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
	if log != nil {
		log.Infof("Shutdown completed successfully")
	}
}

func runHookWithTimeout(fn func() error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}

	result := make(chan error, 1)
	go func() {
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
