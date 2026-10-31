package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
)

type ocCapturedRequest struct {
	body []byte
	path string
}

type ocTestUpstream struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []ocCapturedRequest
}

func (u *ocTestUpstream) lastBody(t *testing.T) []byte {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		t.Fatal("上游未收到请求")
	}
	return append([]byte(nil), u.requests[len(u.requests)-1].body...)
}

func newOCTestUpstream(t *testing.T, respond func(body []byte) string) *ocTestUpstream {
	t.Helper()
	u := &ocTestUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readAllBody(r)
		if err != nil {
			t.Errorf("读取上游请求失败: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		u.mu.Lock()
		u.requests = append(u.requests, ocCapturedRequest{body: body, path: r.URL.Path})
		u.mu.Unlock()
		payload := respond(body)
		if strings.HasPrefix(payload, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func readAllBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ocJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func ocChatSSE(text, toolName, toolArgs string) string {
	delta := `"role":"assistant"`
	if text != "" {
		delta += `,"content":` + ocJSON(text)
	}
	if toolName != "" {
		delta += `,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":` +
			ocJSON(toolName) + `,"arguments":` + ocJSON(toolArgs) + `}}]`
	}
	first := fmt.Sprintf(
		`data: {"id":"chatcmpl-oc","object":"chat.completion.chunk","created":1,"model":"x-free","choices":[{"index":0,"delta":{%s},"finish_reason":null}]}`,
		delta,
	)
	last := `data: {"id":"chatcmpl-oc","object":"chat.completion.chunk","created":1,"model":"x-free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
	return first + "\n\n" + last + "\n\n" + "data: [DONE]\n\n"
}

var ocIdentityAliasPattern = regexp.MustCompile(`opencode_[0-9a-f]{32}`)

func ocIdentityAlias(body []byte) (string, bool) {
	match := ocIdentityAliasPattern.FindSubmatch(body)
	if match == nil {
		return "", false
	}
	return string(match[0]), true
}

func ocMustIdentityAlias(t *testing.T, body []byte) string {
	t.Helper()
	alias, ok := ocIdentityAlias(body)
	if !ok {
		t.Fatalf("上游请求缺少身份别名: %s", body)
	}
	return alias
}

func ocDrainStream(t *testing.T, resp *upstreamResponse) []byte {
	t.Helper()
	defer resp.Close()
	parts := make([][]byte, 0, len(resp.window)+8)
	for _, event := range resp.window {
		if event != nil && len(event.Data) > 0 {
			parts = append(parts, event.Data)
		}
	}
	if !resp.last && resp.events != nil {
		for resp.events.Next() {
			if event := resp.events.Current(); event != nil && len(event.Data) > 0 {
				parts = append(parts, event.Data)
			}
		}
		if err := resp.events.Err(); err != nil {
			t.Fatalf("读取上游事件流失败: %v", err)
		}
	}
	return bytes.Join(parts, []byte("\n"))
}

func ocRawRequest(format llm.APIFormat, body []byte) *httpclient.Request {
	return &httpclient.Request{
		Method:    http.MethodPost,
		URL:       "http://gateway.local" + ocClientPath(format),
		Headers:   http.Header{"Content-Type": []string{"application/json"}},
		Body:      body,
		APIFormat: format.String(),
	}
}

func ocClientPath(format llm.APIFormat) string {
	switch format {
	case llm.APIFormatOpenAIResponse:
		return "/v1/responses"
	case llm.APIFormatAnthropicMessage:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

func ocFreePassthroughChannel(serverURL, lane string) model.Channel {
	return model.Channel{
		ID:           1,
		Name:         "oc-passthrough",
		Type:         model.ChannelProviderOpenAI,
		BaseURL:      serverURL + "/zen",
		Key:          "test-key",
		IsFree:       true,
		OpencodeLane: lane,
	}
}

func ocFreeConvertedChannel(proxyURL, lane string) model.Channel {
	proxy := proxyURL
	return model.Channel{
		ID:           2,
		Name:         "oc-converted",
		Type:         model.ChannelProviderOpenAI,
		BaseURL:      "http://opencode.invalid/zen",
		Key:          "test-key",
		IsFree:       true,
		OpencodeLane: lane,
		Proxy:        true,
		ChannelProxy: &proxy,
	}
}

func ocChatClientBody(stream bool) []byte {
	return []byte(`{"model":"x-free","stream":` + fmt.Sprintf("%t", stream) +
		`,"messages":[{"role":"system","content":"opencode helper"},{"role":"user","content":"opencode user"}],"tools":[` +
		ocContractClientTool(model.UpstreamProtocolChat, "ReadFile") + `]}`)
}

func ocResponsesClientBody(stream bool) []byte {
	return []byte(`{"model":"x-free","stream":` + fmt.Sprintf("%t", stream) +
		`,"instructions":"opencode helper","input":[{"role":"user","content":[{"type":"input_text","text":"opencode user"}]}],"tools":[` +
		ocContractClientTool(model.UpstreamProtocolResponses, "ReadFile") + `]}`)
}

func ocAnthropicClientBody(stream bool) []byte {
	return []byte(`{"model":"x-free","max_tokens":1024,"stream":` + fmt.Sprintf("%t", stream) +
		`,"system":"opencode helper","messages":[{"role":"user","content":"opencode user"}],"tools":[` +
		ocContractClientTool(model.UpstreamProtocolAnthropic, "ReadFile") + `]}`)
}

func TestOpencodePassthroughChatKeepsClientContract(t *testing.T) {
	for _, lane := range []string{"v1", "v2"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", lane, stream), func(t *testing.T) {
				upstream := newOCTestUpstream(t, func(body []byte) string {
					alias, ok := ocIdentityAlias(body)
					if !ok {
						t.Errorf("上游请求缺少身份别名: %s", body)
					}
					return ocChatSSE("identity:"+alias, ocExpectWireName("ReadFile"), `{"diff":"client"}`)
				})
				channel := ocFreePassthroughChannel(upstream.server.URL, lane)
				body := ocChatClientBody(stream)
				original := append([]byte(nil), body...)
				format := llm.APIFormatOpenAIChatCompletion
				outbound, _, err := buildOutbound(channel, &model.ChannelModel{Name: "x-free"}, format)
				if err != nil {
					t.Fatalf("构造出站转换器失败: %v", err)
				}
				resp, err := sendPassthrough(context.Background(), format, ocRawRequest(format, body), channel, outbound, stream, "rand")
				if err != nil {
					t.Fatalf("透传请求失败: %v", err)
				}
				captured := upstream.lastBody(t)
				alias := ocMustIdentityAlias(t, captured)
				if !bytes.Contains(captured, []byte(alias+" helper")) {
					t.Fatalf("系统提示词未替换为别名: %s", captured)
				}
				if !bytes.Contains(captured, []byte("opencode user")) {
					t.Fatalf("用户正文不得改写: %s", captured)
				}
				wire := ocExpectWireName("ReadFile")
				if !bytes.Contains(captured, []byte(`"name":"`+wire+`"`)) {
					t.Fatalf("客户端工具未改名为摘要: %s", captured)
				}
				if bytes.Contains(captured, []byte(`"name":"ReadFile"`)) {
					t.Fatalf("出站正文不得保留客户端工具原名: %s", captured)
				}
				if !bytes.Contains(captured, []byte("9007199254740993")) {
					t.Fatalf("客户端工具参数定义丢失大整数: %s", captured)
				}
				if !json.Valid(captured) {
					t.Fatalf("出站正文不是合法 JSON: %s", captured)
				}
				if stream {
					delivered := ocDrainStream(t, resp)
					if !bytes.Contains(delivered, []byte(`"name":"ReadFile"`)) {
						t.Fatalf("流式响应未还原客户端工具名: %s", delivered)
					}
					if bytes.Contains(delivered, []byte(wire)) {
						t.Fatalf("流式响应不得泄露摘要工具名: %s", delivered)
					}
					if !bytes.Contains(delivered, []byte("identity:opencode")) {
						t.Fatalf("流式响应未还原身份别名: %s", delivered)
					}
				} else {
					defer resp.Close()
					if name := gjson.GetBytes(resp.body, "choices.0.message.tool_calls.0.function.name").String(); name != "ReadFile" {
						t.Fatalf("折叠响应未还原客户端工具名: %s", resp.body)
					}
					if content := gjson.GetBytes(resp.body, "choices.0.message.content").String(); content != "identity:opencode" {
						t.Fatalf("折叠响应未还原身份别名: %q", content)
					}
				}
				if !bytes.Equal(body, original) {
					t.Fatal("sendPassthrough 不得改写调用方请求正文")
				}
			})
		}
	}
}

func TestOpencodePassthroughNonFreeLeavesRequestUntouched(t *testing.T) {
	upstream := newOCTestUpstream(t, func([]byte) string {
		return `{"id":"chatcmpl-oc","object":"chat.completion","created":1,"model":"x-free","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`
	})
	channel := model.Channel{
		ID:      3,
		Name:    "oc-paid",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: upstream.server.URL + "/v1",
		Key:     "test-key",
	}
	body := ocChatClientBody(false)
	original := append([]byte(nil), body...)
	format := llm.APIFormatOpenAIChatCompletion
	outbound, _, err := buildOutbound(channel, &model.ChannelModel{Name: "x-free"}, format)
	if err != nil {
		t.Fatalf("构造出站转换器失败: %v", err)
	}
	resp, err := sendPassthrough(context.Background(), format, ocRawRequest(format, body), channel, outbound, false, "rand")
	if err != nil {
		t.Fatalf("透传请求失败: %v", err)
	}
	defer resp.Close()
	if captured := upstream.lastBody(t); !bytes.Equal(captured, original) {
		t.Fatalf("非免费档请求正文被改写: %s", captured)
	}
	if gjson.GetBytes(resp.body, "choices.0.message.content").String() != "ok" {
		t.Fatalf("非免费档响应被改写: %s", resp.body)
	}
}

func TestOpencodeConvertedKeepsClientContract(t *testing.T) {
	clients := map[string]struct {
		format llm.APIFormat
		body   func(bool) []byte
	}{
		"responses": {format: llm.APIFormatOpenAIResponse, body: ocResponsesClientBody},
		"anthropic": {format: llm.APIFormatAnthropicMessage, body: ocAnthropicClientBody},
	}
	for name, client := range clients {
		for _, lane := range []string{"v1", "v2"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", name, lane, stream), func(t *testing.T) {
					upstream := newOCTestUpstream(t, func(body []byte) string {
						alias, ok := ocIdentityAlias(body)
						if !ok {
							t.Errorf("上游请求缺少身份别名: %s", body)
						}
						return ocChatSSE("identity:"+alias, ocExpectWireName("ReadFile"), `{"diff":"client"}`)
					})
					channel := ocFreeConvertedChannel(upstream.server.URL, lane)
					body := client.body(stream)
					original := append([]byte(nil), body...)
					outbound, _, err := buildOutbound(channel, &model.ChannelModel{Name: "x-free"}, client.format)
					if err != nil {
						t.Fatalf("构造出站转换器失败: %v", err)
					}
					resp, err := sendConverted(context.Background(), client.format, ocRawRequest(client.format, body), channel, outbound, stream, "rand")
					if err != nil {
						t.Fatalf("跨协议请求失败: %v", err)
					}
					captured := upstream.lastBody(t)
					alias := ocMustIdentityAlias(t, captured)
					if !bytes.Contains(captured, []byte(alias+" helper")) {
						t.Fatalf("客户端提示词未替换为别名: %s", captured)
					}
					if !bytes.Contains(captured, []byte("opencode user")) {
						t.Fatalf("用户正文不得改写: %s", captured)
					}
					if !bytes.Contains(captured, []byte("9007199254740993")) {
						t.Fatalf("转换后客户端工具参数定义丢失大整数: %s", captured)
					}
					wire := ocExpectWireName("ReadFile")
					if !bytes.Contains(captured, []byte(`"name":"`+wire+`"`)) {
						t.Fatalf("客户端工具未改名为摘要: %s", captured)
					}
					if bytes.Contains(captured, []byte(`"name":"ReadFile"`)) {
						t.Fatalf("出站正文不得保留客户端工具原名: %s", captured)
					}
					if !json.Valid(captured) {
						t.Fatalf("出站正文不是合法 JSON: %s", captured)
					}
					var delivered []byte
					if stream {
						delivered = ocDrainStream(t, resp)
					} else {
						defer resp.Close()
						delivered = resp.body
					}
					if !bytes.Contains(delivered, []byte(`"ReadFile"`)) {
						t.Fatalf("客户端响应未还原工具名: %s", delivered)
					}
					if bytes.Contains(delivered, []byte(wire)) {
						t.Fatalf("客户端响应不得泄露摘要工具名: %s", delivered)
					}
					if !bytes.Contains(delivered, []byte("identity:opencode")) {
						t.Fatalf("客户端响应未还原身份别名: %s", delivered)
					}
					if !bytes.Equal(body, original) {
						t.Fatal("sendConverted 不得改写调用方请求正文")
					}
				})
			}
		}
	}
}
