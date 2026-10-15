package relay

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/kingsunb/NovaVeil/internal/model"
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

// knownOpencodeTool 返回工具名的真实定义; 不在核心工具集中返回 false。
func knownOpencodeTool(name string) (opencodeToolDef, bool) {
	loadOpencodeTools()
	def, ok := opencodeTools[name]
	return def, ok
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

// bodyModelName 从请求体提取 model 字段; 空串表示无法识别(不改写)。
func bodyModelName(body []byte) string {
	return gjson.GetBytes(body, "model").String()
}

// shapeOpencodeFreeBody 把免费档的 Chat 请求体规范化成 Agent 形态:
// 强制 stream=true、补齐 stream_options.include_usage、注入缺失的核心工具名。
// 已满足全部条件或不是 JSON 对象的正文原样返回。第二个返回值表示是否发生了改写;
// 调用方据此判定折叠需要: 客户端原非流式且改写把 stream 置真时, 上游返回 SSE 须折叠回 JSON。
// 使用 sjson/gjson 做点状改写而非整体 Marshal, 保持既有工具与消息字段的字节原样。
func shapeOpencodeFreeBody(body []byte) ([]byte, bool) {
	out := body
	changed := false

	if !gjson.GetBytes(out, "stream").Bool() {
		if next, err := sjson.SetBytes(out, "stream", true); err == nil {
			out, changed = next, true
		}
	}
	// OpenAI Chat 流式须显式声明 include_usage 才会在末尾带 usage 帧。
	if !gjson.GetBytes(out, "stream_options.include_usage").Bool() {
		if next, err := sjson.SetBytes(out, "stream_options.include_usage", true); err == nil {
			out, changed = next, true
		}
	}
	if next, injected := ensureOpencodeFreeTools(out); injected {
		out, changed = next, true
	}
	return out, changed
}

// ensureOpencodeFreeTools 确保核心工具存在且定义真实, 报告是否改写。
//   - 客户端已声明的同名核心工具: 替换为真实定义(上游逐字节比对, 客户端自带的不匹配会 403)。
//   - 缺失的核心工具: 追加真实定义。
//   - 非核心工具原样保留。
//
// tools 字段存在但不是数组时视为客户端输入异常, 不改写。
func ensureOpencodeFreeTools(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, false
	}

	changed := false
	present := make(map[string]bool, len(opencodeFreeCoreTools))
	items := make([]string, 0, len(tools.Array())+len(opencodeFreeCoreTools))

	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" {
			items = append(items, t.Raw)
			continue
		}
		present[name] = true
		// 同名核心工具: 仅在 description 与真实定义不一致时替换。上游逐字节比对
		// description/parameters, 客户端自带的定义几乎不可能恰好匹配。已一致的
		// 不替换, 保持幂等。
		if def, known := knownOpencodeTool(name); known {
			if t.Get("function.description").String() != def.Description {
				items = append(items, opencodeFreeChatTool(name))
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
			items = append(items, opencodeFreeChatTool(name))
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

// opencodeFreeChatTool 生成 Chat 协议的工具定义。核心工具使用 embed 的真实定义
// (description + parameters); 未知工具降级为占位(上游不识别, 不影响校验)。
func opencodeFreeChatTool(name string) string {
	description := "Agent tool " + name
	parameters := map[string]any{"type": "object", "properties": map[string]any{}}
	if def, ok := knownOpencodeTool(name); ok {
		description = def.Description
		if def.Parameters != nil {
			parameters = def.Parameters
		}
	}
	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters":  parameters,
		},
	})
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