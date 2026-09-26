package relay

// 失败请求排查链路单元测试: 错误分类哨兵分支、尝试轨迹追加与截断、失败环形缓冲淘汰与过滤,
// 以及 committed 后流中断的渠道记账语义(记失败且保留用量)。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// TestClassifyRoundSentinelBranches 覆盖一轮尝试的各分类判定分支与优先级。
func TestClassifyRoundSentinelBranches(t *testing.T) {
	parent := context.Background()
	timeoutCtx, cancelTimeout := context.WithCancelCause(parent)
	cancelTimeout(errMemberResponseTimeout)
	abortCtx, cancelAbort := context.WithCancelCause(parent)
	cancelAbort(nil)
	canceledParent, cancelParent := context.WithCancel(parent)
	cancelParent()
	doubleCanceled, cancelDouble := context.WithCancelCause(canceledParent)
	cancelDouble(nil)
	liveRoundCtx, cancelLive := context.WithCancelCause(parent)
	defer cancelLive(nil)

	cases := []struct {
		name     string
		err      error
		parent   context.Context
		round    context.Context
		expected ErrClass
	}{
		{"零输出哨兵", fmt.Errorf("%w: output tokens reported as 0", errZeroOutput), parent, liveRoundCtx, ErrClassZeroOutput},
		{"超时哨兵直判", errMemberResponseTimeout, parent, liveRoundCtx, ErrClassTimeout},
		{"超时经上下文原因还原", errors.New("等待成员非流式完整响应超时(5 秒)"), parent, timeoutCtx, ErrClassTimeout},
		{"客户端取消优先于人工中止", context.Canceled, canceledParent, doubleCanceled, ErrClassClientCancel},
		{"人工中止", context.Canceled, parent, abortCtx, ErrClassAdminAbort},
		{"上游4xx结构化", fmt.Errorf("request failed: %w", &httpclient.Error{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}), parent, liveRoundCtx, ErrClassUpstream4xx},
		{"上游5xx结构化", &httpclient.Error{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"}, parent, liveRoundCtx, ErrClassUpstream5xx},
		{"上游5xx文本解析", errors.New("upstream responded 502 Bad Gateway: {\"error\":\"boom\"}"), parent, liveRoundCtx, ErrClassUpstream5xx},
		// 网络/连接层错误: connection refused 归到 ErrClassUpstreamNetwork, 不计入成员冷却。
		{"无状态码兜底", errors.New("connection refused"), parent, liveRoundCtx, ErrClassUpstreamNetwork},
		{"正文数字不误判", errors.New("quota exceeded, cost 500 dollars"), parent, liveRoundCtx, ErrClassUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRound(tc.err, tc.parent, tc.round); got != tc.expected {
				t.Fatalf("classifyRound = %q, 期望 %q", got, tc.expected)
			}
		})
	}
}

// TestClassifyErrorSentinels 验证终态分类入口对哨兵错误与状态码的归类。
func TestClassifyErrorSentinels(t *testing.T) {
	if got := ClassifyError(nil); got != "" {
		t.Fatalf("空错误应返回空分类, 实际 %q", got)
	}
	if got := ClassifyError(fmt.Errorf("%w: wrapped", errZeroOutput)); got != ErrClassZeroOutput {
		t.Fatalf("零输出应归 zero_output, 实际 %q", got)
	}
	if got := ClassifyError(fmt.Errorf("outer: %w", errMemberResponseTimeout)); got != ErrClassTimeout {
		t.Fatalf("超时应归 timeout, 实际 %q", got)
	}
	if got := ClassifyError(&httpclient.Error{StatusCode: 429}); got != ErrClassUpstream4xx {
		t.Fatalf("429 应归 upstream_4xx, 实际 %q", got)
	}
	if got := ClassifyError(errors.New("upstream responded 500 Internal Server Error")); got != ErrClassUpstream5xx {
		t.Fatalf("500 文本应归 upstream_5xx, 实际 %q", got)
	}
	if got := ClassifyError(errors.New("mystery")); got != ErrClassUpstream {
		t.Fatalf("未知错误应归 upstream_error, 实际 %q", got)
	}
	if got := ClassifyError(errChannelConcurrencyFull); got != ErrClassChannelBusy {
		t.Fatalf("单成员并发满载应归 channel_busy, 实际 %q", got)
	}
	if got := ClassifyError(fmt.Errorf("wrapped: %w", errAllChannelsBusy)); got != ErrClassChannelBusy {
		t.Fatalf("全部成员并发满载(即使包裹)应归 channel_busy, 实际 %q", got)
	}
}

// TestAttemptRecordsAppendTruncateAndCap 验证尝试轨迹的追加、摘要截断、容量裁剪与 SSE 发布。
func TestAttemptRecordsAppendTruncateAndCap(t *testing.T) {
	request := newRequestState("obs-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, request.ID)
	mu.Unlock()

	// 注册一个观察连接, 验证轨迹随请求状态推送。
	stream := make(chan RequestState, streamBuffer)
	mu.Lock()
	watchers[stream] = struct{}{}
	mu.Unlock()
	defer func() {
		mu.Lock()
		delete(watchers, stream)
		mu.Unlock()
	}()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	seq := request.startRound(cancel, RoundTarget{MemberID: 7, ChannelID: 42, ChannelName: "chan-a", Model: "model-x"})
	if seq != 1 {
		t.Fatalf("首轮序号应为 1, 实际 %d", seq)
	}
	longErr := strings.Repeat("a", 250) + strings.Repeat("中", 30) // 250 字节后跟多字节字符, 截断点落在字符内部。
	request.finishRound(AttemptFailed, ErrClassTimeout, longErr)

	<-stream              // startRound 发布的进行中快照。
	published := <-stream // finishRound 发布的收尾快照。
	if len(published.Attempts) != 1 || published.Sending {
		t.Fatalf("发布的状态应含一条轨迹且不在发送中, 实际 %+v", published.Attempts)
	}
	first := request.Attempts[0]
	if first.Seq != 1 || first.ChannelID != 42 || first.ChannelName != "chan-a" || first.MemberID != 7 || first.Model != "model-x" {
		t.Fatalf("首条轨迹字段不符: %+v", first)
	}
	if first.Outcome != AttemptFailed || first.ErrClass != ErrClassTimeout {
		t.Fatalf("首条轨迹结束形态应为 failed/timeout, 实际 %s/%s", first.Outcome, first.ErrClass)
	}
	if first.LatencyMS < 0 {
		t.Fatalf("耗时不应为负, 实际 %d", first.LatencyMS)
	}
	if len(first.ErrBrief) > errBriefLimit {
		t.Fatalf("摘要应不超过 %d 字节, 实际 %d", errBriefLimit, len(first.ErrBrief))
	}
	if !utf8.ValidString(first.ErrBrief) {
		t.Fatal("截断后的摘要应是合法 UTF-8")
	}

	// 第二轮成功: 成功轮次不带分类与摘要。
	request.startRound(cancel, RoundTarget{MemberID: 7, ChannelID: 42, ChannelName: "chan-a", Model: "model-x"})
	request.finishRound(AttemptSuccess, "", "")
	second := request.Attempts[1]
	if second.Outcome != AttemptSuccess || second.ErrClass != "" || second.ErrBrief != "" {
		t.Fatalf("成功轮次形态不符: %+v", second)
	}

	// 取消轮次使用默认摘要。
	request.startRound(cancel, RoundTarget{MemberID: 8, ChannelID: 43, ChannelName: "chan-b", Model: "model-y"})
	request.finishRound(AttemptCanceled, ErrClassAdminAbort, "")
	if brief := request.Attempts[len(request.Attempts)-1].ErrBrief; brief != "人工中止" {
		t.Fatalf("人工中止轮次应有默认摘要, 实际 %q", brief)
	}

	// 超出容量后保留最近 maxAttempts 条且序号连续。
	baseSeq := request.Round
	for i := 0; i < maxAttempts+20; i++ {
		request.startRound(cancel, RoundTarget{MemberID: i, ChannelID: i, ChannelName: "chan-loop", Model: "model-loop"})
		request.finishRound(AttemptFailed, ErrClassUpstream, fmt.Errorf("fail %d", i).Error())
	}
	if len(request.Attempts) != maxAttempts {
		t.Fatalf("轨迹应裁剪到 %d 条, 实际 %d", maxAttempts, len(request.Attempts))
	}
	if request.Attempts[0].Seq != baseSeq+21 || request.Attempts[len(request.Attempts)-1].Seq != baseSeq+maxAttempts+20 {
		t.Fatalf("裁剪后应保留最新记录, 实际首尾序号 %d/%d", request.Attempts[0].Seq, request.Attempts[len(request.Attempts)-1].Seq)
	}
}

// stubFailureRing 备份并清空失败环形缓冲, 测试结束自动还原。
func stubFailureRing(t *testing.T) {
	t.Helper()
	mu.Lock()
	oldRing, oldCount, oldNext := failureRing, failureCount, failureNext
	failureRing = make([]FailureSummary, maxFailureRecords)
	failureCount, failureNext = 0, 0
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		failureRing, failureCount, failureNext = oldRing, oldCount, oldNext
		mu.Unlock()
	})
}

// TestFailureRingEvictionAndFilter 验证环形缓冲的容量淘汰、新到旧排序、limit 与 class 过滤。
func TestFailureRingEvictionAndFilter(t *testing.T) {
	stubFailureRing(t)

	const total = maxFailureRecords + 50
	mu.Lock()
	for i := 1; i <= total; i++ {
		class := ErrClassUpstream
		if i%2 == 0 {
			class = ErrClassZeroOutput
		}
		appendFailureLocked(FailureSummary{ID: uint64(i), Model: "group", TargetChannel: fmt.Sprintf("chan-%d", i), ErrClass: class, ErrBrief: "brief"})
	}
	mu.Unlock()

	all := FailureSummaries(0, "")
	if len(all) != maxFailureRecords {
		t.Fatalf("缓冲应只保留 %d 条, 实际 %d", maxFailureRecords, len(all))
	}
	if all[0].ID != total || all[len(all)-1].ID != uint64(total-maxFailureRecords+1) {
		t.Fatalf("应按新到旧排列且淘汰最旧, 实际首尾 ID %d/%d", all[0].ID, all[len(all)-1].ID)
	}

	filtered := FailureSummaries(0, ErrClassZeroOutput)
	if len(filtered) == 0 || len(filtered) != maxFailureRecords/2 {
		t.Fatalf("按类过滤应得 %d 条, 实际 %d", maxFailureRecords/2, len(filtered))
	}
	for _, summary := range filtered {
		if summary.ErrClass != ErrClassZeroOutput {
			t.Fatalf("过滤结果混入其他分类: %+v", summary)
		}
	}

	limited := FailureSummaries(3, "")
	if len(limited) != 3 || limited[0].ID != total || limited[2].ID != total-2 {
		t.Fatalf("limit 应限制返回条数并保持新到旧, 实际 %+v", limited)
	}
}

// TestMarkFailedTerminalClassAndRingSummary 验证终态写入分类字段并落一条失败摘要, 取消终态不进环形缓冲。
func TestMarkFailedTerminalClassAndRingSummary(t *testing.T) {
	setupFailoverTest(t)
	stubFailureRing(t)

	request := newRequestState("obs-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, request.ID)
	mu.Unlock()

	request.TargetChannel = "chan-final"
	request.TargetModel = "model-final"
	request.markFailed(fmt.Errorf("%w: output tokens reported as 0", errZeroOutput), "", nil)
	if request.Status != StatusFailed || request.Class != ErrClassZeroOutput {
		t.Fatalf("终态应为 failed/zero_output, 实际 %s/%s", request.Status, request.Class)
	}
	ring := FailureSummaries(0, "")
	if len(ring) != 1 {
		t.Fatalf("失败终态应落一条摘要, 实际 %d 条", len(ring))
	}
	if ring[0].ID != request.ID || ring[0].TargetChannel != "chan-final" || ring[0].TargetModel != "model-final" || ring[0].ErrClass != ErrClassZeroOutput {
		t.Fatalf("摘要字段不符: %+v", ring[0])
	}
	if ring[0].FinishedAt.IsZero() {
		t.Fatal("摘要应带失败时间")
	}

	canceled := newRequestState("obs-group", "{}", "", "", "")
	mu.Lock()
	delete(requests, canceled.ID)
	mu.Unlock()
	canceled.markCanceled(context.Canceled, "", nil)
	if canceled.Class != ErrClassClientCancel {
		t.Fatalf("取消终态分类应为 client_cancel, 实际 %q", canceled.Class)
	}
	if ring = FailureSummaries(0, ""); len(ring) != 1 {
		t.Fatalf("取消终态不应进入失败环形缓冲, 实际 %d 条", len(ring))
	}
}

// TestCommittedStreamFailureTrail 验证 committed 后流中断:
// 请求终态为失败并带分类, 尝试轨迹记录成功取得响应的一轮。
func TestCommittedStreamFailureTrail(t *testing.T) {
	setupFailoverTest(t)

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-obs","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-obs","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"}}]}`)
		writeUpstreamSSE(t, w, "", `{"id":"chatcmpl-obs","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}`)
		// 内容帧之后以非法 chunk 分帧切断连接, 让转发侧在读剩余事件时得到真实读错误而非干净 EOF。
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("测试服务器不支持连接劫持")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("zzz-not-a-chunk\r\n")
		rw.Flush()
	}))
	defer upstream.Close()

	channel := createIntegrationChannel(t, "it-obs-broken", model.ChannelProviderOpenAI, upstream.URL, "it-model-obs")
	group := createIntegrationGroup(t, "it-observability",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     2,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, "it-model-obs"))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	server := httptest.NewServer(engine)
	defer server.Close()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, group.Name)
	expectedID := nextRequestID()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("发起流式请求失败: %v", err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()

	waitFor(t, 5*time.Second, func() bool {
		state := requestStateOf(t, expectedID)
		return state.Status == StatusFailed || state.Status == StatusCanceled || state.Status == StatusSuccess
	})
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("committed 后中断应以失败终态结束, 实际 %s(%s)", state.Status, state.Error)
	}
	if state.Class != ErrClassUpstream {
		t.Fatalf("读错误应归 upstream_error, 实际 %q", state.Class)
	}
	if got := upstreamHits.Load(); got != 1 {
		t.Fatalf("committed 后不得重试, 实际尝试 %d 次", got)
	}
	if len(state.Attempts) != 1 {
		t.Fatalf("应有一条尝试轨迹, 实际 %d 条", len(state.Attempts))
	}
	attempt := state.Attempts[0]
	if attempt.Seq != 1 || attempt.Outcome != AttemptSuccess || attempt.ChannelName != channel.Name || attempt.Model != "it-model-obs" {
		t.Fatalf("轨迹应记录成功取得响应的一轮: %+v", attempt)
	}
}
