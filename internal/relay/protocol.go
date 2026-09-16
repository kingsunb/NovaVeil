package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/gjson"
)

// upstreamPath 返回客户端协议在标准上游中对应的请求路径。
func upstreamPath(format llm.APIFormat) string {
	switch format {
	case llm.APIFormatOpenAIResponse:
		return "/responses"
	case llm.APIFormatAnthropicMessage:
		return "/messages"
	case llm.APIFormatOpenAIImageGeneration:
		return "/images/generations"
	case llm.APIFormatOpenAIImageEdit:
		return "/images/edits"
	case llm.APIFormatOpenAIImageVariation:
		return "/images/variations"
	case llm.APIFormatOpenAIVideo:
		return "/video/generations"
	case llm.APIFormatOpenAISpeech:
		return "/audio/speech"
	case llm.APIFormatOpenAITranscription:
		return "/audio/transcriptions"
	case llm.APIFormatOpenAITranslation:
		return "/audio/translations"
	case llm.APIFormatOpenAIEmbedding:
		return "/embeddings"
	default:
		return "/chat/completions"
	}
}

// isChatFormat 报告 APIFormat 是否为对话类协议(chat/responses/anthropic),
// 对话类协议需要用量校验、终止原因白名单等响应级验证; 非对话类(图片/视频/语音/嵌入)
// 同协议透传时原样返回上游响应, 不做协议级校验。
func isChatFormat(format llm.APIFormat) bool {
	switch format {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatAnthropicMessage:
		return true
	default:
		return false
	}
}

// multipartFormField 从 multipart/form-data 请求体中提取指定表单字段的值。
// 用于图片编辑/变体、语音转写/翻译等 multipart 接口的路由选组: 这些接口的 model
// 字段在 form-data 中而非 JSON body。非 multipart 内容类型或字段不存在时返回空字符串。
func multipartFormField(contentType string, body []byte, field string) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return ""
	}
	boundary := params["boundary"]
	if boundary == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return ""
		}
		if part.FormName() == field {
			buf := make([]byte, 256)
			n, _ := part.Read(buf)
			return strings.TrimSpace(string(buf[:n]))
		}
	}
}

// buildPassthroughRequest 构造同协议透传的上游请求: 目标地址, 认证和请求体由渠道决定, 客户端的其余请求头和查询参数
// 经 MergeInboundRequest 透传给上游, 其中认证类, 库自管类和逐跳类请求头会被丢弃以免覆盖渠道凭据。
// 无密钥渠道(PrimaryKey 为空)不构造任何认证: Auth 保持 nil 时 FinalizeAuthHeaders 直接跳过认证头写入,
// 上游收到的是完全免认证的请求。
// randomValue 为请求级一次性解析的随机头值, 透传给 applyChannelConfig 注入会话级动态头。
func buildPassthroughRequest(format llm.APIFormat, raw *httpclient.Request, channel model.Channel, randomValue string) (*httpclient.Request, error) {
	// BaseURL 以 ## 结尾表示地址已完整, 不再追加版本号和协议路径。
	base := strings.TrimSuffix(channel.BaseURL, "##")
	rawURL := base != channel.BaseURL

	var path string
	if channel.PassThroughBodyEnabled && !rawURL {
		// 完全渠道透传: 使用客户端原始请求路径, 上游收到同样的 URL。
		// raw.Path 形如 "/v1/chat/completions", 剥去 "/v1" 前缀后交给 BuildRequestURL 拼接。
		path = strings.TrimPrefix(raw.Path, "/v1")
		if path == "" || path == raw.Path {
			// 路径不以 /v1 开头, 回退到格式映射路径。
			path = upstreamPath(format)
		}
	} else {
		path = upstreamPath(format)
	}
	url := transformer.BuildRequestURL(base, "v1", path, "", rawURL)

	var auth *httpclient.AuthConfig
	if channel.PrimaryKey() != "" {
		auth = &httpclient.AuthConfig{Type: httpclient.AuthTypeBearer, APIKey: channel.Key}
		if format == llm.APIFormatAnthropicMessage {
			auth = &httpclient.AuthConfig{Type: httpclient.AuthTypeAPIKey, APIKey: channel.Key, HeaderKey: "X-API-Key"}
		}
	}
	// Content-Type 属于库自管头, 不会随客户端请求透传, 需按客户端原值显式重建。
	contentType := raw.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	request := httpclient.MergeInboundRequest(&httpclient.Request{
		Method:    raw.Method,
		URL:       url,
		Headers:   http.Header{"Content-Type": []string{contentType}},
		Body:      raw.Body,
		Auth:      auth,
		APIFormat: format.String(),
	}, raw)
	request, err := httpclient.FinalizeAuthHeaders(request)
	if err != nil {
		return nil, err
	}
	if err := applyChannelConfig(channel, request, randomValue); err != nil {
		return nil, err
	}
	return request, nil
}

// streamEventVerdict 单次解析得到的一个客户端协议流事件的全量判定结果。
// 窗口预读与转发循环此前对同一事件各解 2-3 次(inspect/abnormal/hasContent 各自解码),
// 长流下是转发协程的主要 CPU 开销; 统一由 analyzeStreamEvent 一次解码派生全部结论。
type streamEventVerdict struct {
	terminal     bool  // 事件是否结束整个响应流。
	inspectErr   error // 事件本身即失败(流内错误帧/解码失败/类型错乱)时给出错误。
	abnormalErr  error // 事件携带白名单之外的异常终止原因, 不可交付。
	hasContent   bool  // 事件是否承载实际输出内容(文本/推理/工具调用)。
	hasAnswer    bool  // 事件是否承载最终回答信号(文本增量/工具调用), 推理内容不算。
	hasReasoning bool  // 事件是否承载推理内容信号(reasoning_content)。
	stopFinish   bool  // 任一 choice 携带 finish_reason=="stop"(自然终止); 仅 OpenAI Chat 判定。
	finishSeen   bool  // 上游已声明生成完整结束: OpenAI Chat 为白名单内的非空 finish_reason;
	//               Anthropic 为 message_delta 携带的白名单内非空 stop_reason。
	//               用于区分"缺终止哨兵([DONE]/message_stop)的完整响应"与"真截断",
	//               避免把完整响应误判为提前关闭导致客户端无谓重试(断流)。
}

// analyzeStreamEvent 按客户端协议解析一个流事件并一次性给出全部判定。
// 三种协议各自只做一次解码: OpenAI Chat 解码到轻量探针结构(仅抽取判定所需字段,
// 大文本字段以 json.RawMessage 引用原文字节, 不产生拷贝), Anthropic/Responses 直接
// 解码到协议事件结构。语义与历史版本的三个独立判定函数保持一致。
func analyzeStreamEvent(format llm.APIFormat, event *httpclient.StreamEvent) streamEventVerdict {
	if event == nil || len(event.Data) == 0 {
		return streamEventVerdict{}
	}

	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return analyzeChatStreamEvent(event)

	case llm.APIFormatAnthropicMessage:
		var parsed anthropic.StreamEvent
		if err := json.Unmarshal(event.Data, &parsed); err != nil {
			return streamEventVerdict{terminal: true, inspectErr: fmt.Errorf("decode anthropic stream event: %w", err)}
		}
		verdict := streamEventVerdict{}
		if parsed.Type == "" {
			verdict.terminal = true
			verdict.inspectErr = errors.New("anthropic stream event type is empty")
			return verdict
		}
		// SSE 事件名与正文类型不一致说明流已错乱, 不能继续按协议解析。
		if event.Type != "" && event.Type != parsed.Type {
			verdict.terminal = true
			verdict.inspectErr = fmt.Errorf("anthropic stream event type mismatch: %s != %s", event.Type, parsed.Type)
			return verdict
		}
		if parsed.Type == "message_stop" {
			verdict.terminal = true
			return verdict
		}
		if parsed.Type == "error" {
			// 错误帧在流中至多出现一次, 保留既有二次解码提取错误详情。
			var failure anthropic.AnthropicError
			if err := json.Unmarshal(event.Data, &failure); err != nil {
				verdict.terminal = true
				verdict.inspectErr = fmt.Errorf("decode anthropic stream error: %w", err)
				return verdict
			}
			if failure.Error.Message == "" {
				failure.Error.Message = "anthropic stream error"
			}
			verdict.terminal = true
			verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Message: failure.Error.Message, Type: failure.Error.Type, RequestID: failure.RequestID}}
			return verdict
		}
		switch parsed.Type {
		case "content_block_start", "content_block_delta":
			verdict.hasContent = true
		}
		// message_delta 携带 stop_reason 时应用白名单(与窗口期/转发期判定共用)。
		if parsed.Type == "message_delta" && parsed.Delta != nil && parsed.Delta.StopReason != nil {
			if !validAnthropicStopReason(*parsed.Delta.StopReason) {
				verdict.abnormalErr = fmt.Errorf("%w: stop_reason %q", errAbnormalFinish, *parsed.Delta.StopReason)
			} else if *parsed.Delta.StopReason != "" {
				// 非空且白名单内的 stop_reason = 上游已声明生成完整结束( analogous to OpenAI
				// Chat 的 finish_reason)。部分 Anthropic 兼容上游(第三方代理/网关)发完
				// message_delta 直接关流、不发 message_stop 哨兵, 该信号用于区分"缺哨兵的
				// 完整响应"与"真截断", 避免把完整响应误判为提前关闭导致客户端无谓重试(断流)。
				verdict.finishSeen = true
			}
		}
		return verdict

	case llm.APIFormatOpenAIResponse:
		var parsed responses.StreamEvent
		if err := json.Unmarshal(event.Data, &parsed); err != nil {
			return streamEventVerdict{terminal: true, inspectErr: fmt.Errorf("decode responses stream event: %w", err)}
		}
		verdict := streamEventVerdict{}
		switch parsed.Type {
		case responses.StreamEventTypeResponseCompleted:
			// 已完成事件仍可能携带非 completed 的终态, 需按 status 与 error 区分成败。
			if parsed.Response == nil || parsed.Response.Status == nil || *parsed.Response.Status == "" || *parsed.Response.Status == "completed" {
				verdict.terminal = true
				return verdict
			}
			verdict.terminal = true
			verdict.abnormalErr = fmt.Errorf("%w: response status %q", errAbnormalFinish, *parsed.Response.Status)
			if parsed.Response.Error != nil {
				verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Code: parsed.Response.Error.Code, Message: parsed.Response.Error.Message, Type: parsed.Response.Error.Type}}
			} else {
				verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response " + *parsed.Response.Status, Type: "response_" + *parsed.Response.Status}}
			}
			return verdict
		case responses.StreamEventTypeResponseFailed:
			verdict.terminal = true
			verdict.abnormalErr = fmt.Errorf("%w: responses event %s", errAbnormalFinish, parsed.Type)
			if parsed.Response != nil && parsed.Response.Error != nil {
				verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Code: parsed.Response.Error.Code, Message: parsed.Response.Error.Message, Type: parsed.Response.Error.Type}}
			} else {
				verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response failed", Type: "response_failed"}}
			}
			return verdict
		case responses.StreamEventTypeResponseIncomplete:
			verdict.terminal = true
			verdict.abnormalErr = fmt.Errorf("%w: responses event %s", errAbnormalFinish, parsed.Type)
			message := "response incomplete"
			if parsed.Response != nil && parsed.Response.IncompleteDetails != nil && parsed.Response.IncompleteDetails.Reason != "" {
				message += ": " + parsed.Response.IncompleteDetails.Reason
			}
			verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Message: message, Type: "response_incomplete"}}
			return verdict
		case responses.StreamEventTypeResponseCancelled:
			verdict.terminal = true
			verdict.abnormalErr = fmt.Errorf("%w: responses event %s", errAbnormalFinish, parsed.Type)
			verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response cancelled", Type: "response_cancelled"}}
			return verdict
		case responses.StreamEventTypeError:
			verdict.terminal = true
			verdict.abnormalErr = fmt.Errorf("%w: responses event %s", errAbnormalFinish, parsed.Type)
			if parsed.Message == "" {
				parsed.Message = "responses stream error"
			}
			verdict.inspectErr = &llm.ResponseError{Detail: llm.ErrorDetail{Code: parsed.Code, Message: parsed.Message, Type: "stream_error"}}
			return verdict
		}
		// 增量类事件必然承载内容; item 级 added/done 也视为内容开始,
		// 覆盖 done-only 形态的上游(如 Codex 式一次性 function_call)。
		eventType := string(parsed.Type)
		if strings.HasSuffix(eventType, ".delta") {
			verdict.hasContent = true
		}
		verdict.hasContent = verdict.hasContent || eventType == "output_item.added" || eventType == "output_item.done"
		return verdict

	default:
		return streamEventVerdict{}
	}
}

// chatStreamEventProbe OpenAI Chat 流事件的轻量探针: 只抽取终止原因/内容信号/错误帧三类
// 判定所需字段, 内容类大字段以 json.RawMessage 引用原始字节避免拷贝。错误帧复用库的
// OpenAIError 结构, 与历史 inspectStreamEvent 的错误识别语义完全一致。
type chatStreamEventProbe struct {
	Error *openai.OpenAIError `json:"error"`
	// Choices 保持 RawMessage 引用原始字节: choices 整体非数组(数字/对象等)时不让
	// 顶层解码失败, 由 analyzeChatStreamEvent 折叠为空数组处理, 对齐 gjson Array() 语义。
	Choices json.RawMessage `json:"choices"`
}

// chatStreamChoiceProbe 单条 choice 的判定字段。
// tool_calls 用 RawMessage: 历史实现经 gjson Array() 折叠, 除缺失与显式 null 外
// 任何形态(空对象/数字/数组)都算"携带工具调用", 需要原样保留这一容忍度。
type chatStreamChoiceProbe struct {
	FinishReason json.RawMessage `json:"finish_reason"`
	Delta        struct {
		Content          json.RawMessage `json:"content"`
		ReasoningContent json.RawMessage `json:"reasoning_content"`
		ToolCalls        json.RawMessage `json:"tool_calls"`
	} `json:"delta"`
}

// rawToolCallsPresent 对齐 gjson Array() 的折叠语义: 缺失与显式 null 之外即为携带。
func rawToolCallsPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// rawStringBytes 判定 RawMessage 是否为非空 JSON 字符串(即至少携带一个字符的文本)。
func rawStringBytes(raw json.RawMessage) bool {
	return len(raw) > 2 && raw[0] == '"'
}

// rawFinishReason 提取 finish_reason 的字符串值: 缺失/null 返回 ok=false(跳过判定),
// JSON 字符串解码为 Go 字符串(含空串 `""`, 属白名单内的正常取值), 其余 JSON 值原样
// 字符串化交由白名单判定(与 gjson 时代的语义一致)。
func rawFinishReason(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	if len(raw) >= 2 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return string(raw), true
		}
		return value, true
	}
	return string(raw), true
}

// analyzeChatStreamEvent 单次解码 OpenAI Chat 流事件并派生全部判定。
func analyzeChatStreamEvent(event *httpclient.StreamEvent) streamEventVerdict {
	if bytes.Equal(event.Data, llm.DoneStreamEvent.Data) {
		return streamEventVerdict{terminal: true}
	}
	var probe chatStreamEventProbe
	if err := json.Unmarshal(event.Data, &probe); err != nil {
		return streamEventVerdict{terminal: true, inspectErr: fmt.Errorf("decode openai stream event: %w", err)}
	}
	verdict := streamEventVerdict{}
	// 流内错误帧: 与历史判定一致, 事件名为 error 或错误详情任一字段非空才算失败;
	// 仅 error 字段存在但详情全空且事件名非 error 的负载属于上游私有数据, 不判失败。
	if event.Type == "error" || (probe.Error != nil && (probe.Error.Detail.Message != "" || probe.Error.Detail.Type != "" || probe.Error.Detail.Code != "")) {
		detail := llm.ErrorDetail{}
		if probe.Error != nil {
			detail = probe.Error.Detail
		}
		if detail.Message == "" {
			detail.Message = "openai stream error"
		}
		verdict.terminal = true
		verdict.inspectErr = &llm.ResponseError{Detail: detail}
		return verdict
	}
	// choices 顶层非数组(数字/对象/字符串)时折叠为空数组: 与 gjson 时代 Array() 的折叠
	// 语义一致, 按"无终止原因/无内容"处理, 不让整个事件判失败。
	var choices []json.RawMessage
	if len(probe.Choices) > 0 {
		_ = json.Unmarshal(probe.Choices, &choices)
	}
	for _, choiceRaw := range choices {
		var choice chatStreamChoiceProbe
		if err := json.Unmarshal(choiceRaw, &choice); err != nil {
			// 单条 choice 畸形(非对象/delta 非对象等)只跳过本条, 对齐 gjson 时代不会因此判失败。
			continue
		}
		if reason, ok := rawFinishReason(choice.FinishReason); ok {
			if !validChatFinishReason(reason) {
				// 与历史一致保留首个异常终止原因(多 choice 场景下不互相覆盖)。
				if verdict.abnormalErr == nil {
					verdict.abnormalErr = fmt.Errorf("%w: finish_reason %q", errAbnormalFinish, reason)
				}
			}
			if reason == "stop" {
				verdict.stopFinish = true
			}
			// 非空且白名单内的终止原因 = 上游已声明生成完整结束。部分 OpenAI 兼容上游
			// (如 MiniMax)发完终止块直接关流、不发 [DONE], 该信号用于区分"缺哨兵的完整
			// 响应"与"真截断", 避免把完整响应误判为提前关闭。
			if reason != "" && validChatFinishReason(reason) {
				verdict.finishSeen = true
			}
		}
		answer := rawStringBytes(choice.Delta.Content) ||
			rawToolCallsPresent(choice.Delta.ToolCalls)
		reasoning := rawStringBytes(choice.Delta.ReasoningContent)
		if answer {
			verdict.hasAnswer = true
		}
		if reasoning {
			verdict.hasReasoning = true
		}
		if answer || reasoning {
			verdict.hasContent = true
		}
	}
	return verdict
}

// inspectStreamEvent 判断一个客户端协议流事件是否结束了整个响应流, 并识别以事件形式下发的上游错误。
// 返回 true 表示流已结束; 返回的 error 非空表示该事件本身即失败, 本轮不可提交。
// 转发热路径请使用 analyzeStreamEvent 一次解码取得全部判定; 本函数保留给测试与低频调用。
func inspectStreamEvent(format llm.APIFormat, event *httpclient.StreamEvent) (bool, error) {
	verdict := analyzeStreamEvent(format, event)
	return verdict.terminal, verdict.inspectErr
}

// errZeroOutput 表示上游明确上报了用量且输出 token 为 0, 本轮响应按无效处理并换目标重试。
// 上游未上报用量(统一层 usage 为 nil)不等于 0, 不触发该错误以免对不报用量的渠道无限重试。
var errZeroOutput = errors.New("upstream reported zero output tokens")

// errStreamEarlyClose 表示上游已产出内容并在协议终止事件之前干净关闭了流(EOF 无错误):
// 对下游与中途断连无异, 提交后的流必须按失败终态定稿。客户端侧以静默截断收尾
// (不补发终止帧, 见转发循环), 让支持「流缺终止事件即重连」的客户端感知失败并整体重试,
// 而不是把残缺内容当作完成。
// 豁免: 上游已发白名单内的非空终止原因(OpenAI Chat 的 finish_reason / Anthropic 的
// message_delta.stop_reason)时, 生成已完整结束, 只是缺终止哨兵([DONE]/message_stop),
// 按正常终态合成哨兵帧收尾而非判截断。
var errStreamEarlyClose = errors.New("upstream stream closed before the terminal event")

// errStreamEarlyEof 表示上游在产出任何内容信号与协议终止事件之前就关闭了流(提前 EOF):
// 200 已下发但字节流戛然而止, 属于上游侧异常中断。本轮尚未写给客户端, 可安全换目标重试;
// 首次发生时对同成员给予一次不计失败的立即重试, 第二次仍提前 EOF 才正常计入失败。
var errStreamEarlyEof = errors.New("upstream stream ended before any content or terminal event")

// errAbnormalFinish 表示上游在普通数据帧中下发了协议白名单之外的异常终止原因,
// 例如某上游渠道在 OpenAI 兼容流的普通 chunk 里携带 "finish_reason":"network_error"。
// 这类响应按无效处理: 提交前整轮失败换目标重试; 提交后抑制污染帧并以干净的协议终止帧收尾,
// 保证异常终止原因永远不出现在下游可见的字节流中。
var errAbnormalFinish = errors.New("upstream reported abnormal finish reason")

// errNoAnswerStop 表示上游以自然终止(finish_reason=stop)收尾, 但整轮只产出了推理内容,
// 没有任何最终回答(文本或工具调用)。思考型模型的推理与回答共享输出预算, 思考阶段耗尽
// 预算后部分上游把这类轮次按正常 stop 交付而非如实上报截断; 客户端收到"成功结束的空回答"
// 只能整轮判失败。该形态按无效轮次处理: 非流式在提交前整轮判失败换目标重试; 已提交的流
// 抑制终止帧并静默截断(不补发终止帧, 见转发循环), 客户端按流断开感知失败后自动重试整个请求。
// 仅识别 OpenAI Chat 客户端协议: Anthropic 截断如实上报 max_tokens, Responses 截断下发
// incomplete, 两者均已由既有异常判定覆盖, 无需重复处理。
var errNoAnswerStop = errors.New("upstream finished with reasoning only and no answer content")

// errStreamIdleTimeout 表示流式转发期相邻事件间隔超过配置的空闲超时:
// 首有效内容后逐事件转发期间, 上游长时间不产出任何事件(疑似卡死的心跳流或挂起的连接),
// 超过 MemberStreamIdleTimeoutSeconds 即以明确终态终止流并按失败定稿。0 表示不限时,
// 维持既有长流语义。该超时只作用于单次转发的逐事件读取阶段, 不影响全局 WriteTimeout,
// 不会破坏正常长 SSE(只要事件持续到达, 计时器不断重置)。
var errStreamIdleTimeout = errors.New("upstream stream idle timeout exceeded")

// errStreamWindowBudgetExceeded 表示有效性窗口预读阶段累计事件数或字节数超过安全预算:
// 首个内容信号出现前的结构帧通常只有 1-3 个, 超过固定预算说明上游在持续发送无内容事件
// (心跳/keepalive/空 ping), 本轮尚未写给客户端, 可安全换目标重试。
var errStreamWindowBudgetExceeded = errors.New("upstream stream window budget exceeded")

// errStreamBudgetExceeded 表示已提交的流式转发累计事件数或字节数超过配置的上限:
// 防止异常上游用无限事件流耗尽 relay 内存。已提交的流按失败定稿但不重发任何内容,
// 以静默截断收尾(与其它提交后流失败语义一致)。0 表示不限, 维持既有长流语义。
var errStreamBudgetExceeded = errors.New("upstream stream budget exceeded")

// validChatFinishReason 判定 OpenAI Chat 协议的 choices[].finish_reason 是否为正常终止原因。
// 白名单之外的取值(如 network_error/error/aborted)说明上游以非协议方式终止了生成,
// 一律视为异常。content_filter 等 refusal 类终态同样判异常: 本项目把"成员产出可用回复"
// 作为成功口径, 拒答式终态与其他白名单外取值一样换目标重试, 由其他成员给出有效响应。
// "other" 予以放行: 部分上游(如第三方 OpenAI 兼容服务)在模型因非标准原因正常停止时
// 使用该值, 它是合法的生成终止信号而非错误或拒答, 拦截只会对健康响应误判失败。
func validChatFinishReason(reason string) bool {
	switch reason {
	case "", "stop", "length", "tool_calls", "function_call", "other":
		return true
	default:
		return false
	}
}

// validAnthropicStopReason 判定 Anthropic 协议 message_delta 的 delta.stop_reason 是否为正常终止原因。
// pause_turn 与 refusal 虽不在核心五值内但予以放行: 两者都是 Anthropic 文档化的正常终态——
// pause_turn 表示长周期服务端工具轮次被暂停(客户端按协议发起新请求续跑), refusal 表示模型
// 已完成一次安全拒答; axonhub 自身也分别把它们映射为 "stop" 与 "content_filter" 正常交付。
// 拦截这两个文档化取值只会对健康长任务流误判失败并触发无意义的故障转移, 故不视作异常;
// 其余未知取值(如 network_error)仍一律判异常。
func validAnthropicStopReason(reason string) bool {
	switch reason {
	case "", "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal":
		return true
	default:
		return false
	}
}

// validUnifiedFinishReason 判定统一层(llm.Response)的 FinishReason 是否为正常终止原因。
// axonhub 会把各家协议的终止原因映射成 OpenAI 风格值(end_turn→stop, max_tokens→length,
// tool_use→tool_calls, refusal→content_filter 等), 因此三种客户端格式共用同一份白名单;
// 白名单与 OpenAI Chat 保持一致, 映射后落在白名单外的取值同样判异常并整轮重试。
func validUnifiedFinishReason(reason string) bool {
	return validChatFinishReason(reason)
}

// streamEventAbnormalFinish 检查一个客户端协议流事件是否携带白名单之外的异常终止原因或异常终态。
// 返回包装 errAbnormalFinish 的错误表示该事件不可交付; 返回 nil 包括三类情形:
// 事件不承载终止信息、终止原因在白名单内、事件本身无法解析(解码错误由 inspectStreamEvent 负责识别)。
// Responses 协议没有 finish_reason 字段, 以 response.completed 且 status=="completed" 为唯一正常终态,
// failed/incomplete/cancelled/error 及带异常 status 的 completed 均判异常, 与 inspectStreamEvent 的窗口期判定保持一致。
// 转发热路径请使用 analyzeStreamEvent 一次解码取得全部判定; 本函数保留给测试与低频调用。
func streamEventAbnormalFinish(format llm.APIFormat, event *httpclient.StreamEvent) error {
	return analyzeStreamEvent(format, event).abnormalErr
}

// validEmptyOutputTerminal 判定终止原因是否属于合法的空输出终态:
// 截断(length/max_tokens)与工具调用(tool_calls/function_call/tool_use)轮次可以没有文本产出,
// 即使上游明确上报 output==0 也放行交付, 防止脏数据误杀 max_tokens=1 的连通性 ping 类请求。
func validEmptyOutputTerminal(reason string) bool {
	switch reason {
	case "length", "tool_calls", "function_call", "max_tokens", "tool_use":
		return true
	default:
		return false
	}
}

// unifiedFinishReason 提取统一响应首条 choice 的终止原因, 不存在时返回空串。
func unifiedFinishReason(response *llm.Response) string {
	if response == nil || len(response.Choices) == 0 || response.Choices[0].FinishReason == nil {
		return ""
	}
	return *response.Choices[0].FinishReason
}

// validateRoundUsage 校验一次尝试的最终用量: 明确上报的输出 token 为 0 时判定本轮无效;
// terminal 为该轮聚合出的最终终止原因, 落在合法空输出终态白名单内时放行交付,
// 拿不到终态(空串)维持原判定口径。
func validateRoundUsage(usage *llm.Usage, terminal string) error {
	if usage == nil || usage.CompletionTokens != 0 {
		return nil
	}
	if validEmptyOutputTerminal(terminal) {
		return nil
	}
	return fmt.Errorf("%w: output tokens reported as 0", errZeroOutput)
}

// validateRoundAnswer 校验非流式响应是否为"思考-only 空回答"轮次(errNoAnswerStop):
// finish_reason=stop 且首条消息只有 reasoning_content、没有任何最终回答时判整轮无效。
// 与 validateRoundUsage 的零输出保险丝互补: 思考-only 轮次的输出 token 非零, 不会被
// 零输出判定拦截。仅校验 OpenAI Chat 客户端协议, 其余协议由既有异常判定覆盖;
// 流式路径的同一形态由转发循环按事件流判定, 不经过本函数。
func validateRoundAnswer(format llm.APIFormat, body []byte) error {
	if format != llm.APIFormatOpenAIChatCompletion {
		return nil
	}
	if gjson.GetBytes(body, "choices.0.finish_reason").String() != "stop" {
		return nil
	}
	message := gjson.GetBytes(body, "choices.0.message")
	if !message.Exists() || chatMessageHasAnswer(message) {
		return nil
	}
	reasoning := message.Get("reasoning_content")
	if reasoning.Type != gjson.String || reasoning.Str == "" {
		return nil
	}
	return fmt.Errorf("%w: finish_reason stop with reasoning_content only", errNoAnswerStop)
}

// chatMessageHasAnswer 判定一条 OpenAI Chat 消息是否携带最终回答信号:
// 非空文本内容(字符串或分片数组)或工具调用; reasoning_content 不算回答。
func chatMessageHasAnswer(message gjson.Result) bool {
	content := message.Get("content")
	switch {
	case content.Type == gjson.String:
		if content.Str != "" {
			return true
		}
	case content.IsArray():
		for _, part := range content.Array() {
			if text := part.Get("text"); text.Type == gjson.String && text.Str != "" {
				return true
			}
		}
	}
	toolCalls := message.Get("tool_calls")
	return toolCalls.IsArray() && len(toolCalls.Array()) > 0
}

// streamEventHasContent 判断一个客户端协议流事件是否承载了实际输出内容(文本/推理/工具调用)。
// 用于流式有效性窗口: 只有出现内容信号才提交响应, 纯结构事件(role 帧, message_start, ping,
// usage 尾帧等)不算内容。事件格式为客户端协议。
// 转发热路径请使用 analyzeStreamEvent 一次解码取得全部判定; 本函数保留给测试与低频调用。
func streamEventHasContent(format llm.APIFormat, event *httpclient.StreamEvent) bool {
	return analyzeStreamEvent(format, event).hasContent
}

// terminalStreamFrames 在客户端协议缺终止事件时合成正常终止事件序列, 让客户端 SDK 收到
// 规范的收尾而不是悬挂到超时。仅用于正常收尾路径: 污染流的强制兜底(异常终止原因不出现在
// 下游可见字节流中)与缺 [DONE] 哨兵的完整流(如 MiniMax)。
// 提交后的流失败不走本函数, 以静默截断收尾(见转发循环): 流内错误帧无法触发客户端自动重试,
// 缺失终止事件反而可以(Codex/opencode 等按 stream disconnected 自动重连重试)。
func terminalStreamFrames(format llm.APIFormat) []*httpclient.StreamEvent {
	now := time.Now().Unix()
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		chunk := map[string]any{
			"id":      "chatcmpl-novaveil-recovery",
			"object":  "chat.completion.chunk",
			"created": now,
			"model":   "",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}
		done := &httpclient.StreamEvent{Data: []byte("[DONE]")}
		if data, err := json.Marshal(chunk); err == nil {
			return []*httpclient.StreamEvent{{Data: data}, done}
		}
		return []*httpclient.StreamEvent{done}

	case llm.APIFormatAnthropicMessage:
		delta := map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		}
		stop := map[string]any{"type": "message_stop"}
		deltaData, err1 := json.Marshal(delta)
		stopData, err2 := json.Marshal(stop)
		if err1 != nil || err2 != nil {
			return nil
		}
		return []*httpclient.StreamEvent{
			{Type: "message_delta", Data: deltaData},
			{Type: "message_stop", Data: stopData},
		}

	case llm.APIFormatOpenAIResponse:
		response := map[string]any{
			"id":         "resp_novaveil_recovery",
			"object":     "response",
			"created_at": now,
			"status":     "completed",
			"output":     []any{},
		}
		payload := map[string]any{"type": "response.completed", "response": response}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil
		}
		return []*httpclient.StreamEvent{{Type: "response.completed", Data: data}}

	default:
		return nil
	}
}

// isTerminalStreamEvent 以低成本方式判断一个客户端协议事件是否为协议终止事件。
// 用于转发阶段追踪流是否已正常收尾; 错误识别仍由 inspectStreamEvent 的完整解析负责。
func isTerminalStreamEvent(format llm.APIFormat, event *httpclient.StreamEvent) bool {
	if event == nil || len(event.Data) == 0 {
		return false
	}
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return bytes.Equal(event.Data, llm.DoneStreamEvent.Data)
	case llm.APIFormatAnthropicMessage:
		return bytes.Contains(event.Data, []byte(`"type":"message_stop"`))
	case llm.APIFormatOpenAIResponse:
		data := strings.TrimSpace(string(event.Data))
		const prefix = `{"type":"response.`
		if !strings.HasPrefix(data, prefix) {
			return false
		}
		rest := data[len(prefix):]
		for _, kind := range []string{"completed", "failed", "incomplete", "cancelled"} {
			if strings.HasPrefix(rest, kind+`"`) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// validateResponse 检查统一响应中需要在提交前判定为失败的终止原因:
// Responses 协议会以正常 200 响应下发失败终态, axonhub 把 response.status 编码在统一 FinishReason 中
// (completed→stop, failed→error, incomplete→length, cancelled→cancelled), 先按此还原真实终态;
// 随后三种格式统一应用终止原因白名单, 白名单之外的取值(如上游私加的 network_error)整轮判无效并换目标重试。
// choices 为空且未携带任何用量的 200 响应属于上游脏数据(既无产出也无计量), 同样整轮判失败;
// 携带用量的空 choices 响应维持放行, 交由后续用量校验按明确上报的 0 输出口径判定。
func validateResponse(format llm.APIFormat, response *llm.Response) error {
	if response == nil {
		return errors.New("upstream response is empty")
	}
	if len(response.Choices) == 0 {
		if response.Usage == nil {
			return errors.New("upstream 200 response has neither choices nor usage")
		}
		return nil
	}
	if response.Choices[0].FinishReason == nil {
		return nil
	}
	reason := *response.Choices[0].FinishReason
	if format == llm.APIFormatOpenAIResponse {
		switch reason {
		case "error":
			return &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response failed", Type: "response_failed"}}
		case "length":
			return &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response incomplete", Type: "response_incomplete"}}
		case "cancelled":
			return &llm.ResponseError{Detail: llm.ErrorDetail{Message: "response cancelled", Type: "response_cancelled"}}
		}
	}
	if !validUnifiedFinishReason(reason) {
		return fmt.Errorf("%w: %q", errAbnormalFinish, reason)
	}
	return nil
}
