package relay

// goSafe 行为契约: panic 不外泄到调用方与进程, 正常路径照常执行。

import (
	"testing"
	"time"
)

// TestGoSafeRecoversPanic 验证协程内 panic 被 goSafe 兜住, 不击穿测试进程。
func TestGoSafeRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	goSafe("test-panic", func() {
		defer close(done)
		panic("goSafe boom")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goSafe 协程未执行到 defer")
	}
	// 走到这里进程未因 panic 退出即兜底生效。
}

// TestGoSafeRunsNormalFunc 验证无 panic 时 fn 正常执行完毕。
func TestGoSafeRunsNormalFunc(t *testing.T) {
	done := make(chan struct{})
	goSafe("test-normal", func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goSafe 未执行正常函数")
	}
}
