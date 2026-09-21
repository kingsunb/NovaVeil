package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRelayBodyBudgetRejectsOverLimitAndReleases(t *testing.T) {
	restore := testingSetRelayBodyBudget(32)
	t.Cleanup(restore)
	before := relayBodyBudgetInUse()

	body := bytes.Repeat([]byte("a"), 64)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	_, release, err := readLimitedHTTPRequest(req)
	if !errors.Is(err, ErrRelayBodyTooLarge) {
		t.Fatalf("超过进程预算应拒绝, 得到 %v", err)
	}
	if release != nil {
		t.Fatal("拒绝路径不应把额度交给调用方")
	}
	if got := relayBodyBudgetInUse(); got != before {
		t.Fatalf("超限拒绝后额度 = %d, 期望 %d", got, before)
	}
}

func TestRelayBodyBudgetCancelWhileWaitingReleases(t *testing.T) {
	restore := testingSetRelayBodyBudget(100)
	t.Cleanup(restore)
	if err := acquireRelayBodyBudget(context.Background(), 100); err != nil {
		t.Fatalf("占满预算: %v", err)
	}
	t.Cleanup(func() { releaseRelayBodyBudget(100) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- acquireRelayBodyBudget(ctx, 40)
	}()
	time.Sleep(20 * time.Millisecond)
	if got := relayBodyBudgetInUse(); got != 100 {
		t.Fatalf("等待中的请求不应先占用额度, 实际 %d", got)
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled, 得到 %v", err)
	}
	if got := relayBodyBudgetInUse(); got != 100 {
		t.Fatalf("取消后只应剩下原占用, 实际 %d", got)
	}
	releaseRelayBodyBudget(100)
	if got := relayBodyBudgetInUse(); got != 0 {
		t.Fatalf("原占用释放后额度 = %d, 期望 0", got)
	}
}

func TestRelayBodyBudgetReadCancelReleasesPartial(t *testing.T) {
	restore := testingSetRelayBodyBudget(1 << 20)
	t.Cleanup(restore)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", &cancelAfterFirstRead{ctx: ctx, cancel: cancel})
	req = req.WithContext(ctx)
	_, release, err := readLimitedHTTPRequest(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("读中取消应失败, 得到 %v", err)
	}
	if release != nil {
		t.Fatal("失败路径不应返回 release")
	}
	if got := relayBodyBudgetInUse(); got != 0 {
		t.Fatalf("取消后额度应释放, 实际 %d", got)
	}
}

func TestRelayBodyBudgetGzipFailureReleases(t *testing.T) {
	restore := testingSetRelayBodyBudget(1 << 20)
	t.Cleanup(restore)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("not-gzip")))
	req.Header.Set("Content-Encoding", "gzip")
	_, release, err := readLimitedHTTPRequest(req)
	if err == nil {
		t.Fatal("坏 gzip 应失败")
	}
	if release != nil {
		release()
	}
	if got := relayBodyBudgetInUse(); got != 0 {
		t.Fatalf("解压失败后额度应释放, 实际 %d", got)
	}
}

func TestRelayBodyBudgetCountsCompressedAndDecompressed(t *testing.T) {
	restore := testingSetRelayBodyBudget(1 << 20)
	t.Cleanup(restore)
	payload := bytes.Repeat([]byte("a"), 8000)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := compressed.Bytes()
	if err := acquireRelayBodyBudget(context.Background(), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Content-Encoding", "gzip")
	decoded, extra, separate, err := decodeLimitedBodyBudget(context.Background(), raw, headers, maxRelayDecompressedBodyBytes)
	if err != nil {
		releaseRelayBodyBudget(int64(len(raw)))
		t.Fatal(err)
	}
	if !separate {
		t.Fatal("gzip 应产出独立的解压缓冲")
	}
	if string(decoded) != string(payload) {
		t.Fatalf("解压长度 = %d, 期望 %d", len(decoded), len(payload))
	}
	peak := relayBodyBudgetInUse()
	if peak != int64(len(raw))+extra {
		t.Fatalf("重叠期间占用 = %d, 期望压缩 %d + 解压 %d", peak, len(raw), extra)
	}
	releaseRelayBodyBudget(int64(len(raw)))
	if got := relayBodyBudgetInUse(); got != extra {
		t.Fatalf("丢弃压缩缓冲后应只留解压占用 %d, 实际 %d", extra, got)
	}
	releaseRelayBodyBudget(extra)
	if got := relayBodyBudgetInUse(); got != 0 {
		t.Fatalf("全部释放后额度 = %d", got)
	}
}

func TestRelayBodyBudgetSmallRequestFitsBesideLargeHold(t *testing.T) {
	restore := testingSetRelayBodyBudget(1000)
	t.Cleanup(restore)
	if err := acquireRelayBodyBudget(context.Background(), 900); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseRelayBodyBudget(900) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	_, release, err := readLimitedHTTPRequest(req)
	if err != nil {
		t.Fatalf("预算有余量时小请求不应被拒绝: %v", err)
	}
	release()
	if got := relayBodyBudgetInUse(); got != 900 {
		t.Fatalf("小请求结束后应只剩原占用, 实际 %d", got)
	}
}

// cancelAfterFirstRead 先交出一段正文, 再取消上下文并让后续 Read 失败。
type cancelAfterFirstRead struct {
	ctx    context.Context
	cancel context.CancelFunc
	did    bool
}

func (r *cancelAfterFirstRead) Read(p []byte) (int, error) {
	if !r.did {
		r.did = true
		n := copy(p, []byte(`{"model":"m","messages":[]}`))
		r.cancel()
		return n, nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *cancelAfterFirstRead) Close() error { return nil }

var _ io.ReadCloser = (*cancelAfterFirstRead)(nil)
