package relay

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// convTraceEnabled 进程级缓存: 协议转换追踪是否开启。
// 由 convTraceOn() 在每次跨协议转换前查询设置缓存(in-memory O(1)),
// 关闭时仅一次缓存查询即短路, 不产生任何额外分配或日志 IO。
//
// 性能模型(对齐脱敏 mask_integration.go 的短路模式):
//   - 关闭: 1 次 settingCache.Get → false → return, 零分配零 IO
//   - 开启: 1 次 cache 查询 + 请求体结构分析 + 转换前后字段对比 + Debug 日志

// convTraceOn 报告协议转换追踪是否开启。设置读取失败时按关闭处理(安全默认)。
func convTraceOn() bool {
	v, err := op.SettingGetBool(model.SettingKeyConvTrace)
	if err != nil {
		return false
	}
	return v
}

// ConvTrace 记录一次跨协议转换的追踪信息, 供 Debug 日志输出。
type ConvTrace struct {
	Source      string        // 客户端协议(展示名)
	Target      string        // 上游渠道协议(展示名)
	ChannelID   int           // 渠道 ID
	ChannelName string        // 渠道名称
	InputBytes  int           // 转换前请求体大小
	OutputBytes int           // 转换后请求体大小
	Elapsed     time.Duration // 转换耗时
	Diagnostics []ConvDiag    // 降级/丢失字段诊断
}

// ConvDiag 单条转换诊断: 转换过程中某字段被降级、丢弃或剥离。
type ConvDiag struct {
	Status string // "degraded" | "unsupported" | "stripped"
	Field  string // 涉及的字段名
	Detail string // 人类可读说明
}

// beginConvTrace 在转换前记录请求体结构分析并返回 ConvTrace 骨架。
// 仅在追踪开启时调用; 关闭时调用方直接短路, 不会进入此函数。
func beginConvTrace(format llm.APIFormat, channel model.Channel, inputBody []byte) *ConvTrace {
	ct := &ConvTrace{
		Source:      clientFormatLabel(format),
		Target:      upstreamTypeLabel(channel, nil),
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		InputBytes:  len(inputBody),
	}
	logRequestBodyStructure(inputBody, format)
	return ct
}

// finishConvTrace 在转换完成后填充输出信息、检测降级并输出 Debug 日志。
// err != nil 时记录转换失败日志; 否则检测字段降级并记录成功转换日志。
func finishConvTrace(ct *ConvTrace, outputBody []byte, err error) {
	if ct == nil {
		return
	}
	if err != nil {
		log.Debugf("conv trace FAIL %s→%s ch=%d(%s) in=%d out=0 elapsed=%v err=%v",
			ct.Source, ct.Target, ct.ChannelID, ct.ChannelName,
			ct.InputBytes, ct.Elapsed, err)
		return
	}
	ct.OutputBytes = len(outputBody)
	ct.Diagnostics = detectDiagnostics(ct.Source, ct.Target, outputBody)
	diagStr := "none"
	if len(ct.Diagnostics) > 0 {
		parts := make([]string, 0, len(ct.Diagnostics))
		for _, d := range ct.Diagnostics {
			parts = append(parts, fmt.Sprintf("%s(%s:%s)", d.Status, d.Field, d.Detail))
		}
		diagStr = strings.Join(parts, ", ")
	}
	log.Debugf("conv trace OK %s→%s ch=%d(%s) in=%d out=%d elapsed=%v diags=%s",
		ct.Source, ct.Target, ct.ChannelID, ct.ChannelName,
		ct.InputBytes, ct.OutputBytes, ct.Elapsed, diagStr)
}

// logRequestBodyStructure 在转换前分析请求体结构, 对已知的不支持模式提前告警。
func logRequestBodyStructure(body []byte, format llm.APIFormat) {
	// Responses API 的 previous_response_id 模式: 无 input 字段, 无法转换为 Chat Completions
	if format == llm.APIFormatOpenAIResponse {
		hasPrevID := gjson.GetBytes(body, "previous_response_id").Exists()
		hasInput := gjson.GetBytes(body, "input").Exists()
		if hasPrevID && !hasInput {
			log.Warnf("conv trace: Responses 请求使用 previous_response_id(无 input), 转换到 Chat Completions 时上下文将丢失")
		}
	}

	// 记录顶层字段(帮助调试转换前后字段差异)
	topKeys := topLevelKeys(body)
	if len(topKeys) > 0 {
		log.Debugf("conv trace: 请求体顶层字段 %v", topKeys)
	}

	// 检测 reasoning_content: 转换到不支持推理的协议时可能丢失
	if hasReasoningContent(body) {
		log.Debug("conv trace: 请求携带 reasoning_content, 转换到不支持推理的协议时可能降级")
	}
}

// detectDiagnostics 检测转换后请求体中可能丢失或降级的字段。
// input 为原始客户端请求体, output 为转换后的上游请求体。
// 通过对比转换前后是否存在关键字段来推断降级情况。
//
// 注意: 此处仅检测输出体缺失的输入体字段, 不检测新增字段(转换器按目标协议
// 规范化新增的字段是预期行为, 不构成降级)。
func detectDiagnostics(source, target string, output []byte) []ConvDiag {
	// 同协议不产生诊断
	if source == target {
		return nil
	}
	var diags []ConvDiag

	// reasoning_content 丢失: 输入有但输出没有
	if !gjson.GetBytes(output, "reasoning_content").Exists() &&
		!gjson.GetBytes(output, "reasoning").Exists() {
		// 仅在源协议可能携带推理内容时报告
		if source == "anthropic" || source == "openai_chat" {
			diags = append(diags, ConvDiag{
				Status: "degraded",
				Field:  "reasoning_content",
				Detail: "推理内容在目标协议中无对应字段",
			})
		}
	}

	// tool_choice 丢失
	if !gjson.GetBytes(output, "tool_choice").Exists() {
		diags = append(diags, ConvDiag{
			Status: "unsupported",
			Field:  "tool_choice",
			Detail: "目标协议不支持 tool_choice",
		})
	}

	// thinking / reasoning 配置丢失(Anthropic → 其他)
	if !gjson.GetBytes(output, "thinking").Exists() &&
		!gjson.GetBytes(output, "reasoning").Exists() &&
		!gjson.GetBytes(output, "reasoning_effort").Exists() {
		if source == "anthropic" {
			diags = append(diags, ConvDiag{
				Status: "degraded",
				Field:  "thinking",
				Detail: "思考配置在目标协议中无对应字段",
			})
		}
	}

	return diags
}

// topLevelKeys 返回 JSON 请求体的顶层字段名列表(用于调试日志)。
func topLevelKeys(body []byte) []string {
	obj := gjson.GetBytes(body, "@keys")
	if !obj.IsArray() {
		return nil
	}
	arr := obj.Array()
	keys := make([]string, 0, len(arr))
	for _, k := range arr {
		if k.Type == gjson.String {
			keys = append(keys, k.Str)
		}
	}
	return keys
}

// hasReasoningContent 报告请求体中是否携带推理内容。
func hasReasoningContent(body []byte) bool {
	// OpenAI Chat: messages[].reasoning_content
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		if msg.Get("reasoning_content").Exists() {
			return true
		}
	}
	// Anthropic: content[].type == "thinking"
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range msg.Get("content").Array() {
			if block.Get("type").String() == "thinking" {
				return true
			}
		}
	}
	// 顶层 reasoning / reasoning_effort
	if gjson.GetBytes(body, "reasoning").Exists() || gjson.GetBytes(body, "reasoning_effort").Exists() {
		return true
	}
	return false
}
