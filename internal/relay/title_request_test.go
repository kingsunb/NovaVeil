package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// 标题类请求识别的边界: 三条判定条件任一不成立都不拦截。
func TestIsTitleGenerationRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "dsh 标题请求命中",
			body: `{"messages":[{"role":"developer","content":"Create a concise title for an AI coding-assistant session."},{"role":"user","content":"Generate the session title: [{\"seq\":8}]"}]}`,
			want: true,
		},
		{
			name: "content parts 正文命中",
			body: `{"messages":[{"role":"system","content":[{"type":"text","text":"You are a title generator."}]},{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			want: true,
		},
		{
			name: "工具数组为空仍命中",
			body: `{"messages":[{"role":"user","content":"session title"}],"tools":[]}`,
			want: true,
		},
		{
			name: "无标题标记不命中",
			body: `{"messages":[{"role":"user","content":"现在几点"}]}`,
			want: false,
		},
		{
			name: "声明工具时放行",
			body: `{"messages":[{"role":"system","content":"session title"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]}`,
			want: false,
		},
		{
			name: "超过两条消息放行",
			body: `{"messages":[{"role":"user","content":"session title"},{"role":"assistant","content":"a"},{"role":"user","content":"b"}]}`,
			want: false,
		},
		{
			name: "空消息放行",
			body: `{"messages":[]}`,
			want: false,
		},
		{
			name: "无 messages 字段放行",
			body: `{"input":"session title"}`,
			want: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isTitleGenerationRequest([]byte(testCase.body)); got != testCase.want {
				t.Fatalf("判定结果应为 %v, 实际 %v", testCase.want, got)
			}
		})
	}
}

// titlePolicyUpstream 返回固定成功响应的假上游, 并记录被调用次数。
func titlePolicyUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("it-title", "pong", 10, 2))
	}))
}

const titleRequestIntegrationBody = `{"model":%q,"messages":[{"role":"developer","content":"Create a concise title for an AI coding-assistant session."},{"role":"user","content":"Generate the session title: [{\"seq\":8}]"}],"stream":false}`

// 分组开启拒绝时, 标题类请求以 400 收尾且不发起上游调用。
func TestTitleRequestRejectedByGroupPolicy(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := titlePolicyUpstream(t, &hits)
	defer upstream.Close()

	modelName := integrationUniqueName("it-title-model")
	channel := createIntegrationChannel(t, "it-title-channel", model.ChannelProviderOpenAI, upstream.URL, modelName)
	config := refSkipConfig()
	config.RejectTitleRequests = true
	group := createIntegrationGroup(t, "it-title-group", config, integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, fmt.Sprintf(titleRequestIntegrationBody, group.Name), "", nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("标题类请求应以 400 收尾, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), errTitleRequestRejected.Error()) {
		t.Fatalf("下游应收到分组策略拒绝提示, 实际: %s", recorder.Body.String())
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("被拒绝的请求不得发起上游调用, 实际 %d 次", got)
	}
}

// 关掉开关后同一请求照常转发。
func TestTitleRequestForwardedWhenGroupPolicyOff(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := titlePolicyUpstream(t, &hits)
	defer upstream.Close()

	modelName := integrationUniqueName("it-title-off-model")
	channel := createIntegrationChannel(t, "it-title-off-channel", model.ChannelProviderOpenAI, upstream.URL, modelName)
	group := createIntegrationGroup(t, "it-title-off-group", refSkipConfig(), integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, fmt.Sprintf(titleRequestIntegrationBody, group.Name), "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关关闭时应正常转发, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("上游应被调用一次, 实际 %d 次", got)
	}
}

// 开关打开但请求声明了工具时按普通请求转发。
func TestTitleRequestWithToolsForwarded(t *testing.T) {
	setupFailoverTest(t)

	var hits atomic.Int64
	upstream := titlePolicyUpstream(t, &hits)
	defer upstream.Close()

	modelName := integrationUniqueName("it-title-tools-model")
	channel := createIntegrationChannel(t, "it-title-tools-channel", model.ChannelProviderOpenAI, upstream.URL, modelName)
	config := refSkipConfig()
	config.RejectTitleRequests = true
	group := createIntegrationGroup(t, "it-title-tools-group", config, integrationLeafItem(t, channel, modelName))

	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"system","content":"session title"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"x","parameters":{"type":"object","properties":{}}}}],"stream":false}`, group.Name)
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("带工具的请求应正常转发, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("上游应被调用一次, 实际 %d 次", got)
	}
}
