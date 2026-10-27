package relay

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/utils/opencodeid"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenCode Zen 免费档只接受「Agent 形态」的流式请求: stream=true 且请求体携带
// shell/edit/glob/grep/read 这五个核心工具名, 否则一律 403 FreeTierError。本文件
// 在连线上游前把免费模型的请求规范化成该形态, 并在客户端原本是非流式时把上游
// 折叠出的 SSE 聚合回 JSON。行为对齐 opencode2api 的匿名/免费档请求改写, 不复用其实现。
//
// 2026-10-14 起上游对工具定义做逐字节比对: 名字存在不够, description 与 parameters
// 必须与官方 CLI 完全一致, 占位 stub 会被 403 拒绝。因此本文件 embed 了从官方客户端
// 捕获的真实定义(opencode_tools.json), 注入和同名替换均用真实定义。

// opencodeFreeCoreTools 是免费档判定 Agent 形态所需的核心工具名。缺任一即被上游拒绝。
// OpenCode 2.x 将 bash 重命名为 shell; 旧名 bash 的定义上游不认识, 会 403。
var opencodeFreeCoreTools = []string{"shell", "edit", "glob", "grep", "read"}

// opencodeToolsJSON 是从 OpenCode 官方客户端捕获的真实工具定义(description + JSON schema)。
// 上游对每个识别的工具名做逐字节比对, 占位 stub 会被 403 FreeTierError 拒绝。
//
//go:embed opencode_tools.json
var opencodeToolsJSON []byte

// opencodeToolDef 是 opencode_tools.json 中单个工具的定义。
type opencodeToolDef struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

var (
	opencodeToolsOnce sync.Once
	opencodeTools     map[string]opencodeToolDef
)

// loadOpencodeTools 解析 embed 的真实工具定义; 失败时返回空表(降级为占位, 上游会 403)。
func loadOpencodeTools() {
	opencodeToolsOnce.Do(func() {
		defs := make(map[string]opencodeToolDef)
		if err := json.Unmarshal(opencodeToolsJSON, &defs); err != nil {
			defs = map[string]opencodeToolDef{}
		}
		opencodeTools = defs
	})
}

// knownOpencodeTool 返回工具名的真实定义; 未收录返回 false。
func knownOpencodeTool(name string) (opencodeToolDef, bool) {
	loadOpencodeTools()
	def, ok := opencodeTools[name]
	return def, ok
}

// opencodeToolMatches 判定工具定义完全一致。
func opencodeToolMatches(def opencodeToolDef, tool gjson.Result, protocol string) bool {
	namePath, descPath, paramPath := toolFieldPaths(protocol)
	if protocol == model.UpstreamProtocolChat {
		if tool.Get("function.strict").Type != gjson.False {
			return false
		}
	}
	if tool.Get(descPath).String() != opencodeToolDescription(tool.Get(namePath).String(), def.Description) {
		return false
	}
	if def.Parameters == nil {
		return true
	}
	var parameters map[string]any
	if err := json.Unmarshal([]byte(tool.Get(paramPath).Raw), &parameters); err != nil {
		return false
	}
	return reflect.DeepEqual(parameters, def.Parameters)
}

func toolFieldPaths(protocol string) (namePath, descPath, paramPath string) {
	switch protocol {
	case model.UpstreamProtocolAnthropic:
		return "name", "description", "input_schema"
	case model.UpstreamProtocolResponses:
		return "name", "description", "parameters"
	default:
		return "function.name", "function.description", "function.parameters"
	}
}

// opencodeToolDescription 按平台调整 shell 工具描述。
// 官方 Windows 客户端实抓(2026-10-23, Git Bash 与 PowerShell 两种启动环境一致)
// 发的是 "on Windows using bash": 平台词随 GOOS 变, shell 词恒为 bash。
func opencodeToolDescription(name, description string) string {
	if name == "shell" && runtime.GOOS == "windows" {
		return strings.Replace(description, "on Linux using bash", "on Windows using bash", 1)
	}
	return description
}

// isOpencodeFreeModel 判定该渠道上的模型是否落在 OpenCode Zen 免费档:
// 渠道 BaseURL 是 /zen 或 /inference(见 model.OpenCodeTier 的档位识别), 并且
// 渠道是内置免费渠道, 或模型名含 "free" 子串(不区分大小写, 对齐 opencode2api
// 的 isFreeModel 兜底)。Go 档(/zen/go 或 /zen/v1)是付费档, 不做改写。
func isOpencodeFreeModel(channel model.Channel, modelName string) bool {
	if model.OpenCodeTier(channel.BaseURL) != "zen" {
		return false
	}
	if channel.IsFree {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelName)), "free")
}

// opencodeV2CompletionLimit 是 v2 档默认输出上限。
const opencodeV2CompletionLimit = 32000

// isOpencodeV1Lane 判定渠道是否走 opencode 1.x 客户端形态。
func isOpencodeV1Lane(channel model.Channel) bool {
	return model.OpencodeLaneOf(channel) == "v1"
}

// opencodeLaneKey 返回渠道对应的体检模板档位键, 与模板 map 的键一致。
func opencodeLaneKey(channel model.Channel) string {
	if isOpencodeV1Lane(channel) {
		return "v1"
	}
	return "v2"
}

// opencodeUpstreamFamily 返回 v2 family 段。
func opencodeUpstreamFamily(provider model.ChannelProvider) string {
	switch provider {
	case model.ChannelProviderOpenAI, model.ChannelProviderOpenAIResponses:
		return "openai"
	case model.ChannelProviderAnthropic:
		return "anthropic"
	default:
		return ""
	}
}

// opencodeFamilyForFormat 按客户端协议选 family。
func opencodeFamilyForFormat(format llm.APIFormat) string {
	switch format {
	case llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponse:
		return "openai"
	case llm.APIFormatAnthropicMessage:
		return "anthropic"
	default:
		return ""
	}
}

// opencodeV2FamilyBaseURL 仅补裸 /inference。
func opencodeV2FamilyBaseURL(base string, channel model.Channel, family string) string {
	if family == "" || !isOpencodeFreeLane(channel) ||
		strings.HasSuffix(strings.TrimSpace(channel.BaseURL), "##") {
		return base
	}
	parsed, err := url.Parse(base)
	if err != nil || strings.TrimRight(parsed.Path, "/") != "/inference" {
		return base
	}
	parsed.Path = "/inference/" + family
	parsed.RawPath = ""
	return parsed.String()
}

// opencodeModelProtocol 在渠道模型表中按名查 UpstreamProtocol; 未找到或为空返回 chat(默认)。
// 与 opencode2api 的 v1ProviderUtils 默认分支一致: SystemOne 等无 v1 对应的协议也报 chat 包。
func opencodeModelProtocol(channel model.Channel, modelName string) string {
	for i := range channel.Models {
		if channel.Models[i].Name == modelName {
			if p := channel.Models[i].UpstreamProtocol; p != "" {
				return p
			}
			break
		}
	}
	return model.UpstreamProtocolChat
}

// opencodeV1Accept / opencodeV2Accept 是两档官方客户端发出的 Accept: 均为 */*。
const (
	opencodeV1Accept = "*/*"
	opencodeV2Accept = "*/*"
)

// isOpencodeFreeLane 报告渠道是否属于 OpenCode 免费档: OpencodeCompat 且档位为 "zen"
// (/zen 与 /inference)。/zen/v1、/zen/go 的 go 档与其余渠道均不在内。
// 与 helper.ChannelHttpClient 选择 Bun 传输的门控同口径。
func isOpencodeFreeLane(channel model.Channel) bool {
	return channel.OpencodeCompat && model.OpenCodeTier(channel.BaseURL) == "zen"
}

// opencodeFreeLaneAccept 返回该免费档官方客户端的 Accept 值; 非免费档返回空串。
// 调用方据此让出站请求使用官方 Accept, 而不是流式透传的默认 text/event-stream:
// 上游会把 Accept 不一致视为非官方客户端。
func opencodeFreeLaneAccept(channel model.Channel) string {
	if !isOpencodeFreeLane(channel) {
		return ""
	}
	if isOpencodeV1Lane(channel) {
		return opencodeV1Accept
	}
	return opencodeV2Accept
}

// applyOpencodeFreeLaneHeaders 在连线上游前按档位设置 OpenCode 免费档的请求头:
//   - v1 档(/zen): User-Agent 用协议相关的 opencode/1.19.4 ai-sdk/provider-utils/<ver>
//     runtime/bun/1.4.4(<ver> 按出站协议), Accept 用 */*, 并发送 x-opencode-request
//     (msg_ 升序 id);
//   - v2 档(/inference): User-Agent 动态覆盖为 opencode/latest/2.1.4/cli,
//     Accept 用 */*, 且不发送 x-opencode-request
//     (v2 网关对此头 403); Anthropic 出站补 beta=true。
//
// 仅对免费档改写头, 改写发生在 applyChannelConfig 之后, 覆盖渠道静态同名头。
// x-opencode-project 由关联头注入统一处理。
func applyOpencodeFreeLaneHeaders(channel model.Channel, request *httpclient.Request, modelName string) {
	accept := opencodeFreeLaneAccept(channel)
	if accept == "" || request == nil {
		return
	}
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	request.Headers.Set("Accept", accept)
	request.Headers.Set("x-opencode-client", "cli")
	isAnthropic := opencodeModelProtocol(channel, modelName) == model.UpstreamProtocolAnthropic
	if isAnthropic {
		request.Headers.Set("anthropic-version", "2023-06-01")
	}
	if !isOpencodeV1Lane(channel) {
		// 动态覆盖渠道静态 UA，存量库也生效。
		request.Headers.Set("User-Agent", model.OpencodeV2UserAgent())
		if isAnthropic {
			applyAnthropicBetaQuery(request)
			request.Headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
		}
		return
	}
	request.Headers.Set("User-Agent", model.OpencodeV1UserAgent(opencodeModelProtocol(channel, modelName)))
	request.Headers.Set("x-opencode-request", opencodeid.UserMessageID(
		request.Headers.Get("x-opencode-request"),
		request.Body,
		request.Headers.Get(opencodeSessionHeader),
	))
	if isAnthropic {
		if beta := v1AnthropicBetas(request.Body); beta != "" {
			request.Headers.Set("anthropic-beta", beta)
		}
	}
}

// v2 Anthropic 补 beta, 保留原 query。
func applyAnthropicBetaQuery(request *httpclient.Request) {
	if request.Query == nil {
		request.Query = make(url.Values)
	}
	request.Query.Set("beta", "true")
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return
	}
	query := parsed.Query()
	if query.Has("beta") {
		query.Del("beta")
		parsed.RawQuery = query.Encode()
		request.URL = parsed.String()
	}
}

// bodyModelName 从请求体提取 model 字段; 空串表示无法识别(不改写)。
func bodyModelName(body []byte) string {
	return gjson.GetBytes(body, "model").String()
}

// shapeOpencodeFreeBody 把免费档正文规范成 Agent 形态。
// shapeOpencodeFreeBody 把免费档正文规范成 Agent 形态。第三返回值是出站
// 工具别名表(出站名 → 客户端名), 转发路径据此把响应里的出站名还原给客户端。
func shapeOpencodeFreeBody(body []byte, v1Lane bool, protocol string) ([]byte, bool, map[string]string) {
	out := body
	changed := false

	if !gjson.GetBytes(out, "stream").Bool() {
		if next, err := sjson.SetBytes(out, "stream", true); err == nil {
			out, changed = next, true
		}
	}
	if protocol == model.UpstreamProtocolChat {
		if !gjson.GetBytes(out, "stream_options.include_usage").Bool() {
			if next, err := sjson.SetBytes(out, "stream_options.include_usage", true); err == nil {
				out, changed = next, true
			}
		}
	}
	if v1Lane {
		next, alias, injected := ensureV1ReadTool(out)
		if injected {
			out, changed = next, true
		}
		if protocol == model.UpstreamProtocolChat {
			if sysNext, sysChanged := ensureOfficialOpencodeSystem(out, "v1", bodyModelName(out)); sysChanged {
				out, changed = sysNext, true
			}
			// 官方 1.19.4 主请求恒带 max_tokens=32000 与 tool_choice="auto"(2026-10-26 MITM 实抓);
			// 键序归一只能重排已存在的键, 缺字段必须在此补齐, 否则上游按正文形态判非官方客户端。
			if !gjson.GetBytes(out, "max_tokens").Exists() && !gjson.GetBytes(out, "max_completion_tokens").Exists() {
				if next, err := sjson.SetBytes(out, "max_tokens", opencodeV2CompletionLimit); err == nil {
					out, changed = next, true
				}
			}
			if !gjson.GetBytes(out, "tool_choice").Exists() {
				if next, err := sjson.SetBytes(out, "tool_choice", "auto"); err == nil {
					out, changed = next, true
				}
			}
			if normalized := normalizeOpencodeChatBody(out, v1ChatKeyOrder); normalized != nil {
				out = normalized
			}
		}
		if alias == "" {
			return out, changed, nil
		}
		return out, changed, map[string]string{v1ReadToolName: alias}
	}
	if next, injected := ensureOpencodeFreeTools(out, protocol); injected {
		out, changed = next, true
	}
	if !gjson.GetBytes(out, "max_tokens").Exists() && !gjson.GetBytes(out, "max_completion_tokens").Exists() && !gjson.GetBytes(out, "max_output_tokens").Exists() {
		if next, err := sjson.SetBytes(out, protocolTokenLimitField(protocol), opencodeV2CompletionLimit); err == nil {
			out, changed = next, true
		}
	}
	if protocol == model.UpstreamProtocolChat {
		if sysNext, sysChanged := ensureOfficialOpencodeSystem(out, "v2", bodyModelName(out)); sysChanged {
			out, changed = sysNext, true
		}
		if toolNext, aliases := ensureOfficialV2Toolset(out); aliases != nil {
			out, changed = toolNext, true
			if normalized := normalizeOpencodeChatBody(out, v2ChatKeyOrder); normalized != nil {
				out = normalized
			}
			return out, changed, aliases
		}
		if normalized := normalizeOpencodeChatBody(out, v2ChatKeyOrder); normalized != nil {
			out = normalized
		}
	}
	return out, changed, nil
}

// ocOfficialSystemPrefixLen 是官方系统提示幂等判定的前缀长度。
const ocOfficialSystemPrefixLen = 64

// renderOfficialOpencodeTemplate 渲染官方系统提示, 第二返回值是幂等判定前缀。
func renderOfficialOpencodeTemplate(lane, modelName string) (string, string, bool) {
	templates, err := loadOcProbeTemplates()
	if err != nil || templates[lane] == nil {
		return "", "", false
	}
	raw := templates[lane].System
	prefix := raw
	if len(prefix) > ocOfficialSystemPrefixLen {
		prefix = prefix[:ocOfficialSystemPrefixLen]
	}
	official := raw
	official = strings.ReplaceAll(official, "{{MODEL_NAME}}", modelName)
	official = strings.ReplaceAll(official, "{{MODEL_ID}}", modelName)
	official = strings.ReplaceAll(official, "{{DATE}}", ocProbeDate(time.Now()))
	official = strings.ReplaceAll(official, "{{WORKDIR}}", ocRuntimeWorkdir())
	if strings.Contains(official, "{{") {
		return "", "", false
	}
	return official, prefix, true
}

// ensureOfficialOpencodeSystem 保证免费档正文首条 system 消息是官方模板形态。
// 上游对免费档做系统提示门禁: 缺失或非官方主体的 system 一律 429(2026-10-24 实测,
// 话术 FreeUsageLimitError), 官方模板尾部追加客户端指令不影响通过。无 system 时
// 直接插入; 有客户端 system 时以官方模板打头、客户端原文追加到尾部。
// 客户端 system 是 content parts 数组时, 官方提示作为首个 text part 前置。
// 首条 system 已是官方形态时幂等跳过, 保证体检模板与重复整形不受影响。
func ensureOfficialOpencodeSystem(body []byte, lane, modelName string) ([]byte, bool) {
	official, prefix, ok := renderOfficialOpencodeTemplate(lane, modelName)
	if !ok {
		return body, false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}
	items := make([]string, 0, len(messages.Array())+1)
	clientSystem := ""
	for index, msg := range messages.Array() {
		if index == 0 && msg.Get("role").String() == "system" {
			content := msg.Get("content")
			if content.IsArray() {
				return prefixOfficialContentPart(body, official, prefix, content.Array())
			}
			text := content.String()
			if strings.HasPrefix(text, prefix) {
				return body, false
			}
			clientSystem = text
			continue
		}
		items = append(items, msg.Raw)
	}
	merged := official
	if clientSystem != "" {
		merged = official + "\n\n" + clientSystem
	}
	sysJSON, err := marshalOpencodeJSON(merged)
	if err != nil {
		return body, false
	}
	parts := make([]string, 0, len(items)+1)
	parts = append(parts, `{"role":"system","content":`+string(sysJSON)+`}`)
	parts = append(parts, items...)
	next, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(parts, ",")+"]"))
	if err != nil {
		return body, false
	}
	return next, true
}

// prefixOfficialContentPart 把官方提示作为首个 text part 前置, 保留客户端其余 parts。
func prefixOfficialContentPart(body []byte, official, prefix string, parts []gjson.Result) ([]byte, bool) {
	if len(parts) > 0 && strings.HasPrefix(parts[0].Get("text").String(), prefix) {
		return body, false
	}
	part, err := marshalOpencodeJSON(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: official})
	if err != nil {
		return body, false
	}
	var builder strings.Builder
	builder.WriteString("[")
	builder.Write(part)
	for _, item := range parts {
		builder.WriteString(",")
		builder.WriteString(item.Raw)
	}
	builder.WriteString("]")
	next, err := sjson.SetRawBytes(body, "messages.0.content", []byte(builder.String()))
	if err != nil {
		return body, false
	}
	return next, true
}

// ensureOfficialOpencodeInstructions 保证免费档 Responses 正文的 instructions 以官方模板打头。
// Responses 的系统提示在 instructions 字段而非 messages, 上游门禁口径与 Chat 相同。
func ensureOfficialOpencodeInstructions(body []byte, lane, modelName string) ([]byte, bool) {
	official, prefix, ok := renderOfficialOpencodeTemplate(lane, modelName)
	if !ok {
		return body, false
	}
	current := gjson.GetBytes(body, "instructions").String()
	if strings.HasPrefix(current, prefix) {
		return body, false
	}
	merged := official
	if strings.TrimSpace(current) != "" {
		merged = official + "\n\n" + current
	}
	next, err := sjson.SetBytes(body, "instructions", merged)
	if err != nil {
		return body, false
	}
	return next, true
}

func protocolTokenLimitField(protocol string) string {
	switch protocol {
	case model.UpstreamProtocolResponses:
		return "max_output_tokens"
	case model.UpstreamProtocolAnthropic:
		return "max_tokens"
	default:
		return "max_completion_tokens"
	}
}

// v1ReadToolName 是 v1 档(/zen)唯一要求的工具名, 该档不校验工具定义。
const v1ReadToolName = "read"

// v1DeprecatedReadDescription 让模型忽略注入的占位 read 工具。
const v1DeprecatedReadDescription = "DEPRECATED: This tool is deprecated and disabled. Do NOT call or reference this tool under any circumstances; ignore it completely."

// isV1ReadToolCandidate 判定工具名是否为 read 的变体。
func isV1ReadToolCandidate(name string) bool {
	if strings.EqualFold(name, v1ReadToolName) {
		return true
	}
	lower := strings.ToLower(name)
	switch lower {
	case "read_file", "readfile", "view_file", "viewfile", "read_text", "readtext":
		return true
	}
	return false
}

// ensureV1ReadTool 保证 v1 档存在单个 read 工具。返回改写后的正文、
// 被改名的原始工具名(空表示无需响应还原)和是否改写。
func ensureV1ReadTool(body []byte) ([]byte, string, bool) {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, "", false
	}
	alias := ""
	items := make([]string, 0, len(tools.Array())+1)
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == v1ReadToolName {
			return body, "", false
		}
		if alias == "" && isV1ReadToolCandidate(name) {
			alias = name
			renamed := renameV1ToolDef(t.Raw, name)
			items = append(items, renamed)
			continue
		}
		items = append(items, t.Raw)
	}
	changed := true
	if alias == "" {
		items = append(items, v1DeprecatedReadTool())
	} else {
		items = append(items, v1DeprecatedReadTool())
	}
	next, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, "", false
	}
	if alias != "" {
		next = rewriteV1ToolReferences(next, alias)
	}
	return next, alias, changed
}

// renameV1ToolDef 把工具定义中的 name 改为 read。
func renameV1ToolDef(raw, oldName string) string {
	out, err := sjson.SetBytes([]byte(raw), "function.name", v1ReadToolName)
	if err != nil {
		return raw
	}
	return string(out)
}

// rewriteV1ToolReferences 把 tool_choice 和历史 tool_calls 中的 alias 改为 read。
func rewriteV1ToolReferences(body []byte, alias string) []byte {
	out := body
	// tool_choice.function.name
	tcName := gjson.GetBytes(out, "tool_choice.function.name").String()
	if tcName == alias {
		if next, err := sjson.SetBytes(out, "tool_choice.function.name", v1ReadToolName); err == nil {
			out = next
		}
	}
	// messages[].tool_calls[].function.name
	messages := gjson.GetBytes(out, "messages")
	if messages.IsArray() {
		for i, msg := range messages.Array() {
			calls := msg.Get("tool_calls")
			if !calls.IsArray() {
				continue
			}
			for j, call := range calls.Array() {
				if call.Get("function.name").String() == alias {
					path := fmt.Sprintf("messages.%d.tool_calls.%d.function.name", i, j)
					if next, err := sjson.SetBytes(out, path, v1ReadToolName); err == nil {
						out = next
					}
				}
			}
		}
	}
	return out
}

// restoreV1ReadAliasInBody 把响应正文中的 read 工具调用名还原为 alias。
func restoreV1ReadAliasInBody(body []byte, alias string) []byte {
	if alias == "" || alias == v1ReadToolName {
		return body
	}
	out := body
	// Chat: choices[].message.tool_calls[].function.name
	choices := gjson.GetBytes(out, "choices")
	if choices.IsArray() {
		for i, choice := range choices.Array() {
			calls := choice.Get("message.tool_calls")
			if !calls.IsArray() {
				continue
			}
			for j, call := range calls.Array() {
				if call.Get("function.name").String() == v1ReadToolName {
					path := fmt.Sprintf("choices.%d.message.tool_calls.%d.function.name", i, j)
					if next, err := sjson.SetBytes(out, path, alias); err == nil {
						out = next
					}
				}
			}
		}
	}
	// Anthropic: content[].name (tool_use blocks)
	content := gjson.GetBytes(out, "content")
	if content.IsArray() {
		for i, block := range content.Array() {
			if block.Get("type").String() == "tool_use" && block.Get("name").String() == v1ReadToolName {
				path := fmt.Sprintf("content.%d.name", i)
				if next, err := sjson.SetBytes(out, path, alias); err == nil {
					out = next
				}
			}
		}
	}
	return out
}

// restoreV1ReadAliasInEvent 把流式事件中的 read 工具调用名还原为 alias。
func restoreV1ReadAliasInEvent(data []byte, alias string) []byte {
	if alias == "" || alias == v1ReadToolName || len(data) == 0 {
		return data
	}
	// Chat delta: choices[].delta.tool_calls[].function.name
	choices := gjson.GetBytes(data, "choices")
	if choices.IsArray() {
		out := data
		for i, choice := range choices.Array() {
			calls := choice.Get("delta.tool_calls")
			if !calls.IsArray() {
				continue
			}
			for j, call := range calls.Array() {
				if call.Get("function.name").String() == v1ReadToolName {
					path := fmt.Sprintf("choices.%d.delta.tool_calls.%d.function.name", i, j)
					if next, err := sjson.SetBytes(out, path, alias); err == nil {
						out = next
					}
				}
			}
		}
		if !bytes.Equal(out, data) {
			return out
		}
	}
	// Anthropic: content_block_delta with tool_use name
	name := gjson.GetBytes(data, "name").String()
	if name == v1ReadToolName {
		if next, err := sjson.SetBytes(data, "name", alias); err == nil {
			return next
		}
	}
	return data
}

// v1DeprecatedReadTool 生成 v1 档的禁用 read 占位定义。
func v1DeprecatedReadTool() string {
	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        v1ReadToolName,
			"description": v1DeprecatedReadDescription,
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	})
	return string(b)
}

// ensureOpencodeFreeTools 确保核心工具存在且定义真实, 报告是否改写。
//   - 客户端已声明的同名核心工具: 替换为真实定义(上游逐字节比对, 客户端自带的不匹配会 403)。
//   - 缺失的核心工具: 追加真实定义。
//   - 非核心工具原样保留。
//
// tools 字段存在但不是数组时视为客户端输入异常, 不改写。
func ensureOpencodeFreeTools(body []byte, protocol string) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, false
	}

	changed := false
	present := make(map[string]bool, len(opencodeFreeCoreTools))
	items := make([]string, 0, len(tools.Array())+len(opencodeFreeCoreTools))
	namePath, _, _ := toolFieldPaths(protocol)

	for _, t := range tools.Array() {
		name := t.Get(namePath).String()
		if name == "" {
			items = append(items, t.Raw)
			continue
		}
		present[name] = true
		// 同名已知工具: description 或 parameters 不一致时替换。上游逐字节比对
		// description/parameters, 客户端自带的定义几乎不可能恰好匹配。已一致的
		// 不替换, 保持幂等。
		if def, known := knownOpencodeTool(name); known {
			if !opencodeToolMatches(def, t, protocol) {
				items = append(items, opencodeFreeTool(name, protocol))
				changed = true
				continue
			}
		}
		items = append(items, t.Raw)
	}

	missing := make([]string, 0, len(opencodeFreeCoreTools))
	for _, name := range opencodeFreeCoreTools {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		for _, name := range missing {
			items = append(items, opencodeFreeTool(name, protocol))
		}
		changed = true
	}

	if !changed {
		return body, false
	}

	next, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return next, true
}

// opencodeFreeTool 生成协议感知的工具定义。核心工具使用 embed 的真实定义
// (description + parameters); 未知工具降级为占位(上游不识别, 不影响校验)。
func opencodeFreeTool(name, protocol string) string {
	description := "Agent tool " + name
	parameters := map[string]any{"type": "object", "properties": map[string]any{}}
	if def, ok := knownOpencodeTool(name); ok {
		description = def.Description
		if def.Parameters != nil {
			parameters = def.Parameters
		}
	}
	description = opencodeToolDescription(name, description)
	var tool any
	switch protocol {
	case model.UpstreamProtocolAnthropic:
		tool = map[string]any{
			"name":         name,
			"description":  description,
			"input_schema": parameters,
		}
	case model.UpstreamProtocolResponses:
		tool = map[string]any{
			"type":        "function",
			"name":        name,
			"description": description,
			"parameters":  parameters,
		}
	default:
		tool = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": description,
				"parameters":  parameters,
				"strict":      false,
			},
		}
	}
	b, _ := marshalOpencodeJSON(tool)
	return string(b)
}

// marshalOpencodeJSON 编码 JSON 时不转义 HTML。
func marshalOpencodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// v2ChatKeyOrder 是免费档 v2 Chat 正文的固定键序, 对齐官方客户端逐字节指纹。
var v2ChatKeyOrder = []string{"model", "messages", "tools", "stream", "stream_options", "max_completion_tokens"}

// v1ChatKeyOrder 是 v1 档(/zen)官方 1.19.4 客户端的 Chat 键序, 由实抓核实。
var v1ChatKeyOrder = []string{"model", "max_tokens", "messages", "tools", "tool_choice", "stream", "stream_options"}

// normalizeOpencodeChatBody 把免费档 Chat 正文重编码为固定键序且不转义 HTML,
// 对齐上游逐字节门禁(#142)。仅在免费档 Chat 路径调用, keyOrder 按档位传入。
func normalizeOpencodeChatBody(body []byte, keyOrder []string) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	first := true
	written := make(map[string]bool, len(raw))
	emit := func(key string) {
		val, ok := raw[key]
		if !ok {
			return
		}
		if !first {
			buffer.WriteByte(',')
		}
		first = false
		buffer.WriteString(jsonEscapeString(key))
		buffer.WriteByte(':')
		buffer.Write(val)
		written[key] = true
	}
	for _, key := range keyOrder {
		emit(key)
	}
	rest := make([]string, 0, len(raw))
	for key := range raw {
		if !written[key] {
			rest = append(rest, key)
		}
	}
	slices.Sort(rest)
	for _, key := range rest {
		emit(key)
	}
	buffer.WriteByte('}')
	out := buffer.Bytes()
	if !json.Valid(out) {
		return nil
	}
	return out
}

func jsonEscapeString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// collapseOpencodeFreeStream 把免费档强制流式得到的完整 SSE 折叠成客户端协议的非流式正文。
// resp 是 sendPassthroughStream 或 sendConverted 流式分支已通过 readStreamWindow 预读校验的响应;
// 此处把窗口事件 + 剩余事件一并聚合为完整 JSON, 与非流式响应同形。
func collapseOpencodeFreeStream(ctx context.Context, format llm.APIFormat, resp *upstreamResponse) ([]byte, *llm.Usage, error) {
	if resp == nil {
		return nil, nil, fmt.Errorf("collapse free stream: nil response")
	}
	chunks := make([]*httpclient.StreamEvent, 0, len(resp.window)+16)
	chunks = append(chunks, resp.window...)
	if !resp.last && resp.events != nil {
		for resp.events.Next() {
			chunks = append(chunks, resp.events.Current())
		}
		if err := resp.events.Err(); err != nil {
			return nil, nil, fmt.Errorf("collapse free stream: %w", err)
		}
	}
	body, meta, err := inboundForFormat(format).AggregateStreamChunks(ctx, chunks)
	if err != nil {
		return nil, nil, fmt.Errorf("collapse free stream aggregate: %w", err)
	}
	return body, meta.Usage, nil
}

// validateCollapsedFreeBody 校验折叠正文的用量与终态, 与非流式路径同一口径。
// 折叠路径跳过了非流式 Outbound.TransformResponse 的白名单校验, 这里按客户端协议补齐:
// OpenAI Chat 检查 choices[].finish_reason 落在白名单内; 其余协议由 readStreamWindow
// 的逐事件判定与 validateRoundUsage/validateRoundAnswer 覆盖。
func validateCollapsedFreeBody(format llm.APIFormat, body []byte, usage *llm.Usage) error {
	terminal := gjson.GetBytes(body, "choices.0.finish_reason").String()
	if err := validateRoundUsage(usage, terminal); err != nil {
		return err
	}
	if err := validateRoundAnswer(format, body); err != nil {
		return err
	}
	if terminal != "" && !validChatFinishReason(terminal) {
		return fmt.Errorf("%w: %q", errAbnormalFinish, terminal)
	}
	return nil
}

type v1AnthropicBetaRequest struct {
	Model             string            `json:"model"`
	MCPServers        []json.RawMessage `json:"mcp_servers"`
	ContextManagement *struct {
		Edits []struct {
			Type string `json:"type"`
		} `json:"edits"`
	} `json:"context_management"`
	Container    json.RawMessage `json:"container"`
	OutputConfig struct {
		TaskBudget json.RawMessage `json:"task_budget"`
	} `json:"output_config"`
	Speed     json.RawMessage `json:"speed"`
	Fallbacks json.RawMessage `json:"fallbacks"`
	Thinking  struct {
		BlockBinding json.RawMessage `json:"block_binding"`
	} `json:"thinking"`
	Tools []struct {
		Type           string          `json:"type"`
		InputExamples  json.RawMessage `json:"input_examples"`
		AllowedCallers json.RawMessage `json:"allowed_callers"`
	} `json:"tools"`
}

var v1AnthropicToolBetas = map[string]string{
	"code_execution_20250522": "code-execution-2025-05-22",
	"code_execution_20250825": "code-execution-2025-08-25",
	"computer_20241022":       "computer-use-2024-10-22",
	"computer_20250124":       "computer-use-2025-01-24",
	"computer_20251124":       "computer-use-2025-11-24",
	"text_editor_20241022":    "computer-use-2024-10-22",
	"text_editor_20250124":    "computer-use-2025-01-24",
	"text_editor_20250429":    "computer-use-2025-01-24",
	"bash_20241022":           "computer-use-2024-10-22",
	"bash_20250124":           "computer-use-2025-01-24",
	"memory_20250818":         "context-management-2025-06-27",
	"web_fetch_20250910":      "web-fetch-2025-09-10",
	"web_fetch_20260209":      "code-execution-web-tools-2026-02-09",
	"web_search_20260209":     "code-execution-web-tools-2026-02-09",
	"advisor_20260301":        "advisor-tool-2026-03-01",
}

var claude3OrOlderPattern = regexp.MustCompile(`claude-(?:instant(?:-|$)|v?2(?:$|[-.:])|3(?:$|[-.]))`)

// v1AnthropicBetas 从 body 派生 anthropic-beta
func v1AnthropicBetas(body []byte) string {
	var request v1AnthropicBetaRequest
	_ = json.Unmarshal(body, &request)
	var betas []string
	add := func(beta string) {
		if !slices.Contains(betas, beta) {
			betas = append(betas, beta)
		}
	}
	if len(request.MCPServers) > 0 {
		add("mcp-client-2025-04-04")
	}
	if request.ContextManagement != nil {
		add("context-management-2025-06-27")
		for _, edit := range request.ContextManagement.Edits {
			if edit.Type == "compact_20260112" {
				add("compact-2026-01-12")
			}
		}
	}
	var container struct {
		Skills []json.RawMessage `json:"skills"`
	}
	if json.Unmarshal(request.Container, &container) == nil && len(container.Skills) > 0 {
		add("code-execution-2025-08-25")
		add("skills-2025-10-02")
		add("files-api-2025-04-14")
	}
	if isPresentJSON(request.OutputConfig.TaskBudget) {
		add("task-budgets-2026-03-13")
	}
	if string(request.Speed) == `"fast"` {
		add("fast-mode-2026-02-01")
	}
	var fallbacks []json.RawMessage
	if string(request.Fallbacks) == `"default"` {
		add("server-side-fallback-2026-07-01")
	} else if json.Unmarshal(request.Fallbacks, &fallbacks) == nil && len(fallbacks) > 0 {
		add("server-side-fallback-2026-06-01")
	}
	if isPresentJSON(request.Thinking.BlockBinding) {
		add("thinking-binding-controls-2026-08-01")
	}
	for _, tool := range request.Tools {
		if tool.Type == "" || tool.Type == "custom" {
			if anthropicSupportsStructuredOutput(request.Model) {
				add("structured-outputs-2025-11-13")
			}
			if isPresentJSON(tool.InputExamples) || isPresentJSON(tool.AllowedCallers) {
				add("advanced-tool-use-2025-11-20")
			}
		} else if beta := v1AnthropicToolBetas[tool.Type]; beta != "" {
			add(beta)
		}
	}
	return strings.Join(betas, ",")
}

func isPresentJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func anthropicSupportsStructuredOutput(model string) bool {
	for _, supported := range []string{
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-fable-5", "claude-sonnet-5",
		"claude-sonnet-4-6", "claude-opus-4-6", "claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5",
		"claude-opus-4-1",
	} {
		if strings.Contains(model, supported) {
			return true
		}
	}
	if strings.Contains(model, "claude-sonnet-4-") || strings.Contains(model, "claude-opus-4-") ||
		strings.Contains(model, "claude-3-haiku") || claude3OrOlderPattern.MatchString(model) {
		return false
	}
	return strings.Contains(model, "claude-")
}
