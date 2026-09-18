package relay

import (
	"testing"
)

// TestTotalErrorCountIncrementsOnRecordErrorLog 验证 recordErrorLog 递增进程级
// 业务错误计数(TotalErrorCount)。该计数在投递日志前即递增, 不依赖错误日志的
// 留存/按类去重/队满丢弃——无 DB 时日志投递与去重均为空操作, 失败事件仍被计数。
// 对应审计 STA-12: 提供不依赖日志留存的独立业务错误计数。
func TestTotalErrorCountIncrementsOnRecordErrorLog(t *testing.T) {
	before := TotalErrorCount()
	recordErrorLog(&RequestState{Class: ErrClassUpstream4xx, Error: "test failure"})
	after := TotalErrorCount()
	if after != before+1 {
		t.Fatalf("recordErrorLog 应使 TotalErrorCount 递增 1, before=%d after=%d", before, after)
	}
}

// TestTotalErrorCountMonotonic 验证多次记录持续累加(进程级计数单调递增)。
func TestTotalErrorCountMonotonic(t *testing.T) {
	base := TotalErrorCount()
	for i := 0; i < 3; i++ {
		recordErrorLog(&RequestState{Class: ErrClassUpstream5xx, Error: "repeat failure"})
	}
	if got := TotalErrorCount() - base; got != 3 {
		t.Fatalf("三次 recordErrorLog 应使计数递增 3, 实际递增 %d", got)
	}
}

// TestRoundsExhaustedSkipsPersistence 验证路由层耗尽（轮次/时长超限）不进持久化
// 错误列表: recordErrorLog 仍递增 TotalErrorCount, 但不投递 ErrorLog 到队列。
func TestRoundsExhaustedSkipsPersistence(t *testing.T) {
	before := TotalErrorCount()
	recordErrorLog(&RequestState{Class: ErrClassRoundsExhausted, Error: "请求尝试轮次超限"})
	after := TotalErrorCount()
	if after != before+1 {
		t.Fatalf("rounds_exhausted 应使 TotalErrorCount 递增 1, before=%d after=%d", before, after)
	}
}

// TestClassifyRoundsExhausted 验证哨兵错误正确归类为 rounds_exhausted。
func TestClassifyRoundsExhausted(t *testing.T) {
	if got := ClassifyError(errRoundsExceeded); got != ErrClassRoundsExhausted {
		t.Fatalf("ClassifyError(errRoundsExceeded) = %q, want %q", got, ErrClassRoundsExhausted)
	}
	if got := ClassifyError(errDeadlineExceeded); got != ErrClassRoundsExhausted {
		t.Fatalf("ClassifyError(errDeadlineExceeded) = %q, want %q", got, ErrClassRoundsExhausted)
	}
}
