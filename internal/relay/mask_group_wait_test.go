package relay

// 分组暂不可得与脱敏的时序契约: 请求在分组被删期间等待, 恢复那一轮
// 先脱敏再转发, 绝不放行明文; 脱敏每请求只执行一次, 重试轮复用结果。

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/looplj/axonhub/llm"
)

// TestMaskWaitsForGroupThenMasksBeforeForward 验证分组短暂不可得后,
// 恢复那一轮先脱敏再转发: 上游只见到脱敏体, 且脱敏恰好执行一次。
func TestMaskWaitsForGroupThenMasksBeforeForward(t *testing.T) {
	setupFailoverTest(t)
	const secret = "机密数据-黑盒密钥"
	var upstreamBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读上游请求体失败: %v", err)
		}
		upstreamBody.Store(string(data))
		w.Header().Set("Content-Type", "application/json")
		w.Write(chatCompletionBody("up-mask-wait", "ok", 1, 1))
	}))
	defer upstream.Close()
	channel := createIntegrationChannel(t, "it-mask-wait", model.ChannelProviderOpenAI, upstream.URL, "it-model-mask-wait")
	group := createIntegrationGroup(t, "it-mask-wait", model.GroupRelayConfig{},
		integrationLeafItem(t, channel, "it-model-mask-wait"))

	var maskCalls atomic.Int32
	origMask := requestMask
	requestMask = func(body []byte, _ string, _ bool) ([]byte, *mask.Mapping, []mask.Match, error) {
		maskCalls.Add(1)
		masked := bytes.ReplaceAll(body, []byte(secret), []byte("MASKED_TEST_PLACEHOLDER"))
		return masked, nil, nil, nil
	}
	t.Cleanup(func() { requestMask = origMask })

	// 主循环首轮分组暂不可得: 请求必须等待, 不得用明文直发上游。
	origLookup := groupLookupFunc
	groupLookupFunc = func(name string) (model.Group, error) {
		return model.Group{}, errors.New("group transient missing")
	}
	t.Cleanup(func() { groupLookupFunc = origLookup })

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"` + secret + `"}]}`
	expectedID := nextRequestID()
	codeCh := make(chan int, 1)
	go func() {
		rec := postRelayJSON(t, engine, path, body, "sess-mask-wait", nil)
		codeCh <- rec.Code
	}()

	// 请求进入等待路径后恢复分组读取, 模拟分组短暂被删后重现。
	time.Sleep(100 * time.Millisecond)
	groupLookupFunc = origLookup

	select {
	case code := <-codeCh:
		if code != http.StatusOK {
			t.Fatalf("分组恢复后应正常转发, 实际 %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("分组恢复后请求未完成转发")
	}
	if got := maskCalls.Load(); got != 1 {
		t.Fatalf("脱敏应恰好执行一次, 实际 %d 次", got)
	}
	sent, _ := upstreamBody.Load().(string)
	if strings.Contains(sent, secret) {
		t.Fatalf("上游收到明文敏感词, 请求体: %s", sent)
	}
	if !strings.Contains(sent, "MASKED_TEST_PLACEHOLDER") {
		t.Fatalf("上游未收到占位符, 请求体: %s", sent)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess {
		t.Fatalf("请求应以成功定稿, 实际 %s", state.Status)
	}
}

// TestMaskOnceAcrossRetryRounds 验证重试轮复用同一脱敏结果, 不重复扫描:
// 上游失败与分组消失都发生后, 恢复继续转发时 requestMask 仍只调用一次。
func TestMaskOnceAcrossRetryRounds(t *testing.T) {
	setupFailoverTest(t)
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamHits.Add(1) == 1 {
			// 首轮上游 500: 触发成员失败重试, 请求进入下一轮。
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(chatCompletionBody("up-mask-retry", "ok", 1, 1))
	}))
	defer upstream.Close()
	config := model.GroupRelayConfig{MemberRetryIntervalSeconds: 0.05}
	channel := createIntegrationChannel(t, "it-mask-retry", model.ChannelProviderOpenAI, upstream.URL, "it-model-mask-retry")
	group := createIntegrationGroup(t, "it-mask-retry", config,
		integrationLeafItem(t, channel, "it-model-mask-retry"))

	var maskCalls atomic.Int32
	origMask := requestMask
	requestMask = func(body []byte, _ string, _ bool) ([]byte, *mask.Mapping, []mask.Match, error) {
		maskCalls.Add(1)
		masked := bytes.ReplaceAll(body, []byte("机密数据-重试密钥"), []byte("MASKED_RETRY_PLACEHOLDER"))
		return masked, nil, nil, nil
	}
	t.Cleanup(func() { requestMask = origMask })

	// 分组读取: 首轮成功(脱敏在此发生), 重试轮暂时不可得, 之后再恢复。
	origLookup := groupLookupFunc
	lookupCalls := 0
	groupLookupFunc = func(name string) (model.Group, error) {
		lookupCalls++
		if lookupCalls == 2 {
			return model.Group{}, errors.New("group transient missing")
		}
		return origLookup(name)
	}
	t.Cleanup(func() { groupLookupFunc = origLookup })

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"机密数据-重试密钥"}]}`
	rec := postRelayJSON(t, engine, path, body, "sess-mask-retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("分组恢复后应正常转发, 实际 %d", rec.Code)
	}
	if got := maskCalls.Load(); got != 1 {
		t.Fatalf("跨重试轮脱敏应只执行一次, 实际 %d 次", got)
	}
	if upstreamHits.Load() != 2 {
		t.Fatalf("上游应被调用两次(首轮 500 + 恢复轮 200), 实际 %d 次", upstreamHits.Load())
	}
}
