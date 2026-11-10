package relay

import (
	"errors"
	"strings"
	"testing"
)

func TestMarkFailedKeepsRawErrorForAdmin(t *testing.T) {
	setupFailoverTest(t)
	stubFailureRing(t)

	request := newRequestState("redact-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, request.ID)
	mu.Unlock()

	err := errors.New(`upstream 401: Authorization: Bearer sk-live-secret api_key=plain-secret`)
	request.markFailed(err, "", nil)
	if request.Class != ClassifyError(err) {
		t.Fatalf("分类应基于原始错误, got %q", request.Class)
	}
	if !strings.Contains(request.Error, "sk-live-secret") || !strings.Contains(request.Error, "plain-secret") {
		t.Fatalf("后台失败原文应保留完整错误: %s", request.Error)
	}
}

func TestMarkCanceledKeepsRawErrorForAdmin(t *testing.T) {
	setupFailoverTest(t)

	request := newRequestState("redact-cancel-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, request.ID)
	mu.Unlock()

	request.markCanceled(errors.New("client closed: token=plain-secret"), "", nil)
	if !strings.Contains(request.Error, "plain-secret") {
		t.Fatalf("后台取消原文应保留完整错误: %s", request.Error)
	}
}

func TestFinishRoundKeepsRawErrorForAdmin(t *testing.T) {
	request := newRequestState("redact-round-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, request.ID)
	mu.Unlock()

	request.finishRound(AttemptFailed, ErrClassUpstream, "Authorization: Bearer sk-round-secret")
	if !strings.Contains(request.Error, "sk-round-secret") {
		t.Fatalf("后台中间态原文应保留完整错误: %s", request.Error)
	}
}

func TestDownstreamErrorTextRedacts(t *testing.T) {
	got := downstreamErrorText(errors.New(`upstream 401: Authorization: Bearer sk-live-secret api_key=plain-secret`))
	if strings.Contains(got, "sk-live-secret") || strings.Contains(got, "plain-secret") {
		t.Fatalf("下游错误文本仍含敏感值: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("下游错误文本应保留脱敏占位: %s", got)
	}
}
