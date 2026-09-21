package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/looplj/axonhub/llm"
)

func TestBodyStringCopyOverBudgetReleases(t *testing.T) {
	restore := testingSetRelayBodyBudget(1000)
	t.Cleanup(restore)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/chat/completions", Forward(llm.APIFormatOpenAIChatCompletion))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Repeat("a", 600)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("切片加上字符串副本超过预算时应 413, 实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := relayBodyBudgetInUse(); got != 0 {
		t.Fatalf("副本放不下时两笔额度都应释放, 实际 %d", got)
	}
}

func TestMaskFailureFinalizesBodyAndReleasesBudget(t *testing.T) {
	setupFailoverTest(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("脱敏失败不得打到上游")
	}))
	defer upstream.Close()
	channel := createIntegrationChannel(t, "it-mask-fail", model.ChannelProviderOpenAI, upstream.URL, "it-model-mask-fail")
	group := createIntegrationGroup(t, "it-mask-fail", model.GroupRelayConfig{},
		integrationLeafItem(t, channel, "it-model-mask-fail"))

	orig := requestMask
	requestMask = func(body []byte, sessionKey string, groupMaskEnabled bool) ([]byte, *mask.Mapping, []mask.Match, error) {
		return nil, nil, nil, errors.New("mask down")
	}
	t.Cleanup(func() { requestMask = orig })

	before := relayBodyBudgetInUse()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	content := strings.Repeat("明", maxBodyPreview)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"` + content + `"}]}`
	expectedID := nextRequestID()
	recorder := postRelayJSON(t, engine, path, body, "sess-mask-fail", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("脱敏失败应拒绝, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := relayBodyBudgetInUse(); got != before {
		t.Fatalf("脱敏失败后预算 = %d, 期望 %d", got, before)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("脱敏失败应以失败定稿, 实际 %s", state.Status)
	}
	if n := len(RequestBody(expectedID)); n > maxBodyPreview {
		t.Fatalf("失败定稿后请求体应截断, 实际 %d 字节", n)
	}
}

func TestPanicFinalizesBodyAndReleasesBudget(t *testing.T) {
	setupFailoverTest(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("panic 不得打到上游")
	}))
	defer upstream.Close()
	channel := createIntegrationChannel(t, "it-mask-panic", model.ChannelProviderOpenAI, upstream.URL, "it-model-mask-panic")
	group := createIntegrationGroup(t, "it-mask-panic", model.GroupRelayConfig{},
		integrationLeafItem(t, channel, "it-model-mask-panic"))

	orig := requestMask
	requestMask = func(body []byte, sessionKey string, groupMaskEnabled bool) ([]byte, *mask.Mapping, []mask.Match, error) {
		panic("mask boom")
	}
	t.Cleanup(func() { requestMask = orig })

	before := relayBodyBudgetInUse()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/chat/completions", Forward(llm.APIFormatOpenAIChatCompletion))
	content := strings.Repeat("文", maxBodyPreview)
	body := `{"model":"` + group.Name + `","messages":[{"role":"user","content":"` + content + `"}]}`
	expectedID := nextRequestID()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		engine.ServeHTTP(rec, req)
	}()
	if !panicked {
		t.Fatal("脱敏 panic 应继续向外抛, 以便 gin 的 recover 生效")
	}
	if got := relayBodyBudgetInUse(); got != before {
		t.Fatalf("panic 后预算 = %d, 期望 %d", got, before)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusFailed {
		t.Fatalf("panic 路径应以失败定稿, 实际 %s", state.Status)
	}
	if n := len(RequestBody(expectedID)); n > maxBodyPreview {
		t.Fatalf("panic 定稿后请求体应截断, 实际 %d 字节", n)
	}
}
