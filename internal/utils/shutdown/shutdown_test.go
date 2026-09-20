package shutdown

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownRunsHooksInLIFOOrder(t *testing.T) {
	Init(nil)

	var order []int
	Register(func() error {
		order = append(order, 1)
		return nil
	})
	Register(func() error {
		order = append(order, 2)
		return nil
	})
	Register(func() error {
		order = append(order, 3)
		return nil
	})

	Shutdown()

	if want := []int{3, 2, 1}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestShutdownContinuesAfterHookTimeout(t *testing.T) {
	Init(nil)
	previousTimeout := hookTimeout
	hookTimeout = 80 * time.Millisecond
	t.Cleanup(func() {
		hookTimeout = previousTimeout
		Init(nil)
	})

	started := make(chan time.Time, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	continued := make(chan struct{})

	// Register the fast hook first so the blocking hook is run first by LIFO.
	Register(func() error {
		close(continued)
		return nil
	})
	Register(func() error {
		started <- time.Now()
		<-release
		close(finished)
		return nil
	})

	shutdownDone := make(chan struct{})
	go func() {
		Shutdown()
		close(shutdownDone)
	}()

	var hookStarted time.Time
	select {
	case hookStarted = <-started:
	case <-time.After(time.Second):
		t.Fatal("timed-out hook did not start")
	}

	select {
	case <-continued:
		if elapsed := time.Since(hookStarted); elapsed < hookTimeout/2 {
			t.Fatalf("next hook started after %v, before timeout %v", elapsed, hookTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not continue after hook timeout")
	}

	// Shutdown 现在必须等超时钩子 goroutine 归零后才返回：最终器（如 db.Close）
	// 不能与仍在活动的钩子并发。释放阻塞钩子后应能正常返回。
	close(release)
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after timed-out hook finished")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed-out hook did not finish after release")
	}
}

func TestShutdownConcurrentAndRepeatedRunsHooksOnce(t *testing.T) {
	Init(nil)
	previousTimeout := hookTimeout
	hookTimeout = time.Second
	t.Cleanup(func() {
		hookTimeout = previousTimeout
		Init(nil)
	})

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	Register(func() error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			Shutdown()
		}()
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shutdown hook did not start")
	}
	close(release)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent Shutdown calls did not all return")
	}

	Shutdown()
	if got := calls.Load(); got != 1 {
		t.Fatalf("hook calls = %d, want 1", got)
	}
}
