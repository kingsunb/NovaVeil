package relay

import (
	"context"
	"errors"
	"io"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// 用户生产日志中的真实错误形态: 非流式透传路径上 axonhub HttpClient.Do 包装的连接提前中断。
var errDiagUnexpectedEOF = &url.Error{
	Op:  "Post",
	URL: "https://api.stepfun.com/step_plan/v1/chat/completions",
	Err: io.ErrUnexpectedEOF,
}

// shrinkDiagInterval 测试期间把重试间隔缩到毫秒级并注册恢复, 避免真实等待拖慢测试。
func shrinkDiagInterval(t *testing.T) {
	t.Helper()
	orig := diagInfraRetryInterval
	diagInfraRetryInterval = time.Millisecond
	t.Cleanup(func() { diagInfraRetryInterval = orig })
}

// TestSendDiagnosticUpstreamRetriesInfraError 验证诊断探针对基础设施层错误按
// diagInfraRetryLimit 计数重试(阈值含首次失败), 任一次成功即返回成功。
func TestSendDiagnosticUpstreamRetriesInfraError(t *testing.T) {
	shrinkDiagInterval(t)
	limit := diagInfraRetryLimit()
	var calls atomic.Int64
	send := func() (*upstreamResponse, error) {
		if calls.Add(1) < int64(limit) {
			return nil, errDiagUnexpectedEOF
		}
		return &upstreamResponse{}, nil
	}
	resp, err := sendDiagnosticUpstream(context.Background(), send)
	if err != nil {
		t.Fatalf("第 %d 次应成功, 实际返回错误: %v", limit, err)
	}
	if resp == nil {
		t.Fatal("成功时不应返回 nil 响应")
	}
	if got := calls.Load(); got != int64(limit) {
		t.Fatalf("上游调用次数 = %d, want %d", got, limit)
	}
}

// TestSendDiagnosticUpstreamStopsAtLimit 验证连续基础设施错误达到上限后返回最后一次错误,
// 不再发起额外调用。
func TestSendDiagnosticUpstreamStopsAtLimit(t *testing.T) {
	shrinkDiagInterval(t)
	limit := diagInfraRetryLimit()
	var calls atomic.Int64
	send := func() (*upstreamResponse, error) {
		calls.Add(1)
		return nil, errDiagUnexpectedEOF
	}
	_, err := sendDiagnosticUpstream(context.Background(), send)
	if !errors.Is(err, errDiagUnexpectedEOF) {
		t.Fatalf("应返回最后一次基础设施错误, 实际: %v", err)
	}
	if got := calls.Load(); got != int64(limit) {
		t.Fatalf("上游调用次数 = %d, want %d", got, limit)
	}
}

// TestSendDiagnosticUpstreamDoesNotRetryBusinessError 验证业务错误(429/5xx 等)不重试:
// 重试不可能改变结果, 只会烧计费请求。
func TestSendDiagnosticUpstreamDoesNotRetryBusinessError(t *testing.T) {
	shrinkDiagInterval(t)
	bizErr := errors.New("POST - https://upstream/v1/chat/completions with status 429 Too Many Requests: quota exceeded")
	var calls atomic.Int64
	send := func() (*upstreamResponse, error) {
		calls.Add(1)
		return nil, bizErr
	}
	_, err := sendDiagnosticUpstream(context.Background(), send)
	if !errors.Is(err, bizErr) {
		t.Fatalf("应原样返回业务错误, 实际: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("业务错误不该重试, 实际调用 %d 次", got)
	}
}

// TestSendDiagnosticUpstreamDoesNotRetryContextError 验证上下文取消/超时不重试:
// 调用方已放弃或时间预算耗尽, 重试只会把等待拉长 N 倍。
func TestSendDiagnosticUpstreamDoesNotRetryContextError(t *testing.T) {
	shrinkDiagInterval(t)
	var calls atomic.Int64
	send := func() (*upstreamResponse, error) {
		calls.Add(1)
		return nil, &url.Error{Op: "Post", URL: "https://u", Err: context.DeadlineExceeded}
	}
	_, err := sendDiagnosticUpstream(context.Background(), send)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应保留超时语义, 实际: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("上下文超时不该重试, 实际调用 %d 次", got)
	}
}

// TestSendDiagnosticUpstreamStopsWhenContextCanceledDuringWait 验证等待间隔期间 ctx 被取消
// 时立即放弃并返回最后一次错误, 不再发起新调用。
func TestSendDiagnosticUpstreamStopsWhenContextCanceledDuringWait(t *testing.T) {
	orig := diagInfraRetryInterval
	diagInfraRetryInterval = 50 * time.Millisecond
	t.Cleanup(func() { diagInfraRetryInterval = orig })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	send := func() (*upstreamResponse, error) {
		calls.Add(1)
		cancel()
		return nil, errDiagUnexpectedEOF
	}
	_, err := sendDiagnosticUpstream(ctx, send)
	if !errors.Is(err, errDiagUnexpectedEOF) {
		t.Fatalf("应返回最后一次基础设施错误, 实际: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("等待期间取消后不该再调用, 实际调用 %d 次", got)
	}
}
