package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
)

// TestIsOpencodeFreeModel 验证免费档判定的三层门: Zen 档 + (内置免费渠道 或名字含 free 子串)。
func TestIsOpencodeFreeModel(t *testing.T) {
	cases := []struct {
		name      string
		baseURL   string
		isFree    bool
		modelName string
		want      bool
	}{
		{"内置免费渠道 zen", "https://opencode.ai/zen", true, "gpt-5.3-codex", true},
		{"zen 上 -free 后缀模型名", "https://opencode.ai/zen", false, "deepseek-v4-flash-free", true},
		{"zen 上名字中部含 free", "https://opencode.ai/zen", false, "gpt-5.3-codex-free-turbo", true},
		{"zen 上 free 前缀模型名", "https://opencode.ai/zen", false, "free-gpt-5.3-codex", true},
		{"zen 上非 free 模型名且非免费渠道", "https://opencode.ai/zen", false, "gpt-5.3-codex", false},
		{"go 档不判定免费", "https://opencode.ai/zen/go", true, "gpt-5.3-codex-free", false},
		{"非 opencode 渠道不判定免费", "https://api.openai.com/v1", true, "gpt-x-free", false},
		{"空 baseURL", "", true, "x-free", false},
		// 新端点: /inference(免费) 和 /zen/v1(付费)。
		{"内置免费渠道 inference", "https://opencode.ai/inference", true, "gpt-5.3-codex", true},
		{"inference 上 free 模型名", "https://opencode.ai/inference", false, "deepseek-v4-flash-free", true},
		{"inference 上非 free 模型名且非免费渠道", "https://opencode.ai/inference", false, "gpt-5.3-codex", false},
		{"zen/v1 付费档不判定免费", "https://opencode.ai/zen/v1", true, "gpt-5.3-codex-free", false},
		{"zen/v1 付费档 free 模型名也不改写", "https://opencode.ai/zen/v1", false, "deepseek-v4-flash-free", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channel := model.Channel{BaseURL: tc.baseURL, IsFree: tc.isFree}
			if got := isOpencodeFreeModel(channel, tc.modelName); got != tc.want {
				t.Fatalf("isOpencodeFreeModel(%q, model=%q) = %v, 期望 %v", tc.baseURL, tc.modelName, got, tc.want)
			}
		})
	}
}

// TestShapeOpencodeFreeBodyForcesAgentShape 验证非流式无工具正文被规范成 Agent 形态:
// stream=true、include_usage=true、注入全部五个核心工具。
func TestShapeOpencodeFreeBodyForcesAgentShape(t *testing.T) {
	body, changed, _ := shapeOpencodeFreeBody([]byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`), false, model.UpstreamProtocolChat)
	if !changed {
		t.Fatalf("应判定发生改写")
	}
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("应强制 stream=true")
	}
	if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("应补齐 stream_options.include_usage=true")
	}
	var names []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if len(names) != len(opencodeFreeCoreTools) {
		t.Fatalf("应注入 %d 个核心工具, 实际 %d: %v", len(opencodeFreeCoreTools), len(names), names)
	}
	for _, core := range opencodeFreeCoreTools {
		found := false
		for _, n := range names {
			if n == core {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺失核心工具 %q: %v", core, names)
		}
	}
}

// TestShapeOpencodeFreeBodyIdempotent 验证已满足 Agent 形态(真实工具定义)的请求不再改写。
func TestShapeOpencodeFreeBodyIdempotent(t *testing.T) {
	body := []byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":32000,"tools":[` +
		func() string {
			var parts []string
			for _, core := range opencodeFreeCoreTools {
				parts = append(parts, opencodeFreeTool(core, model.UpstreamProtocolChat))
			}
			return strings.Join(parts, ",")
		}() + `]}`)
	_, changed, _ := shapeOpencodeFreeBody(body, false, model.UpstreamProtocolChat)
	if changed {
		t.Fatalf("已完备的 Agent 形态应无改写")
	}
}

// TestShapeOpencodeFreeBodyReplacesSameNameTools 验证客户端声明的同名核心工具被替换为真实定义。
func TestShapeOpencodeFreeBodyReplacesSameNameTools(t *testing.T) {
	body, changed, _ := shapeOpencodeFreeBody([]byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"shell","description":"my custom","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}]}`), false, model.UpstreamProtocolChat)
	if !changed {
		t.Fatalf("同名核心工具应被替换为真实定义")
	}
	desc := gjson.GetBytes(body, "tools.0.function.description").String()
	if desc == "my custom" {
		t.Fatalf("客户端自定义 description 应被真实定义替换, 仍为 custom")
	}
	if desc == "" {
		t.Fatalf("替换后 description 不应为空")
	}
}

// TestShapeOpencodeFreeBodyMergesMissingTools 验证非核心工具原样保留、缺失核心工具被追加。
func TestShapeOpencodeFreeBodyMergesMissingTools(t *testing.T) {
	body, _, _ := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":[{"type":"function","function":{"name":"mytool","description":"custom","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}]}`), false, model.UpstreamProtocolChat)
	raw := gjson.GetBytes(body, "tools").Raw
	// 非核心工具 mytool 字节完全保留。
	if !strings.Contains(raw, `"description":"custom"`) {
		t.Fatalf("非核心工具应原样保留: %s", raw)
	}
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names[tool.Get("function.name").String()] = true
	}
	for _, core := range opencodeFreeCoreTools {
		if !names[core] {
			t.Fatalf("缺失核心工具 %q: %v", core, names)
		}
	}
}

// TestShapeOpencodeFreeBodyNonArrayToolsUntouched 验证 tools 非数组(客户端异常)时不改写。
func TestShapeOpencodeFreeBodyNonArrayToolsUntouched(t *testing.T) {
	body, changed, _ := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":"nope"}`), false, model.UpstreamProtocolChat)
	if gjson.GetBytes(body, "tools").String() != "nope" {
		t.Fatalf("tools 非数组时不应篡改")
	}
	// stream/include_usage 仍属规范步骤, 与工具注入互不牵连; 这里只确保未注入工具覆盖原值。
	if !strings.Contains(string(body), `"tools":"nope"`) {
		t.Fatalf("原 tools 值应保留: %s", body)
	}
	_ = changed
}

// TestShapeOpencodeFreeBodyV1ReadTool 验证 v1 档只保证单个 read 工具, 且不改名客户端工具。
func TestShapeOpencodeFreeBodyV1ReadTool(t *testing.T) {
	t.Run("无工具时补 read 占位", func(t *testing.T) {
		body, changed, _ := shapeOpencodeFreeBody([]byte(`{"model":"x-free","messages":[]}`), true, model.UpstreamProtocolChat)
		if !changed {
			t.Fatalf("v1 档缺工具时应注入 read")
		}
		tools := gjson.GetBytes(body, "tools").Array()
		if len(tools) != 1 || tools[0].Get("function.name").String() != v1ReadToolName {
			t.Fatalf("应只注入单个 %q, 实际 %s", v1ReadToolName, gjson.GetBytes(body, "tools").Raw)
		}
	})

	t.Run("已有 read 时不动", func(t *testing.T) {
		body := []byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"read","description":"client","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`)
		got, changed, _ := shapeOpencodeFreeBody(body, true, model.UpstreamProtocolChat)
		if changed {
			t.Fatalf("已有 read 时不应改写: %s", got)
		}
		if !strings.Contains(string(got), `"description":"client"`) {
			t.Fatalf("客户端 read 定义应原样保留: %s", got)
		}
	})

	t.Run("read 变体改名并补占位", func(t *testing.T) {
		body, _, alias := shapeOpencodeFreeBody([]byte(`{"model":"x-free","tools":[{"type":"function","function":{"name":"ReadFile","description":"client","parameters":{"type":"object","properties":{}}}}]}`), true, model.UpstreamProtocolChat)
		if alias != "ReadFile" {
			t.Fatalf("应返回别名 ReadFile, 实际 %q", alias)
		}
		raw := gjson.GetBytes(body, "tools").Raw
		if strings.Contains(raw, `"name":"ReadFile"`) {
			t.Fatalf("ReadFile 应被改名为 read: %s", raw)
		}
		readCount := 0
		for _, tool := range gjson.GetBytes(body, "tools").Array() {
			if tool.Get("function.name").String() == v1ReadToolName {
				readCount++
			}
		}
		if readCount < 1 {
			t.Fatalf("应存在 read 工具: %s", raw)
		}
	})
}

// TestShapeOpencodeFreeBodyV1ChatKeyOrder 对齐官方 1.19.4 实抓键序:
// [model, max_tokens, messages, tools, tool_choice, stream, stream_options], 多余键按字母序殿后。
func TestShapeOpencodeFreeBodyV1ChatKeyOrder(t *testing.T) {
	body := []byte(`{"stream":true,"stream_options":{"include_usage":true},"model":"x-free","tools":[{"type":"function","function":{"name":"read","description":"client","parameters":{"type":"object","properties":{}}}}],"max_tokens":1024,"tool_choice":"auto","temperature":0.7}`)
	out, _, _ := shapeOpencodeFreeBody(body, true, model.UpstreamProtocolChat)
	want := []string{"model", "max_tokens", "messages", "tools", "tool_choice", "stream", "stream_options", "temperature"}
	if got := topLevelKeys(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 键序 = %v, 期望 %v", got, want)
	}

	out, _, _ = shapeOpencodeFreeBody([]byte(`{"model":"x-free","stream":true,"messages":[]}`), true, model.UpstreamProtocolChat)
	want = []string{"model", "messages", "tools", "stream"}
	if got := topLevelKeys(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 缺省键序 = %v, 期望 %v", got, want)
	}
}

// topLevelKeys 按出现顺序返回 JSON 顶层键。
func topLevelKeys(t *testing.T, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil {
		t.Fatalf("decode body: %v", err)
	} else if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		t.Fatalf("body 非对象: %v", tok)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode key: %v", err)
		}
		keys = append(keys, tok.(string))
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("decode value: %v", err)
		}
	}
	return keys
}

func TestOpencodeToolsIncludePatchDefinition(t *testing.T) {
	def, ok := knownOpencodeTool("patch")
	if !ok {
		t.Fatal("knownOpencodeTool 应识别 patch")
	}
	if !strings.Contains(def.Description, "patchText") && !strings.Contains(def.Description, "patch") {
		t.Fatalf("patch description 异常: %q", def.Description)
	}
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("patch parameters.properties 缺失: %#v", def.Parameters)
	}
	if _, ok := properties["patchText"]; !ok {
		t.Fatalf("patch 缺少 patchText 参数: %#v", properties)
	}
	for _, core := range opencodeFreeCoreTools {
		if core == "patch" {
			t.Fatal("patch 不应加入 opencodeFreeCoreTools")
		}
	}
}

func TestShapeOpencodeFreeBodyCorrectsClientPatch(t *testing.T) {
	def, ok := knownOpencodeTool("patch")
	if !ok {
		t.Fatal("knownOpencodeTool 应识别 patch")
	}
	body := []byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"patch","description":` + quoteJSON(t, def.Description) + `,"parameters":{"type":"object","properties":{"diff":{"type":"string"}}}}}]}`)
	got, changed, _ := shapeOpencodeFreeBody(body, false, model.UpstreamProtocolChat)
	if !changed {
		t.Fatal("客户端 patch 定义不一致时应替换")
	}
	var parameters map[string]any
	if err := json.Unmarshal([]byte(gjson.GetBytes(got, "tools.0.function.parameters").Raw), &parameters); err != nil {
		t.Fatalf("解析 patch parameters: %v", err)
	}
	if !reflect.DeepEqual(parameters, def.Parameters) {
		t.Fatalf("patch parameters 未按真实定义校正: %#v", parameters)
	}
}

func TestShapeOpencodeFreeBodyKeepsExactPatch(t *testing.T) {
	tools := make([]string, 0, len(opencodeFreeCoreTools)+1)
	for _, core := range opencodeFreeCoreTools {
		tools = append(tools, opencodeFreeTool(core, model.UpstreamProtocolChat))
	}
	tools = append(tools, opencodeFreeTool("patch", model.UpstreamProtocolChat))
	body := []byte(`{"model":"x-free","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":32000,"tools":[` + strings.Join(tools, ",") + `]}`)
	if got, changed, _ := shapeOpencodeFreeBody(body, false, model.UpstreamProtocolChat); changed {
		t.Fatalf("真实 patch 定义不应改写: %s", got)
	}
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(encoded)
}

func TestShapeOpencodeFreeBodyV2CompletionLimit(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		v1Lane    bool
		wantLimit bool
		wantValue int64
	}{
		{
			name:      "v2 无上限时补默认值",
			body:      `{"model":"x-free","messages":[]}`,
			wantLimit: true,
			wantValue: 32000,
		},
		{
			name: "显式 max_tokens=0 不覆盖",
			body: `{"model":"x-free","max_tokens":0,"messages":[]}`,
		},
		{
			name:      "显式 max_completion_tokens=0 不覆盖",
			body:      `{"model":"x-free","max_completion_tokens":0,"messages":[]}`,
			wantLimit: true,
			wantValue: 0,
		},
		{
			name:   "v1 不补",
			body:   `{"model":"x-free","messages":[]}`,
			v1Lane: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _, _ := shapeOpencodeFreeBody([]byte(tc.body), tc.v1Lane, model.UpstreamProtocolChat)
			limit := gjson.GetBytes(body, "max_completion_tokens")
			if limit.Exists() != tc.wantLimit {
				t.Fatalf("max_completion_tokens exists = %v, 期望 %v: %s", limit.Exists(), tc.wantLimit, body)
			}
			if tc.wantLimit && limit.Int() != tc.wantValue {
				t.Fatalf("max_completion_tokens = %d, 期望 %d", limit.Int(), tc.wantValue)
			}
		})
	}
}

func TestOpencodeToolMatchesRequiresStrictFalse(t *testing.T) {
	def, ok := knownOpencodeTool("shell")
	if !ok {
		t.Fatal("knownOpencodeTool 应识别 shell")
	}
	cases := []struct {
		name    string
		strict  any
		include bool
		want    bool
	}{
		{name: "显式 false 匹配", strict: false, include: true, want: true},
		{name: "缺失不匹配", include: false},
		{name: "true 不匹配", strict: true, include: true},
		{name: "字符串不匹配", strict: "false", include: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			function := map[string]any{
				"name":        "shell",
				"description": opencodeToolDescription("shell", def.Description),
				"parameters":  def.Parameters,
			}
			if tc.include {
				function["strict"] = tc.strict
			}
			raw, err := json.Marshal(map[string]any{"type": "function", "function": function})
			if err != nil {
				t.Fatalf("marshal tool: %v", err)
			}
			if got := opencodeToolMatches(def, gjson.ParseBytes(raw), model.UpstreamProtocolChat); got != tc.want {
				t.Fatalf("opencodeToolMatches = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

func TestOpencodeFreeChatToolStrictAndEncoding(t *testing.T) {
	raw := opencodeFreeTool("read", model.UpstreamProtocolChat)
	tool := gjson.Parse(raw)
	if strict := tool.Get("function.strict"); strict.Type != gjson.False {
		t.Fatalf("生成工具应显式 strict=false: %s", raw)
	}
	if strings.Contains(raw, `\u003c`) || !strings.Contains(raw, "<line>") {
		t.Fatalf("工具 JSON 不应转义 HTML: %s", raw)
	}

	legacy := v1DeprecatedReadTool()
	if gjson.Parse(legacy).Get("function.strict").Exists() {
		t.Fatalf("v1 legacy 工具不应带 strict: %s", legacy)
	}
}

func TestApplyOpencodeFreeLaneHeadersV2UserAgent(t *testing.T) {
	channel := model.Channel{
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/inference",
		Models: []model.ChannelModel{{
			Name:             "chat-x",
			UpstreamProtocol: model.UpstreamProtocolChat,
		}},
	}
	request := &httpclient.Request{
		Headers: http.Header{"User-Agent": {"opencode/latest/2.0.32/cli"}},
		Body:    []byte(`{"model":"chat-x"}`),
	}
	applyOpencodeFreeLaneHeaders(channel, request, "chat-x")
	if got := request.Headers.Get("User-Agent"); got != "opencode/latest/2.1.4/cli" {
		t.Fatalf("User-Agent = %q, 期望 v2 动态值", got)
	}
	if got := request.Headers.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q, 期望 */*", got)
	}
}

func TestApplyOpencodeFreeLaneHeadersV1UserAgentVersion(t *testing.T) {
	channel := model.Channel{
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/zen",
		Models: []model.ChannelModel{{
			Name:             "chat-x",
			UpstreamProtocol: model.UpstreamProtocolChat,
		}},
	}
	request := &httpclient.Request{
		Headers: http.Header{},
		Body:    []byte(`{"model":"chat-x"}`),
	}
	applyOpencodeFreeLaneHeaders(channel, request, "chat-x")
	want := "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4"
	if got := request.Headers.Get("User-Agent"); got != want {
		t.Fatalf("User-Agent = %q, 期望 %q", got, want)
	}
	if got := request.Headers.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q, 期望 */*", got)
	}
}

func TestApplyOpencodeFreeLaneHeadersV2AnthropicBeta(t *testing.T) {
	channel := model.Channel{
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/inference",
		Models: []model.ChannelModel{{
			Name:             "claude-x",
			UpstreamProtocol: model.UpstreamProtocolAnthropic,
		}},
	}
	request := &httpclient.Request{
		Method:  http.MethodPost,
		URL:     "https://opencode.ai/inference/v1/messages?foo=bar&beta=false",
		Headers: http.Header{},
		Query:   url.Values{"keep": {"1"}},
		Body:    []byte(`{"model":"claude-x"}`),
	}

	applyOpencodeFreeLaneHeaders(channel, request, "claude-x")
	applyOpencodeFreeLaneHeaders(channel, request, "claude-x")
	if got := request.Query.Get("beta"); got != "true" {
		t.Fatalf("request.Query beta = %q, 期望 true", got)
	}

	built, err := httpclient.BuildHttpRequest(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildHttpRequest: %v", err)
	}
	query := built.URL.Query()
	if got := query.Get("foo"); got != "bar" {
		t.Fatalf("已有 query foo = %q, 期望 bar", got)
	}
	if got := query.Get("keep"); got != "1" {
		t.Fatalf("已有 query keep = %q, 期望 1", got)
	}
	if got := query["beta"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("beta = %v, 期望唯一 true", got)
	}
}

func TestApplyOpencodeFreeLaneHeadersSkipsBetaForChatAndV1(t *testing.T) {
	cases := []struct {
		baseURL   string
		modelName string
		protocol  string
	}{
		{
			baseURL:   "https://opencode.ai/inference",
			modelName: "chat-x",
			protocol:  model.UpstreamProtocolChat,
		},
		{
			baseURL:   "https://opencode.ai/zen",
			modelName: "claude-x",
			protocol:  model.UpstreamProtocolAnthropic,
		},
	}
	for _, tc := range cases {
		channel := model.Channel{
			OpencodeCompat: true,
			BaseURL:        tc.baseURL,
			Models: []model.ChannelModel{{
				Name:             tc.modelName,
				UpstreamProtocol: tc.protocol,
			}},
		}
		request := &httpclient.Request{
			Method:  http.MethodPost,
			URL:     "https://unit.invalid/v1/messages",
			Headers: http.Header{},
			Body:    []byte(`{"model":"` + tc.modelName + `"}`),
		}
		applyOpencodeFreeLaneHeaders(channel, request, tc.modelName)
		if got := request.Query.Get("beta"); got != "" {
			t.Fatalf("%s 不应补 beta, 实际 %q", channel.BaseURL, got)
		}
	}
}
