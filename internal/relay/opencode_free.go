package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenCode Zen 免费档只接受「Agent 形态」的流式请求: stream=true 且请求体携带
// bash/edit/glob/grep/read 这五个核心工具名, 否则一律 403 FreeTierError。本文件
// 在连线上游前把免费模型的请求规范化成该形态, 并在客户端原本是非流式时把上游
// 折叠出的 SSE 聚合回 JSON。行为对齐 opencode2api 的匿名/免费档请求改写, 不复用其实现。

// opencodeFreeCoreTools 是免费档判定 Agent 形态所需的核心工具名。缺任一即被上游拒绝;
// 判定只看名字, 工具的 description/parameters 内容无关。
var opencodeFreeCoreTools = []string{"bash", "edit", "glob", "grep", "read"}

// isOpencodeFreeModel 判定该渠道上的模型是否落在 OpenCode Zen 免费档:
// 渠道 BaseURL 是 /zen(且不含 /zen/go, 见 model.OpenCodeTier 的 Go 先判), 并且
// 渠道是内置免费渠道, 或模型名含 "free" 子串(不区分大小写, 对齐 opencode2api
// 的 isFreeModel 兜底)。Go 档(/zen/go)是付费档, 不做改写。
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

// ensureOpencodeFreeTools 追加缺失的核心工具定义, 报告是否改写。已声明的工具原样保留;
// tools 字段存在但不是数组时视为客户端输入异常, 不改写(与上游拒绝口径一致)。
func ensureOpencodeFreeTools(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body, false
	}

	present := make(map[string]bool, len(opencodeFreeCoreTools))
	if tools.IsArray() {
		for _, t := range tools.Array() {
			if name := t.Get("function.name").String(); name != "" {
				present[name] = true
			}
		}
	}
	missing := make([]string, 0, len(opencodeFreeCoreTools))
	for _, name := range opencodeFreeCoreTools {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return body, false
	}

	items := make([]string, 0, len(tools.Array())+len(missing))
	for _, t := range tools.Array() {
		items = append(items, t.Raw)
	}
	for _, name := range missing {
		items = append(items, opencodeFreeChatTool(name))
	}
	next, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return next, true
}

// opencodeFreeChatTool 合成一个 Chat 协议的最小工具定义; 名字来自固定白名单,
// 无注入风险。description/parameters 仅为满足上游校验的占位, 不对客户端可见。
func opencodeFreeChatTool(name string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": "Agent tool " + name,
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
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