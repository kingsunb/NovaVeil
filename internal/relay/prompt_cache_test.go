package relay

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/gjson"
)

func TestPromptCacheKeyFromClient(t *testing.T) {
	if got := promptCacheKeyFromClient([]byte(`{"prompt_cache_key":"pk-1"}`)); got != "pk-1" {
		t.Fatalf("字符串应读出, 得到 %q", got)
	}
	spaced := "  pk  "
	if got := promptCacheKeyFromClient([]byte(`{"prompt_cache_key":"  pk  "}`)); got != spaced {
		t.Fatalf("不应 trim, 得到 %q", got)
	}
	exact := strings.Repeat("a", maxPromptCacheKeyBytes)
	if got := promptCacheKeyFromClient([]byte(`{"prompt_cache_key":"` + exact + `"}`)); got != exact {
		t.Fatalf("256 字节应保留, 长度 %d", len(got))
	}
	tooLong := strings.Repeat("b", maxPromptCacheKeyBytes+1)
	if got := promptCacheKeyFromClient([]byte(`{"prompt_cache_key":"` + tooLong + `"}`)); got != "" {
		t.Fatal("超过 256 字节应忽略")
	}
	runes := strings.Repeat("你", 86) // 258 字节
	if got := promptCacheKeyFromClient([]byte(`{"prompt_cache_key":"` + runes + `"}`)); got != "" {
		t.Fatalf("应按字节计长, 得到长度 %d", len(got))
	}
	for _, body := range []string{
		`{}`,
		`{"prompt_cache_key":""}`,
		`{"prompt_cache_key":1}`,
		`{"prompt_cache_key":null}`,
		`{"prompt_cache_key":{"k":"v"}}`,
		`{"prompt_cache_key":["a"]}`,
		`not-json`,
	} {
		if got := promptCacheKeyFromClient([]byte(body)); got != "" {
			t.Fatalf("body %s 应忽略, 得到 %q", body, got)
		}
	}
}

func TestRestorePromptCacheKeyTargets(t *testing.T) {
	key := "pk-placeholder"
	chat := restorePromptCacheKey([]byte(`{"model":"m"}`), key, llm.APIFormatOpenAIChatCompletion)
	if gjson.GetBytes(chat, "prompt_cache_key").String() != key {
		t.Fatalf("Chat 应写回, body=%s", chat)
	}
	responsesBody := restorePromptCacheKey([]byte(`{"model":"m"}`), key, llm.APIFormatOpenAIResponse)
	if gjson.GetBytes(responsesBody, "prompt_cache_key").String() != key {
		t.Fatalf("Responses 应写回, body=%s", responsesBody)
	}
	anthropicBody := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	anthropicOut := restorePromptCacheKey(anthropicBody, key, llm.APIFormatAnthropicMessage)
	if !bytes.Equal(anthropicOut, anthropicBody) {
		t.Fatalf("Anthropic 不应改写正文, 得到 %s", anthropicOut)
	}
	if bytes.Contains(anthropicOut, []byte("prompt_cache_key")) || bytes.Contains(anthropicOut, []byte("cache_control")) {
		t.Fatalf("Anthropic 不应新增 prompt_cache_key 或 cache_control, body=%s", anthropicOut)
	}
	kept := restorePromptCacheKey([]byte(`{"prompt_cache_key":"already"}`), key, llm.APIFormatOpenAIChatCompletion)
	if gjson.GetBytes(kept, "prompt_cache_key").String() != "already" {
		t.Fatalf("已有字段不得覆盖, body=%s", kept)
	}
	quoted := restorePromptCacheKey([]byte(`{"model":"m"}`), `a"b\c`, llm.APIFormatOpenAIChatCompletion)
	if gjson.GetBytes(quoted, "prompt_cache_key").String() != `a"b\c` {
		t.Fatalf("应原样回写字符串, 得到 %q", gjson.GetBytes(quoted, "prompt_cache_key").String())
	}
}

func TestOnOutboundRestoresMaskedPromptCacheKey(t *testing.T) {
	// 脱敏后的占位符才是允许上送的值。原文不得从别处写回。
	const masked = "TKN_1"
	const original = "super-secret-key"
	client := []byte(`{"model":"m","prompt_cache_key":"` + masked + `","messages":[{"role":"user","content":"hi"}]}`)
	if got := promptCacheKeyFromClient(client); got != masked || got == original {
		t.Fatalf("应读到脱敏后的值, 得到 %q", got)
	}

	req := &httpclient.Request{
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"prompt_cache_key":"transformer-key"}`),
		APIFormat: llm.APIFormatOpenAIChatCompletion.String(),
		JSONBody:  []byte(`{"logged":true}`),
	}
	middleware := &conversionMiddleware{
		channel:        model.Channel{ID: 1, Type: model.ChannelProviderOpenAI},
		format:         llm.APIFormatOpenAIChatCompletion,
		randomValue:    "rv",
		promptCacheKey: promptCacheKeyFromClient(client),
	}
	out, err := middleware.OnOutboundRawRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("OnOutboundRawRequest: %v", err)
	}
	if got := gjson.GetBytes(out.Body, "prompt_cache_key").String(); got != masked {
		t.Fatalf("Chat 归一批掉字段后应写回脱敏值, 得到 %q, body=%s", got, out.Body)
	}
	if bytes.Contains(out.Body, []byte(original)) {
		t.Fatal("上游正文不得出现脱敏前原文")
	}
	if bytes.Contains(out.JSONBody, []byte(masked)) || bytes.Contains(out.JSONBody, []byte(original)) {
		t.Fatalf("JSONBody 日志副本不得带上 prompt_cache_key, json=%s", out.JSONBody)
	}
}

func TestOnOutboundKeepsExistingPromptCacheKey(t *testing.T) {
	req := &httpclient.Request{
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"already"}`),
		APIFormat: llm.APIFormatOpenAIChatCompletion.String(),
	}
	middleware := &conversionMiddleware{
		channel:        model.Channel{ID: 1, Type: model.ChannelProviderOpenAI},
		format:         llm.APIFormatOpenAIChatCompletion,
		promptCacheKey: "client-key",
	}
	out, err := middleware.OnOutboundRawRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("OnOutboundRawRequest: %v", err)
	}
	if got := gjson.GetBytes(out.Body, "prompt_cache_key").String(); got != "already" {
		t.Fatalf("出站已有字段应保留, 得到 %q", got)
	}
}

func TestOnOutboundSkipsAnthropicPromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req := &httpclient.Request{
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      body,
		APIFormat: llm.APIFormatAnthropicMessage.String(),
	}
	middleware := &conversionMiddleware{
		channel:        model.Channel{ID: 1, Type: model.ChannelProviderAnthropic},
		format:         llm.APIFormatAnthropicMessage,
		promptCacheKey: "client-key",
	}
	out, err := middleware.OnOutboundRawRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("OnOutboundRawRequest: %v", err)
	}
	if gjson.GetBytes(out.Body, "prompt_cache_key").Exists() {
		t.Fatalf("Anthropic 出站不得新增 prompt_cache_key, body=%s", out.Body)
	}
	if bytes.Contains(out.Body, []byte("client-key")) || bytes.Contains(out.Body, []byte("cache_control")) {
		t.Fatalf("不得把 prompt_cache_key 映射进 Anthropic 正文, body=%s", out.Body)
	}
}

func TestPassthroughKeepsPromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"m","prompt_cache_key":"keep-me","messages":[{"role":"user","content":"hi"}]}`)
	raw := &httpclient.Request{
		Method:  http.MethodPost,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body,
	}
	channel := model.Channel{
		ID:             77,
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://unit.invalid",
		Key:            "sk-test",
		OpencodeCompat: true,
	}
	request, err := buildPassthroughRequest(llm.APIFormatOpenAIChatCompletion, raw, channel, "trace-value", validSesA)
	if err != nil {
		t.Fatalf("buildPassthroughRequest: %v", err)
	}
	if !bytes.Equal(request.Body, body) {
		t.Fatalf("同协议透传不得改 prompt_cache_key, body=%s", request.Body)
	}
	if got := request.Headers.Get(opencodeSessionHeader); got != validSesA {
		t.Fatalf("透传仍应单独上送会话头, 得到 %q", got)
	}
	if got := request.Headers.Get("x-trace-id"); got != "" && got == validSesA {
		t.Fatal("未配置的动态头不应被塞进会话号")
	}
}

func TestChatToResponsesAndAnthropicPromptCacheKey(t *testing.T) {
	ctx := context.Background()
	clientBody := []byte(`{"model":"gpt-4o","prompt_cache_key":"client-key","messages":[{"role":"user","content":"hi"}]}`)
	raw := &httpclient.Request{
		Method:    http.MethodPost,
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      clientBody,
		APIFormat: llm.APIFormatOpenAIChatCompletion.String(),
	}
	llmReq, err := openai.NewInboundTransformer().TransformRequest(ctx, raw)
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	key := promptCacheKeyFromClient(clientBody)

	responsesOutbound, err := responses.NewOutboundTransformer("https://unit.invalid", "sk-test")
	if err != nil {
		t.Fatalf("responses outbound: %v", err)
	}
	converted, err := responsesOutbound.TransformRequest(ctx, llmReq)
	if err != nil {
		t.Fatalf("responses transform: %v", err)
	}
	responsesMiddleware := &conversionMiddleware{
		channel:        model.Channel{ID: 3, Type: model.ChannelProviderOpenAIResponses, BaseURL: "https://unit.invalid", Key: "sk-test"},
		format:         llm.APIFormatOpenAIResponse,
		randomValue:    "rv",
		promptCacheKey: key,
	}
	responsesOut, err := responsesMiddleware.OnOutboundRawRequest(ctx, converted)
	if err != nil {
		t.Fatalf("responses hook: %v", err)
	}
	if got := gjson.GetBytes(responsesOut.Body, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("Chat→Responses 应保留客户端 prompt_cache_key, 得到 %q, body=%s", got, responsesOut.Body)
	}

	anthropicOutbound, err := anthropic.NewOutboundTransformer("https://unit.invalid", "sk-test")
	if err != nil {
		t.Fatalf("anthropic outbound: %v", err)
	}
	anthropicReq, err := anthropicOutbound.TransformRequest(ctx, llmReq)
	if err != nil {
		t.Fatalf("anthropic transform: %v", err)
	}
	anthropicMiddleware := &conversionMiddleware{
		channel:        model.Channel{ID: 4, Type: model.ChannelProviderAnthropic, BaseURL: "https://unit.invalid", Key: "sk-test"},
		format:         llm.APIFormatAnthropicMessage,
		randomValue:    "rv",
		promptCacheKey: key,
	}
	anthropicOut, err := anthropicMiddleware.OnOutboundRawRequest(ctx, anthropicReq)
	if err != nil {
		t.Fatalf("anthropic hook: %v", err)
	}
	if gjson.GetBytes(anthropicOut.Body, "prompt_cache_key").Exists() || bytes.Contains(anthropicOut.Body, []byte("client-key")) {
		t.Fatalf("Chat→Anthropic 不得新增 prompt_cache_key, body=%s", anthropicOut.Body)
	}
}
