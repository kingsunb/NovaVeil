package relay

import (
	"sort"
	"strings"
	"time"

	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// maskEngine 进程级脱敏引擎, 内置会话映射表随会话粘合过期回收。
// 开关关闭时 applyRequestMask 跳过正则与还原, 但仍先读取并解析一次配置(见 op.MaskConfigGet)。
var maskEngine = mask.NewEngine(mask.NewSessionStore())

// maskSessionStore 引擎内置会话映射表, 供请求结束后回收映射防止内存泄漏。
var maskSessionStore = maskEngine.SessionStore()

// requestMask 是转发路径的脱敏入口。生产实现是 applyRequestMask;
// 测试可替换, 用来覆盖脱敏失败与 panic 时的定稿和预算释放。
var requestMask = applyRequestMask

// applyRequestMask 对请求体执行脱敏, 返回脱敏后字节、映射表与命中明细。
// 全局开关或分组开关任一关闭时直接原样返回, 映射表为 nil, 命中明细为 nil; 关闭路径仍先读取/解析配置, 仅省去正则与还原开销(文档 01 §二、脱敏 README §八.4)。
// 开关均开但未命中任何敏感信息时同样返回 nil 映射与 nil 明细: 占位符未插入, 响应不会含占位符,
// 还原为 no-op, 调用方据此跳过脱敏标记, 避免对无敏感内容的请求误标"已脱敏"。
// 脱敏异常时返回 error, 调用方须走 fail-closed 拒绝请求, 绝不放行明文(文档 05 §八)。
// 命中明细只属于当前请求这一次 Apply 的结果, 按引擎返回顺序(已按唯一原文去重保序)原样使用(文档 07 §3.1)。
func applyRequestMask(body []byte, sessionKey string, groupMaskEnabled bool) ([]byte, *mask.Mapping, []mask.Match, error) {
	cfg, err := op.MaskConfigGet()
	if err != nil {
		// 配置读取失败按 fail-closed 处理: 若全局开关本就关着则无需阻断, 但无法判定故拒绝。
		return nil, nil, nil, err
	}
	if !cfg.Enabled || !groupMaskEnabled {
		return body, nil, nil, nil // 短路: 任一开关关即跳过, 零正则开销
	}
	terms := make([]mask.CustomTerm, 0, len(cfg.CustomTerms))
	for _, t := range cfg.CustomTerms {
		if v := strings.TrimSpace(t.Value); v != "" {
			terms = append(terms, mask.CustomTerm{Value: v, Category: t.Category})
		}
	}
	res, err := maskEngine.Apply(string(body), sessionKey, cfg.BuiltinRuleSwitch, terms)
	if err != nil {
		return nil, nil, nil, err
	}
	// 判定「是否真的脱敏」以命中明细为准, 而非字节是否变化: JSON 请求体经 mapJSONStringValues
	// 重序列化, 即使零命中也可能改变字节(去空格、HTML 转义等), 若按 bytes.Equal 比较会误标
	// 「已脱敏」却无任何命中明细, 并悄悄改写请求体格式(文档 07)。
	// 零命中时原样透传原始字节, 映射表与命中明细均为 nil, 调用方据此跳过脱敏标记与还原器。
	if len(res.Matches) == 0 {
		return body, nil, nil, nil
	}
	return []byte(res.Masked), res.Mapping, res.Matches, nil
}

// 命中明细裁剪上限(文档 07 §3.1 第 5 点硬约束, design §2.3.1)。
// 命中原文是被批准下发的敏感片段, 但必须约束为有界摘要, 避免超大请求体导致
// 命中明细(含原文)随 SSE/落库扩散失控。这些常量作为 relay 层唯一上限来源,
// op 层(错误日志)另有等价常量做 defense-in-depth, 两处数值必须同步。
const (
	maxMaskMatchesPerRequest     = 128       // 单请求命中明细条数上限, 约束 SSE 载荷与 DOM 节点数。
	maxMaskMatchLabelBytes       = 32        // 单条 label 字节上限(safeLabel 已截 12, 32 宽裕)。
	maxMaskMatchOriginalBytes    = 256       // 单条命中原文字节上限, 足够定位 MAC/USCC/PHONE/EMAIL/密钥片段, 避免整段密文泄漏。
	maxMaskMatchPlaceholderBytes = 64        // 单条占位符字节上限。
	maxMaskMatchesTotalBytes     = 32 * 1024 // 命中明细总字节硬上限。
)

// truncateMaskMatches 把引擎命中明细按条数/字节上限裁剪为状态流的命中明细形状,
// 返回裁剪后的 []MaskMatch 与是否发生截断的布尔标记(文档 07 §3.1 第 5 点、design §2.1.3)。
//
// 决策变更(文档 07): 已批准下发命中原文 original, 连同 label + placeholder 一并透传，
// 但仅透传裁剪后的有界摘要, 超限仅裁剪展示、不改变实际脱敏与还原。
//
// 行为契约:
//   - 空输入返回 (nil, false), 使 RequestState.MaskMatches 在无命中时不进 JSON(omitempty);
//   - 按引擎返回顺序保序遍历; 对每条 label/original/placeholder 分别按 UTF-8 边界截到各自上限;
//   - 累计条数或总字节任一超过上限即停止追加后续并置 truncated=true;
//   - 只读引擎结果, 不触碰 res.Mapping / masked / StreamRestorer, 与「实际替换/还原」解耦。
func truncateMaskMatches(matches []mask.Match) ([]MaskMatch, bool) {
	if len(matches) == 0 {
		return nil, false
	}
	result := make([]MaskMatch, 0, min(len(matches), maxMaskMatchesPerRequest))
	total := 0
	truncated := false
	for _, m := range matches {
		if len(result) >= maxMaskMatchesPerRequest {
			truncated = true
			break
		}
		label := truncateUTF8Bytes(m.Label, maxMaskMatchLabelBytes)
		original := truncateUTF8Bytes(m.Original, maxMaskMatchOriginalBytes)
		placeholder := truncateUTF8Bytes(m.Placeholder, maxMaskMatchPlaceholderBytes)
		itemBytes := len(label) + len(original) + len(placeholder)
		if total+itemBytes > maxMaskMatchesTotalBytes {
			truncated = true
			break
		}
		result = append(result, MaskMatch{Label: label, Original: original, Placeholder: placeholder})
		total += itemBytes
	}
	return result, truncated
}

// restoreNonStream 对非流式响应体执行占位符还原。
// 映射表为 nil 时原样返回, no-op。
func restoreNonStream(body []byte, mapping *mask.Mapping) []byte {
	if mapping == nil {
		return body
	}
	return mask.RestoreBytes(body, mapping)
}

// restoreStreamEvent 对单个 SSE 事件的增量文本执行占位符还原。
//
// 按客户端协议提取增量文本字段, 用 StreamRestorer.Push 做跨 chunk 缓冲拼接还原后写回。
// sjson.SetBytes 自动处理 JSON 转义, 保证还原后的 JSON 结构合法。
// reasoning_content 使用独立通道 "reasoning" 缓冲, 避免与 content 通道的 pending 混合。
// tool_calls.N.function.arguments 按槽位独立缓冲还原(跨 chunk 拆分的占位符可闭合)。
// 中途无内容返回原 data(不写出空事件, 避免断流)。
func restoreStreamEvent(data []byte, format llm.APIFormat, restorer *mask.StreamRestorer) []byte {
	if restorer == nil {
		return data
	}
	// content / text 主文本字段: 使用默认通道。
	if path := streamContentPath(data, format); path != "" {
		text := gjson.GetBytes(data, path).String()
		if text != "" {
			restored := restorer.Push([]byte(text))
			if len(restored) == 0 {
				// 全部缓冲为 pending, 内容置空等后续 chunk 拼合; 事件本身仍写出(可能含 finish_reason 等)。
				out, _ := sjson.SetBytes(data, path, "")
				return out
			}
			out, _ := sjson.SetBytes(data, path, string(restored))
			data = out
		}
	}
	// reasoning_content: DeepSeek/Qwen 等推理字段, 使用独立通道避免与 content pending 混合。
	if rcPath := streamReasoningPath(data, format); rcPath != "" {
		text := gjson.GetBytes(data, rcPath).String()
		if text != "" {
			restored := restorer.PushChannel(mask.ReasoningChannel, []byte(text))
			if len(restored) == 0 {
				out, _ := sjson.SetBytes(data, rcPath, "")
				return out
			}
			out, _ := sjson.SetBytes(data, rcPath, string(restored))
			data = out
		}
	}
	// tool-call arguments: 按槽位独立缓冲还原。
	data = restoreToolCallArgs(data, format, restorer)
	return data
}

// restoreToolCallArgs 还原 OpenAI Chat 流式 tool_calls 的 function.arguments 占位符。
// arguments 是 JSON 字符串增量, 按 tool-call index 使用独立通道缓冲, 使跨 chunk 拆分的占位符可闭合。
func restoreToolCallArgs(data []byte, format llm.APIFormat, restorer *mask.StreamRestorer) []byte {
	if format != llm.APIFormatOpenAIChatCompletion {
		return data
	}
	toolCalls := gjson.GetBytes(data, "choices.0.delta.tool_calls")
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return data
	}
	arr := toolCalls.Array()
	for i := range arr {
		argPath := "choices.0.delta.tool_calls." + itoa(i) + ".function.arguments"
		argVal := gjson.GetBytes(data, argPath)
		if !argVal.Exists() || argVal.Type != gjson.String {
			continue
		}
		s := argVal.String()
		if s == "" {
			continue
		}
		// 按 tool-call index 独立通道缓冲, 跨 chunk 拆分的占位符可闭合。
		channel := mask.ToolChannelPrefix + itoa(i)
		restored := restorer.PushChannel(channel, []byte(s))
		if string(restored) != s {
			out, _ := sjson.SetBytes(data, argPath, string(restored))
			data = out
		}
	}
	return data
}

// itoa 轻 int → string, 避免 fmt.Sprintf 开销。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// flushStreamRestorer 在流终止时调用, 返回各通道残留 pending 的 SSE 事件 data(已按协议包装)。
// 无残留返回 nil, 调用方跳过。残留只含未闭合前缀(非占位符), 原样输出让客户端可见。
// 默认通道残留写入正文 delta, reasoning/tool-N 通道分别写回对应字段, 绝不混写。
func flushStreamRestorer(restorer *mask.StreamRestorer, format llm.APIFormat) [][]byte {
	if restorer == nil {
		return nil
	}
	pending := restorer.FlushChannels()
	if len(pending) == 0 {
		return nil
	}
	// 出队顺序确定性: default → reasoning → tool-N(按槽位升序)。
	channels := make([]string, 0, len(pending))
	for ch := range pending {
		channels = append(channels, ch)
	}
	sort.Strings(channels)
	out := make([][]byte, 0, len(channels))
	for _, ch := range channels {
		if ch == mask.DefaultChannel {
			data := flushChannelData(format, ch, string(pending[ch]))
			if data != nil {
				out = append(out, data)
			}
			continue
		}
		// 非默认通道只有 OpenAI Chat 协议支持(reasoning_content / tool_calls)。
		if format != llm.APIFormatOpenAIChatCompletion {
			continue
		}
		switch {
		case ch == mask.ReasoningChannel:
			out = append(out, mustSetJSONBytes(nil, "choices.0.delta.reasoning_content", string(pending[ch])))
		case strings.HasPrefix(ch, mask.ToolChannelPrefix):
			idx := strings.TrimPrefix(ch, mask.ToolChannelPrefix)
			out = append(out, mustSetJSONBytes(nil, "choices.0.delta.tool_calls."+idx+".function.arguments", string(pending[ch])))
		}
	}
	return out
}

// flushChannelData 把单通道残留包装为协议对应的内容 delta 事件。默认通道外的字段由
// flushStreamRestorer 在 OpenAI Chat 分支单独构造。
func flushChannelData(format llm.APIFormat, _ string, remain string) []byte {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return mustSetJSONBytes(nil, "choices.0.delta.content", remain)
	case llm.APIFormatAnthropicMessage:
		return mustSetJSONBytes(nil, "delta.text", remain)
	case llm.APIFormatOpenAIResponse:
		return mustSetJSONBytes(nil, "delta", remain)
	}
	return nil
}

// mustSetJSONBytes 与 sjson.SetBytes 同义, 仅在纯内存构造的 nil root 上使用,
// 错误永远不会发生; 返回 nil 仅防御未来意外。便于 flush 路径保持零日志开销。
func mustSetJSONBytes(root []byte, path, value string) []byte {
	out, _ := sjson.SetBytes(root, path, value)
	return out
}

// pruneMaskSessions 回收超过 TTL 未访问的脱敏会话映射。
func pruneMaskSessions() {
	maskSessionStore.PruneExpired(time.Now())
}

// streamContentPath 按协议返回 SSE 事件中增量正文文本字段的 gjson 路径, 无匹配返回空。
func streamContentPath(data []byte, format llm.APIFormat) string {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		if gjson.GetBytes(data, "choices.0.delta.content").Exists() {
			return "choices.0.delta.content"
		}
	case llm.APIFormatAnthropicMessage:
		if gjson.GetBytes(data, "delta.text").Exists() {
			return "delta.text"
		}
	case llm.APIFormatOpenAIResponse:
		// response.output_text.delta 事件的 delta 是字符串增量
		if gjson.GetBytes(data, "delta").Type == gjson.String {
			return "delta"
		}
	}
	return ""
}

// streamReasoningPath 返回 SSE 事件中推理增量字段的 gjson 路径, 无匹配返回空。
// DeepSeek/Qwen 等模型在 choices.0.delta.reasoning_content 中输出推理过程,
// 脱敏占位符可能出现在该字段中, 须与 content 独立缓冲还原。
func streamReasoningPath(data []byte, format llm.APIFormat) string {
	if format == llm.APIFormatOpenAIChatCompletion {
		if gjson.GetBytes(data, "choices.0.delta.reasoning_content").Exists() {
			return "choices.0.delta.reasoning_content"
		}
	}
	return ""
}
