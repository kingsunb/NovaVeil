package relay

// 无密钥渠道测试: 上游为免认证服务时, 透传与转换两条路径发出的上游请求
// 都不得携带任何认证头; 含密钥渠道的带头行为不受影响。
// 端到端用例经 Forward gin 入口 + httptest 假上游驱动完整转发循环。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// capturedUpstream 记录假上游收到的认证相关请求头, 并发安全。
type capturedUpstream struct {
	mu            sync.Mutex
	authorization []string
	apiKey        []string
}

func (c *capturedUpstream) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authorization = append(c.authorization, r.Header.Get("Authorization"))
	c.apiKey = append(c.apiKey, r.Header.Get("X-Api-Key"))
}

func (c *capturedUpstream) snapshot() (authorization, apiKey []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.authorization...), append([]string(nil), c.apiKey...)
}

// writeChatCompletion 以标准 OpenAI Chat 非流式响应回写假上游。
func writeChatCompletion(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(chatCompletionBody("chatcmpl-keyless", content, 1, 4))
}

// createKeylessChannel 创建不携带任何凭据的真实渠道记录并写入缓存。
func createKeylessChannel(t *testing.T, name, baseURL, modelName string, provider model.ChannelProvider) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name:    integrationUniqueName(name),
		Type:    provider,
		Enabled: true,
		BaseURL: baseURL,
		Models:  []model.ChannelModel{{Name: modelName, Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建无密钥渠道 %s 失败: %v", name, err)
	}
	return channel
}

// TestBuildPassthroughRequestKeylessOmitsAuth 单元验证透传构造:
// 无密钥渠道 Auth 保持 nil 且不产生 Authorization/X-Api-Key 头; 有密钥渠道两种协议形态照常带头。
func TestBuildPassthroughRequestKeylessOmitsAuth(t *testing.T) {
	raw := &httpclient.Request{
		Method:  http.MethodPost,
		URL:     "https://unit.invalid/v1/chat/completions",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{}`),
	}

	keyless := model.Channel{ID: 9001, Type: model.ChannelProviderOpenAI, BaseURL: "https://unit.invalid"}
	request, err := buildPassthroughRequest(llm.APIFormatOpenAIChatCompletion, raw, keyless, "")
	if err != nil {
		t.Fatalf("构造无密钥透传请求失败: %v", err)
	}
	if request.Auth != nil {
		t.Fatal("无密钥渠道透传请求的 Auth 应保持 nil")
	}
	if got := request.Headers.Get("Authorization"); got != "" {
		t.Fatalf("无密钥渠道透传不应携带 Authorization, 实际 %q", got)
	}
	if got := request.Headers.Get("X-Api-Key"); got != "" {
		t.Fatalf("无密钥渠道透传不应携带 X-Api-Key, 实际 %q", got)
	}
	if got := request.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type 应按客户端原值重建, 实际 %q", got)
	}

	keyed := model.Channel{ID: 9002, Type: model.ChannelProviderOpenAI, BaseURL: "https://unit.invalid", Key: "sk-passthrough"}
	request, err = buildPassthroughRequest(llm.APIFormatOpenAIChatCompletion, raw, keyed, "")
	if err != nil {
		t.Fatalf("构造有密钥透传请求失败: %v", err)
	}
	if got := request.Headers.Get("Authorization"); got != "Bearer sk-passthrough" {
		t.Fatalf("有密钥渠道应保留 Bearer 头, 实际 %q", got)
	}

	anthropicKeyed := model.Channel{ID: 9003, Type: model.ChannelProviderAnthropic, BaseURL: "https://unit.invalid", Key: "sk-anthropic"}
	request, err = buildPassthroughRequest(llm.APIFormatAnthropicMessage, raw, anthropicKeyed, "")
	if err != nil {
		t.Fatalf("构造 anthropic 透传请求失败: %v", err)
	}
	if got := request.Headers.Get("Authorization"); got != "" {
		t.Fatalf("anthropic 形态不应写 Authorization, 实际 %q", got)
	}
	if got := request.Headers.Get("X-Api-Key"); got != "sk-anthropic" {
		t.Fatalf("anthropic 形态应写 X-API-Key, 实际 %q", got)
	}
}

// TestKeylessPassthroughEndToEnd 端到端验证无密钥渠道同协议透传:
// 上游收到的请求不得含 Authorization/X-API-Key, 客户端仍正常收到成功响应。
func TestKeylessPassthroughEndToEnd(t *testing.T) {
	setupFailoverTest(t)

	upstream := &capturedUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.record(r)
		writeChatCompletion(t, w, "keyless-passthrough-ok")
	}))
	defer server.Close()

	modelName := integrationUniqueName("it-model-keyless")
	channel := createKeylessChannel(t, "it-keyless-passthrough", server.URL, modelName, model.ChannelProviderOpenAI)
	group := createIntegrationGroup(t, "it-keyless-passthrough-group",
		model.GroupRelayConfig{
			MemberMaxAttempts:                     1,
			MemberRetryIntervalSeconds:            1,
			MemberNonStreamResponseTimeoutSeconds: 5,
			MemberStreamFirstEventTimeoutSeconds:  5,
			MemberCooldownSeconds:                 60,
			CooldownBackoffMultiplier:             2,
			CooldownMaxSeconds:                    120,
		},
		integrationLeafItem(t, channel, modelName))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("无密钥渠道透传应返回 200, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "keyless-passthrough-ok") {
		t.Fatalf("客户端应收到上游回复, 实际: %s", recorder.Body.String())
	}
	authorization, apiKey := upstream.snapshot()
	if len(authorization) != 1 || authorization[0] != "" {
		t.Fatalf("透传上游不应收到 Authorization, 实际 %v", authorization)
	}
	if len(apiKey) != 1 || apiKey[0] != "" {
		t.Fatalf("透传上游不应收到 X-Api-Key, 实际 %v", apiKey)
	}
}

// TestKeylessConversionEndToEnd 端到端验证无密钥渠道跨协议转换:
// 客户端 Anthropic 协议 -> openai 渠道经 pipeline 转换, 占位凭据写入的认证头被钩子删除,
// 上游同样收不到任何认证头; 另以含密钥渠道对照验证带头行为保持不变。
func TestKeylessConversionEndToEnd(t *testing.T) {
	setupFailoverTest(t)

	upstream := &capturedUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.record(r)
		writeChatCompletion(t, w, "keyless-conversion-ok")
	}))
	defer server.Close()

	modelName := integrationUniqueName("it-model-keyless-conv")
	keylessChannel := createKeylessChannel(t, "it-keyless-conversion", server.URL, modelName, model.ChannelProviderOpenAI)
	newGroup := func(channel model.Channel) model.Group {
		return createIntegrationGroup(t, "it-keyless-conversion-group",
			model.GroupRelayConfig{
				MemberMaxAttempts:                     1,
				MemberRetryIntervalSeconds:            1,
				MemberNonStreamResponseTimeoutSeconds: 5,
				MemberStreamFirstEventTimeoutSeconds:  5,
				MemberCooldownSeconds:                 60,
				CooldownBackoffMultiplier:             2,
				CooldownMaxSeconds:                    120,
			},
			integrationLeafItem(t, channel, modelName))
	}

	engine, path := newIntegrationEngine(llm.APIFormatAnthropicMessage)
	body := func(group model.Group) string {
		return fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	}

	keylessGroup := newGroup(keylessChannel)
	recorder := postRelayJSON(t, engine, path, body(keylessGroup), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("无密钥渠道转换应返回 200, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "keyless-conversion-ok") {
		t.Fatalf("客户端应收到转换后的回复, 实际: %s", recorder.Body.String())
	}
	authorization, apiKey := upstream.snapshot()
	if len(authorization) != 1 || authorization[0] != "" {
		t.Fatalf("转换路径上游不应收到 Authorization, 实际 %v", authorization)
	}
	if len(apiKey) != 1 || apiKey[0] != "" {
		t.Fatalf("转换路径上游不应收到 X-Api-Key, 实际 %v", apiKey)
	}

	// 对照组: 同链路的含密钥渠道仍按 Bearer 带头。
	keyedChannel := model.Channel{
		Name:    integrationUniqueName("it-keyed-conversion"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: server.URL,
		Key:     "sk-keyed-conversion",
		Models:  []model.ChannelModel{{Name: modelName, Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&keyedChannel, context.Background()); err != nil {
		t.Fatalf("创建含密钥渠道失败: %v", err)
	}
	keyedGroup := newGroup(keyedChannel)
	recorder = postRelayJSON(t, engine, path, body(keyedGroup), "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("含密钥渠道转换应返回 200, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	authorization, _ = upstream.snapshot()
	if len(authorization) != 2 || authorization[1] != "Bearer sk-keyed-conversion" {
		t.Fatalf("含密钥渠道转换仍应带头, 实际 Authorization 序列 %v", authorization)
	}
}

// TestKeylessAuthRejectionFailsFastAcrossMembers 验证无密钥渠道被上游 401/403 拒绝时:
// 渠道没有任何凭据可轮换, 重试同一成员或等待冷却都不可能改变结果, 故应与确定性
// 400/404/422 一样每成员只试一次、全失败后立即终止并返回统一"暂无可用渠道"
// (重试间隔配 5 秒放大旧行为按 member_max_attempts 空转重试的代价)。
func TestKeylessAuthRejectionFailsFastAcrossMembers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{"401", http.StatusUnauthorized},
		{"403", http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupFailoverTest(t)

			authErrBody := `{"error":{"message":"auth invalid","type":"invalid_request_error"}}`
			var hitsA, hitsB atomic.Int64
			newAuthUpstream := func(counter *atomic.Int64) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					counter.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(authErrBody))
				}))
			}
			serverA := newAuthUpstream(&hitsA)
			defer serverA.Close()
			serverB := newAuthUpstream(&hitsB)
			defer serverB.Close()

			config := refSkipConfig()
			modelNameA := integrationUniqueName("it-keyless-auth-model-a")
			modelNameB := integrationUniqueName("it-keyless-auth-model-b")
			chA := createKeylessChannel(t, "it-keyless-auth-ch-a", serverA.URL, modelNameA, model.ChannelProviderOpenAI)
			chB := createKeylessChannel(t, "it-keyless-auth-ch-b", serverB.URL, modelNameB, model.ChannelProviderOpenAI)
			group := createIntegrationGroup(t, "it-keyless-auth-g", config,
				integrationLeafItem(t, chA, modelNameA),
				integrationLeafItem(t, chB, modelNameB))

			engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)

			expectedID := nextRequestID()
			begin := time.Now()
			recorder := postRelayJSON(t, engine, path, body, "", nil)
			elapsed := time.Since(begin)

			// 每成员只试一次: 不进入 member_max_attempts 重试循环(且 401/403 无 400 那样的清洗重试)。
			if hitsA.Load() != 1 || hitsB.Load() != 1 {
				t.Fatalf("无密钥 %s 应每成员只试一次(不重试), 实际 A=%d B=%d", tt.name, hitsA.Load(), hitsB.Load())
			}
			if elapsed >= 8*time.Second {
				t.Fatalf("无密钥 %s 应快速终止, 实际耗时 %v(疑似冷却-等待循环)", tt.name, elapsed)
			}
			// 下游统一契约: 全部成员不可用只返回 400+"暂无可用渠道", 上游详情仅保留在内部状态。
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("应把统一 400 交还下游, 实际 %d: %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "暂无可用渠道") {
				t.Fatalf("下游应收到统一文案\"暂无可用渠道\", 实际: %s", recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "auth invalid") {
				t.Fatalf("上游错误详情不得泄漏给下游: %s", recorder.Body.String())
			}
			// 整体失败终态 + 每成员各一条 4xx 失败轨迹。
			state := requestStateOf(t, expectedID)
			if state.Status != StatusFailed {
				t.Fatalf("请求应以失败终态结束, 实际 %s(%s)", state.Status, state.Error)
			}
			if len(state.Attempts) != 2 {
				t.Fatalf("应恰好两条尝试轨迹(每成员一次), 实际 %d 条", len(state.Attempts))
			}
			for i, attempt := range state.Attempts {
				if attempt.Outcome != AttemptFailed || attempt.ErrClass != ErrClassUpstream4xx {
					t.Fatalf("第 %d 条轨迹应为 4xx 失败, 实际 %s/%s", i+1, attempt.Outcome, attempt.ErrClass)
				}
			}
		})
	}
}

// TestDeterministic4xxWithDisabledMemberFailsFast 验证确定性 4xx 的"全部成员失败即终止"
// 分母不把禁用成员计入: 分组唯一可用成员被 401 拒绝时, 即使同组还挂着禁用渠道成员,
// 也应立即按统一 400 快速失败, 而非因为 len(group.Items) 虚高而空转烧轮次。
func TestDeterministic4xxWithDisabledMemberFailsFast(t *testing.T) {
	setupFailoverTest(t)

	authErrBody := `{"error":{"message":"auth invalid","type":"invalid_request_error"}}`
	var hits atomic.Int64
	authUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(authErrBody))
	}))
	defer authUpstream.Close()
	var disabledHits atomic.Int64
	disabledUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledHits.Add(1)
		_, _ = w.Write(chatCompletionBody("chatcmpl-disabled", "should-not-serve", 1, 1))
	}))
	defer disabledUpstream.Close()

	config := refSkipConfig()
	modelNameA := integrationUniqueName("it-det4xx-disabled-model-a")
	modelNameB := integrationUniqueName("it-det4xx-disabled-model-b")
	chA := createKeylessChannel(t, "it-det4xx-disabled-ch-a", authUpstream.URL, modelNameA, model.ChannelProviderOpenAI)
	chB := createIntegrationChannel(t, "it-det4xx-disabled-ch-b", model.ChannelProviderOpenAI, disabledUpstream.URL, modelNameB)

	disabled := false
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: chB.ID, Enabled: &disabled}, context.Background()); err != nil {
		t.Fatalf("禁用渠道 %d 失败: %v", chB.ID, err)
	}

	group := createIntegrationGroup(t, "it-det4xx-disabled-g", config,
		integrationLeafItem(t, chA, modelNameA),
		integrationLeafItem(t, chB, modelNameB))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("唯一可用成员确定性 4xx 时应按 400 快速失败(禁用成员不计分母), 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "暂无可用渠道") {
		t.Fatalf("下游应收到统一文案\"暂无可用渠道\", 实际: %s", recorder.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("确定性 4xx 成员应只试一次, 实际 %d 次", got)
	}
	if got := disabledHits.Load(); got != 0 {
		t.Fatalf("禁用渠道不应被调用, 实际 %d 次", got)
	}
	if elapsed >= 6*time.Second {
		t.Fatalf("应快速终止(禁用成员虚高分母会导致空转), 实际耗时 %v", elapsed)
	}
}

// TestDeterministic4xxReferenceDoesNotPrematurelyFail 验证引用场景下确定性 4xx 的
// 分母改数可达叶子后, 引用目标被 401 拒绝不会因为叶子分组的 group.Items 过小而提前 400:
// 顶层低优直连健康成员仍应被尝试并交付。
func TestDeterministic4xxReferenceDoesNotPrematurelyFail(t *testing.T) {
	setupFailoverTest(t)

	authErrBody := `{"error":{"message":"auth invalid","type":"invalid_request_error"}}`
	var subHits, lowHits atomic.Int64
	subUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(authErrBody))
	}))
	defer subUpstream.Close()
	lowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lowHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionBody("chatcmpl-low-ok", "ok-from-low-direct", 2, 2))
	}))
	defer lowUpstream.Close()

	config := refSkipConfig()
	subModel := integrationUniqueName("it-det4xx-ref-sub-model")
	lowModel := integrationUniqueName("it-det4xx-ref-low-model")
	subChannel := createKeylessChannel(t, "it-det4xx-ref-sub-ch", subUpstream.URL, subModel, model.ChannelProviderOpenAI)
	lowChannel := createIntegrationChannel(t, "it-det4xx-ref-low-ch", model.ChannelProviderOpenAI, lowUpstream.URL, lowModel)

	sub := createIntegrationGroup(t, "it-det4xx-ref-sub-g", config, integrationLeafItem(t, subChannel, subModel))
	auto := createIntegrationGroup(t, "it-det4xx-ref-auto-g", config,
		integrationRefItem(sub.Name),
		integrationLeafItem(t, lowChannel, lowModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)
	recorder := postRelayJSON(t, engine, path, body, "", nil)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-from-low-direct") {
		t.Fatalf("引用目标确定性 4xx 时应切换到顶层直连成员而非过早 400, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := subHits.Load(); got != 1 {
		t.Fatalf("引用目标应只试一次(确定性 4xx), 实际 %d 次", got)
	}
	if got := lowHits.Load(); got != 1 {
		t.Fatalf("低优直连成员应恰好承载一次, 实际 %d 次", got)
	}
}

// TestDeterministic4xxAcrossReferenceChainFailsFast 验证跨引用链的全部成员确定性 4xx 时
// 仍按统一 400 终止: 分母沿引用链展开数可达叶子, 引用目标叶子与顶层直连叶子都 401 一次后凑满触发终止。
func TestDeterministic4xxAcrossReferenceChainFailsFast(t *testing.T) {
	setupFailoverTest(t)

	authErrBody := `{"error":{"message":"auth invalid","type":"invalid_request_error"}}`
	var subHits, lowHits atomic.Int64
	newAuthUpstream := func(counter *atomic.Int64, id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(authErrBody))
		}))
	}
	subUpstream := newAuthUpstream(&subHits, "sub")
	defer subUpstream.Close()
	lowUpstream := newAuthUpstream(&lowHits, "low")
	defer lowUpstream.Close()

	config := refSkipConfig()
	subModel := integrationUniqueName("it-det4xx-refall-sub-model")
	lowModel := integrationUniqueName("it-det4xx-refall-low-model")
	subChannel := createKeylessChannel(t, "it-det4xx-refall-sub-ch", subUpstream.URL, subModel, model.ChannelProviderOpenAI)
	lowChannel := createKeylessChannel(t, "it-det4xx-refall-low-ch", lowUpstream.URL, lowModel, model.ChannelProviderOpenAI)

	sub := createIntegrationGroup(t, "it-det4xx-refall-sub-g", config, integrationLeafItem(t, subChannel, subModel))
	auto := createIntegrationGroup(t, "it-det4xx-refall-auto-g", config,
		integrationRefItem(sub.Name),
		integrationLeafItem(t, lowChannel, lowModel))

	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, auto.Name)
	begin := time.Now()
	recorder := postRelayJSON(t, engine, path, body, "", nil)
	elapsed := time.Since(begin)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("跨引用链全部成员确定性 4xx 时仍应按统一 400 拒绝, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := subHits.Load(); got != 1 {
		t.Fatalf("引用目标应只试一次, 实际 %d 次", got)
	}
	if got := lowHits.Load(); got != 1 {
		t.Fatalf("顶层直连成员应只试一次, 实际 %d 次", got)
	}
	if elapsed >= 8*time.Second {
		t.Fatalf("应快速终止而非空转烧轮次, 实际耗时 %v", elapsed)
	}
}
