package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// upstreamResponse 是已验证但尚未写给客户端的上游成功响应; events 为 nil 表示非流式响应。
// 透传响应保留上游响应头; 跨协议响应由客户端协议决定响应头。失败一律以 error 返回。
//
// release 是渠道级并发槽位的释放函数: 对流式响应而言, 槽位必须覆盖"上游事件流仍在产出
// 剩余事件"的整个生命周期, 仅在 events 流被客户端消费完毕或转发终止时显式调用一次;
// 对非流式响应也作为可幂等调用的 Close 句柄, 由调用方统一在转发结束点释放以避免遗漏。
// 重复调用 release 是安全的(no-op)。
type upstreamResponse struct {
	body       []byte                                  // 非流式响应的完整正文。
	header     http.Header                             // 同协议透传时需要原样返回的上游响应头。
	events     streams.Stream[*httpclient.StreamEvent] // 流式响应中有效性窗口之后的剩余事件。
	window     []*httpclient.StreamEvent               // 流式有效性窗口内预读并缓冲的事件, 提交时按序写给客户端。
	last       bool                                    // 事件流已在窗口内耗尽, events 中不再有剩余事件。
	terminated bool                                    // 窗口内出现了协议终止事件; 与 last 组合可区分正常终止与提前 EOF。
	usage      *llm.Usage                              // 上游本次可确认的用量。
	release    func()                                  // 归还渠道级并发槽位; 幂等且只释放一次。
	closeOnce  sync.Once                               // Close 可与管理端中止及 handler 收尾并发调用。
}

// Close 释放上游响应持有的渠道并发槽位, 同时关闭事件流(若有)。流式转发期必须在事件流耗尽、
// 客户端断开或终止判定后调用一次; 非流式路径上为幂等的 no-op(已通过 defer 兜底释放)。
// 重复调用安全: release 由 acquire 端保证幂等, events.Close 同样只关闭底层 io。
func (r *upstreamResponse) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		// 先关事件流使阻塞 Next 及时返回, 再归还并发槽位; 这保证槽位不会在上游连接仍
		// 读取期间被下一请求拿走。两者都只读 immutable 字段, 不与 handler 的消费构成数据竞争。
		if r.events != nil {
			_ = r.events.Close()
		}
		if r.release != nil {
			r.release()
		}
	})
}

// sendPassthrough 以同协议透传方式请求上游, 取得的响应无需转换即可回给客户端。
// randomValue 为请求级一次性解析的随机头值, 同一请求的所有头与所有重试复用此值。
func sendPassthrough(ctx context.Context, format llm.APIFormat, raw *httpclient.Request, channel model.Channel, outbound transformer.Outbound, streaming bool, randomValue string) (*upstreamResponse, error) {
	// 渠道整体并发上限: 发起上游前领取槽位并把释放函数交给调用方持有整个生命周期
	// (含流式窗口预读与剩余事件流的逐帧消费); 满载等待期间上下文结束立即以 ctx 错误返回,
	// 不发起任何上游请求。释放语义由 Close 统一收口, 不再依赖 defer, 避免流式分支在
	// 函数返回前过早归还导致后续长流期间槽位丢失。
	releaseConcurrency, err := acquireChannelConcurrency(ctx, channel.ID, channel.MaxConcurrent)
	if err != nil {
		return nil, err
	}

	request, err := buildPassthroughRequest(format, raw, channel, randomValue)
	if err != nil {
		releaseConcurrency()
		return nil, err
	}
	client, err := helper.ChannelHttpClient(&channel)
	if err != nil {
		releaseConcurrency()
		return nil, err
	}
	if streaming {
		resp, streamErr := sendPassthroughStream(ctx, format, request, client)
		if streamErr != nil {
			releaseConcurrency()
			return nil, streamErr
		}
		// 槽位由返回值持有, 调用方在事件流耗尽或转发终止时调用 Close 释放;
		// 失败路径已在 sendPassthroughStream 内部 close events 后返回, 此处无需再释放。
		resp.release = releaseConcurrency
		return resp, nil
	}

	response, err := httpclient.NewHttpClientWithClient(client).Do(ctx, request)
	if err != nil {
		releaseConcurrency()
		var failure *httpclient.Error
		if errors.As(err, &failure) && len(failure.Body) > 0 {
			return nil, fmt.Errorf("%w: %s", err, errBodySnippet(failure.Body))
		}
		return nil, err
	}
	// 非对话类接口(图片/视频/语音/嵌入等)或完全渠道透传: 不解析响应、不校验用量与终止原因,
	// 原样返回上游响应体与头, 由客户端自行处理。响应可能是二进制(如 audio/speech 返回音频流)。
	// 完全渠道透传下, outbound transformer 按渠道类型创建(如 OpenAI), 但响应格式由客户端
	// 协议决定(可能是 Anthropic), 用 OpenAI outbound 解析会失败, 因此也跳过。
	if !isChatFormat(format) || channel.PassThroughBodyEnabled {
		return &upstreamResponse{body: response.Body, header: response.Headers.Clone(), release: releaseConcurrency}, nil
	}
	// 同协议下响应可原样回给客户端, 仍需解析一次以取得用量并识别以 200 下发的失败终态;
	// validateResponse 同时应用终止原因白名单, 异常 finish_reason 的响应在此整轮判无效。
	parsed, err := outbound.TransformResponse(ctx, response)
	if err != nil {
		releaseConcurrency()
		return nil, fmt.Errorf("%w: %s", err, errBodySnippet(response.Body))
	}
	if err := validateResponse(format, parsed); err != nil {
		releaseConcurrency()
		return nil, fmt.Errorf("%w: %s", err, errBodySnippet(response.Body))
	}
	// 输出 token 明确为 0 的响应按无效处理, 在提交前换目标重试;
	// 截断/工具调用等合法空输出终态放行交付。
	if err := validateRoundUsage(parsed.Usage, unifiedFinishReason(parsed)); err != nil {
		releaseConcurrency()
		return nil, fmt.Errorf("%w: %s", err, errBodySnippet(response.Body))
	}
	// 思考-only 空回答轮次(stop 终态但只有推理内容)同样在提交前整轮判无效换目标重试。
	if err := validateRoundAnswer(format, response.Body); err != nil {
		releaseConcurrency()
		return nil, fmt.Errorf("%w: %s", err, errBodySnippet(response.Body))
	}
	// response.Body 是本请求的独立读取缓冲, 直接移交所有权, 不再整包克隆一份。
	return &upstreamResponse{body: response.Body, header: response.Headers.Clone(), usage: parsed.Usage, release: releaseConcurrency}, nil
}

// sendPassthroughStream 发起同协议流式请求并预读有效性窗口, 窗口通过验证才算本轮取得可提交响应。
func sendPassthroughStream(ctx context.Context, format llm.APIFormat, request *httpclient.Request, client *http.Client) (*upstreamResponse, error) {
	rawRequest, err := httpclient.BuildHttpRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	// 客户端的 Accept 属于库自管头不会透传, 需显式声明才能让上游按 SSE 返回。
	rawRequest.Header.Set("Accept", "text/event-stream")

	// client 由 ChannelHttpClient 取得, 内置 upstreamCheckRedirect: 跨源重定向与 HTTPS→HTTP
	// 降级被拒绝、跳转次数受限。被拒绝的 3xx 在此以 err 返回(Body 已由 net/http 关闭),
	// 不会进入下方 SSE 解码, 也不会被当作成功流式响应透传给客户端。
	response, err := client.Do(rawRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		failure, readErr := readUpstreamErrorBody(response.Body)
		if closeErr := response.Body.Close(); readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("upstream responded %s: %s", response.Status, errBodySnippet(failure))
	}

	events := httpclient.NewDefaultSSEDecoder(ctx, response.Body)
	window, ended, terminated, err := readStreamWindow(ctx, format, inboundForFormat(format), events)
	if err != nil {
		events.Close()
		return nil, err
	}
	return &upstreamResponse{header: response.Header.Clone(), events: events, window: window, last: ended, terminated: terminated}, nil
}

// inboundForFormat 按客户端协议构造入站转换器。
func inboundForFormat(format llm.APIFormat) transformer.Inbound {
	switch format {
	case llm.APIFormatOpenAIResponse:
		return responses.NewInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		return anthropic.NewInboundTransformer()
	default:
		return openai.NewInboundTransformer()
	}
}

// conversionMiddleware 保存跨协议 pipeline 单次调用需要应用和取得的状态。
type conversionMiddleware struct {
	pipeline.DummyMiddleware               // 提供本次无需处理的其余 pipeline 中间件方法。
	channel                  model.Channel // 本轮上游请求使用的渠道配置。
	format                   llm.APIFormat // 上游渠道协议, 用于校验统一响应终态。
	randomValue              string        // 请求级一次性解析的随机头值, 同一请求的所有头与所有重试复用此值。
	rawBody                  string        // 上游非流式响应或错误的诊断片段(已截断), 转换/校验失败时嵌入错误。
	usage                    *llm.Usage    // 非流式统一响应中确认的用量。
	terminal                 string        // 非流式统一响应的终止原因, 供空输出保险丝区分合法空终态。
	traceEnabled             bool          // 是否捕获转换后请求体(供协议转换追踪); 关闭时不捕获, 零开销。
	convertedRequestBody     []byte        // 转换后的上游请求体快照; 仅 traceEnabled=true 时填充, 供追踪诊断对比。
}

// OnOutboundRawRequest 在转换后的上游请求上应用渠道参数和自定义 Header。
// 目标为 OpenAI Chat 协议时顺带做角色归一化: Responses 协议特有的 developer 角色
// 与会话中途的 system 消息会被大量兼容代理以 Incorrect role / Invalid parameter 拒绝。
// 无密钥渠道在此删除认证头: pipeline 中 FinalizeAuthHeaders 先于本钩子执行, 占位凭据
// 此时已写成 Authorization/X-API-Key 等请求头, 只能在钩子里删除; Auth 一并置 nil,
// 防止构建 HTTP 请求时被二次写入。之后才应用渠道配置, 显式自定义的认证头不受影响。
func (m *conversionMiddleware) OnOutboundRawRequest(_ context.Context, request *httpclient.Request) (*httpclient.Request, error) {
	if m.traceEnabled {
		m.convertedRequestBody = request.Body
	}
	if m.channel.PrimaryKey() == "" {
		request.Auth = nil
		request.Headers.Del("Authorization")
		request.Headers.Del("X-Api-Key")
		request.Headers.Del("X-Goog-Api-Key")
	}
	if m.format == llm.APIFormatOpenAIChatCompletion {
		request.Body = normalizeChatRoles(request.Body)
	}
	return request, applyChannelConfig(m.channel, request, m.randomValue)
}

// StripNonFunctionTools 移除 tools 中类型不是 "function" 的条目(server 内建工具如
// web_search 无法映射到 OpenAI Chat 协议, 严格代理会以 Invalid API parameter 拒绝)。
// 仅在跨协议转换场景调用; 同协议透传保持原样以免破坏原生能力。
func StripNonFunctionTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	kept := make([]string, 0, len(tools.Array()))
	removed := 0
	for _, t := range tools.Array() {
		if t.Get("type").String() == "function" {
			kept = append(kept, t.Raw)
		} else {
			removed++
		}
	}
	if removed == 0 {
		return body
	}
	builder := "[" + strings.Join(kept, ",") + "]"
	out, err := sjson.SetRawBytes(body, "tools", []byte(builder))
	if err != nil {
		return body
	}
	return out
}

// convertedFieldWhitelist 跨协议转换请求允许保留的顶层字段(对齐 new-api 最小输出):
// thinking/context_management/output_config/metadata/tool_choice/stream_options/
// reasoning_effort 等扩展参数在多数兼容代理上会触发 Invalid API parameter 类拒绝。
var convertedFieldWhitelist = map[string]struct{}{
	"model": {}, "messages": {}, "max_tokens": {}, "temperature": {},
	"top_p": {}, "top_k": {}, "stop": {}, "stream": {}, "tools": {},
}

// normalizeChatRoles 对 chat 消息做两类归一化(返回新切片; 无需调整时原样返回):
//  1. developer 角色: 首条消息映射为 system, 会话中途映射为 user;
//  2. 会话中途的 system 消息折叠进首条 system(无首条则将其移至最前),
//     保证 system 只出现在消息列表开头——多数兼容代理仅接受前导 system。
//
// 实现基于 gjson 扫描与 sjson 精准补丁: 历史实现把全部消息逐条 Unmarshal 成 map
// 再整体重新 Marshal, 长上下文下产生数倍于请求体的瞬时分配; 现在角色修正为单点
// sjson 改写, 折叠/重排以原始 JSON 片段重建数组, 消息内容不重新编码。
func normalizeChatRoles(body []byte) []byte {
	rows := gjson.GetBytes(body, "messages").Array()
	if len(rows) == 0 {
		return body
	}

	developerIdx := make([]int, 0, 2)
	systemIdx := make([]int, 0, 2)
	for i, row := range rows {
		switch row.Get("role").String() {
		case "developer":
			developerIdx = append(developerIdx, i)
		case "system":
			systemIdx = append(systemIdx, i)
		}
	}
	// 快速路径: 无任何 system 角色时不做折叠/重排, 也不应用工具与参数剥离。
	if len(systemIdx) == 0 {
		// developer 角色补丁仍然生效: 首条→system, 会话中途→user。
		// 历史实现在该分支直接返回原始 body, 中途 developer 角色被遗留并触发上游 400,
		// 依赖 400 清洗重试兜底; 这里直接修正, 省去一轮注定失败的上游往返。
		if len(developerIdx) == 0 {
			return body
		}
		out := body
		for _, i := range developerIdx {
			role := "user"
			if i == 0 {
				role = "system"
			}
			next, err := sjson.SetBytes(out, fmt.Sprintf("messages.%d.role", i), role)
			if err != nil {
				return body
			}
			out = next
		}
		return out
	}

	out := body
	// 1) developer 角色精准补丁: 首条→system, 会话中途→user。
	for _, i := range developerIdx {
		role := "user"
		if i == 0 {
			role = "system"
		}
		next, err := sjson.SetBytes(out, fmt.Sprintf("messages.%d.role", i), role)
		if err != nil {
			return body
		}
		out = next
	}

	// 2) system 折叠/前移: 重扫补丁后的消息列表, 以原始片段重建 messages 数组。
	rows = gjson.GetBytes(out, "messages").Array()
	systemIdx = systemIdx[:0]
	for i, row := range rows {
		if row.Get("role").String() == "system" {
			systemIdx = append(systemIdx, i)
		}
	}
	if len(systemIdx) == 0 {
		// developer 补丁不会删除 system, 理论不可达; 保守返回补丁后的 body。
		return out
	}
	first := systemIdx[0]
	canonicalText := messageRawText(rows[first])
	for _, idx := range systemIdx[1:] {
		canonicalText = joinBlocks(canonicalText, messageRawText(rows[idx]))
	}
	firstRaw, err := sjson.Set(rows[first].Raw, "content", canonicalText)
	if err != nil {
		return body
	}
	parts := make([]string, 0, len(rows))
	parts = append(parts, firstRaw)
	for i, row := range rows {
		if i == first {
			continue
		}
		folded := false
		for _, idx := range systemIdx[1:] {
			if i == idx {
				folded = true
				break
			}
		}
		if folded {
			continue
		}
		parts = append(parts, row.Raw)
	}
	next, err := sjson.SetRawBytes(out, "messages", []byte("["+strings.Join(parts, ",")+"]"))
	if err != nil {
		return body
	}
	out = next
	// 转换目标为 OpenAI Chat: 剔除无法映射的内建工具(server 工具), 严格代理会以
	// Invalid API parameter 拒绝; 同协议透传不经过本函数不受影响。
	out = StripNonFunctionTools(out)
	// 剥离白名单之外的顶层扩展参数, 防止严格代理报 Invalid parameter。
	for _, k := range gjson.GetBytes(out, "@keys").Array() {
		key := k.String()
		if _, ok := convertedFieldWhitelist[key]; !ok {
			if o, derr := sjson.DeleteBytes(out, key); derr == nil {
				out = o
			}
		}
	}
	return out
}

// messageRawText 提取消息 content 的纯文本(基于原始 JSON 片段, 不重新编码消息):
// 字符串原样; 块数组拼接各 text 字段。与历史 map 版 messageText 语义一致。
func messageRawText(row gjson.Result) string {
	content := row.Get("content")
	switch {
	case content.Type == gjson.String:
		return content.Str
	case content.IsArray():
		parts := make([]string, 0, 2)
		for _, block := range content.Array() {
			if t := block.Get("text"); t.Type == gjson.String && t.Str != "" {
				parts = append(parts, t.Str)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

// joinBlocks 合并两段文本, 空段直接取另一段。
func joinBlocks(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// OnOutboundRawError 保留上游错误状态码携带的原始正文。
func (m *conversionMiddleware) OnOutboundRawError(_ context.Context, err error) {
	var failure *httpclient.Error
	if errors.As(err, &failure) {
		m.rawBody = errBodySnippet(failure.Body)
	}
}

// OnOutboundRawResponse 保留上游成功响应的诊断片段, 供后续转换或终态校验失败时嵌入错误;
// 只截断保存片段而非整包克隆, 避免成功路径为诊断常驻一份完整响应副本。
func (m *conversionMiddleware) OnOutboundRawResponse(_ context.Context, response *httpclient.Response) (*httpclient.Response, error) {
	m.rawBody = errBodySnippet(response.Body)
	return response, nil
}

// OnOutboundLlmResponse 取得非流式用量与终止原因, 并在回转客户端协议前校验上游终态与终止原因白名单。
func (m *conversionMiddleware) OnOutboundLlmResponse(_ context.Context, response *llm.Response) (*llm.Response, error) {
	if err := validateResponse(m.format, response); err != nil {
		return nil, err
	}
	m.usage = response.Usage
	m.terminal = unifiedFinishReason(response)
	return response, nil
}

// sendConverted 经 axonhub pipeline 把客户端请求转换成渠道协议后请求上游, 响应再转换回客户端协议。
// randomValue 为请求级一次性解析的随机头值, 同一请求的所有头与所有重试复用此值。
func sendConverted(ctx context.Context, format llm.APIFormat, raw *httpclient.Request, channel model.Channel, outbound transformer.Outbound, streaming bool, randomValue string) (*upstreamResponse, error) {
	// 协议转换追踪: 关闭时仅一次缓存查询即短路, 不创建 ConvTrace, 不捕获转换体, 零开销。
	traceOn := convTraceOn()
	var ct *ConvTrace
	if traceOn {
		ct = beginConvTrace(format, channel, raw.Body)
	}
	convStart := time.Now()

	// 渠道整体并发上限: 发起上游前领取槽位并把释放函数交给 upstreamResponse 持有。
	// 流式响应必须覆盖窗口预读和剩余事件流的完整生命周期; 非流式响应在完整解析后立即释放。
	releaseConcurrency, err := acquireChannelConcurrency(ctx, channel.ID, channel.MaxConcurrent)
	if err != nil {
		return nil, err
	}

	var inbound transformer.Inbound
	switch format {
	case llm.APIFormatOpenAIResponse:
		inbound = responses.NewInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		inbound = anthropic.NewInboundTransformer()
	default:
		inbound = openai.NewInboundTransformer()
	}

	client, err := channelHTTPClient(&channel)
	if err != nil {
		releaseConcurrency()
		return nil, err
	}
	middleware := &conversionMiddleware{channel: channel, format: outbound.APIFormat(), randomValue: randomValue, traceEnabled: traceOn}
	processor := pipeline.NewFactory(httpclient.NewHttpClientWithClient(client)).Pipeline(
		inbound,
		outbound,
		pipeline.WithMiddlewares(middleware),
	)
	result, err := processor.Process(ctx, raw)
	if traceOn && ct != nil {
		ct.Elapsed = time.Since(convStart)
		finishConvTrace(ct, middleware.convertedRequestBody, err)
	}
	if err != nil {
		releaseConcurrency()
		if len(middleware.rawBody) > 0 {
			return nil, fmt.Errorf("%w: %s", err, middleware.rawBody)
		}
		return nil, err
	}
	if !streaming {
		// 输出 token 明确为 0 的响应按无效处理, 在提交前换目标重试;
		// 截断/工具调用等合法空输出终态放行交付。
		if err := validateRoundUsage(middleware.usage, middleware.terminal); err != nil {
			releaseConcurrency()
			return nil, fmt.Errorf("%w: %s", err, middleware.rawBody)
		}
		// 思考-only 空回答轮次(stop 终态但只有推理内容)同样在提交前整轮判无效换目标重试。
		if err := validateRoundAnswer(format, result.Response.Body); err != nil {
			releaseConcurrency()
			return nil, fmt.Errorf("%w: %s", err, middleware.rawBody)
		}
		// pipeline 输出的响应体是本请求的独立缓冲, 直接移交所有权, 不再整包克隆。
		return &upstreamResponse{body: result.Response.Body, usage: middleware.usage, release: releaseConcurrency}, nil
	}

	events := result.EventStream
	window, ended, terminated, err := readStreamWindow(ctx, format, inbound, events)
	if err != nil {
		_ = events.Close()
		releaseConcurrency()
		return nil, err
	}
	return &upstreamResponse{events: events, window: window, last: ended, terminated: terminated, release: releaseConcurrency}, nil
}

// readStreamWindow 预读流式事件直到出现首个内容承载信号(提交点)或流结束(有效性窗口关闭)。
// 返回的窗口内事件按序写给客户端; ended 为 true 表示事件流已在窗口内耗尽;
// terminated 为 true 表示窗口内出现了协议终止事件。
// 窗口内任一事件携带白名单之外的异常终止原因时返回包装 errAbnormalFinish 的错误:
// 本轮尚未写给客户端, 可安全换目标重试, 异常原因不会到达下游。
// 流耗尽且聚合用量明确为 0 时返回 errZeroOutput, 本轮尚未写给客户端, 可安全换目标重试。
// 流在任何内容信号与协议终止事件之前耗尽(ended 且未 terminated)时返回包装 errStreamEarlyEof 的错误:
// 上游提前 EOF 属于异常中断, 本轮尚未写给客户端, 按可重试失败处理而不是把空流交付给客户端。
// 窗口内出现终止事件但全程无内容信号且无白名单内非空终止原因时同样返回 errStreamEarlyEof:
// 转换 pipeline 的 outbound transformer(如 Anthropic)会向流追加合成终止事件( DoneResponse / [DONE] ),
// 当上游返回 0 字节 SSE 时该合成事件成为流中唯一事件, 形态上看似正常结束实则空响应。此处拦截以触发重试而非交付空成功。
// 豁免: 上游已发白名单内的非空终止原因(OpenAI Chat 的 finish_reason / Anthropic 的 stop_reason)时,
// 即使无内容也视为合法的空完成(模型主动停止), 不拦截以免对合法空响应误判失败。
// 用量缺失或聚合失败时不做判定, 与"只认明确上报的 0"的口径一致。
// 窗口内累计事件数/字节数超过 streamWindowMaxEvents/streamWindowMaxBytes 时返回
// errStreamWindowBudgetExceeded: 首个内容信号前的结构帧通常只有 1-3 个, 超过固定预算
// 说明上游在持续发送无内容事件(心跳/keepalive/空 ping), 本轮尚未写给客户端, 可安全换目标重试。
func readStreamWindow(ctx context.Context, format llm.APIFormat, inbound transformer.Inbound, events streams.Stream[*httpclient.StreamEvent]) ([]*httpclient.StreamEvent, bool, bool, error) {
	var window []*httpclient.StreamEvent
	var windowedBytes int
	sawContent := false    // 窗口内是否出现过任何内容承载信号(文本增量/工具调用/推理内容)
	sawFinishSeen := false // 窗口内是否出现过白名单内的非空终止原因(finish_reason/stop_reason)
	for events.Next() {
		event := events.Current()
		if event == nil || len(event.Data) == 0 {
			continue
		}
		windowedBytes += len(event.Data)
		if len(window)+1 > streamWindowMaxEvents || windowedBytes > streamWindowMaxBytes {
			return nil, false, false, fmt.Errorf("%w: windowed %d events / %d bytes", errStreamWindowBudgetExceeded, len(window)+1, windowedBytes)
		}
		// 单次解码取得全部判定(终止/错误/异常终止原因/内容信号), 窗口期避免同一事件反复解析。
		verdict := analyzeStreamEvent(format, event)
		if verdict.inspectErr != nil {
			return nil, false, false, fmt.Errorf("%w: %s", verdict.inspectErr, errBodySnippet(event.Data))
		}
		if verdict.abnormalErr != nil {
			return nil, false, false, fmt.Errorf("%w: %s", verdict.abnormalErr, errBodySnippet(event.Data))
		}
		window = append(window, event)
		if verdict.hasContent {
			sawContent = true
		}
		if verdict.finishSeen {
			sawFinishSeen = true
		}
		if verdict.terminal {
			// 先检查上游是否明确上报了 0 输出用量(errZeroOutput), 该错误比 early_eof 更具体,
			// 需在合成终止事件拦截之前返回以保留正确的错误分类。
			if uerr := rejectZeroOutput(ctx, format, inbound, window); uerr != nil {
				return nil, false, false, uerr
			}
			// 终止事件前未出现任何内容信号且未出现白名单内的非空终止原因:
			// 上游返回了空流(0 字节 SSE), 转换 pipeline 的 outbound transformer 会
			// 追加合成终止事件(如 [DONE]/DoneResponse), 使空流看起来像正常结束。
			// rejectZeroOutput 此时因 usage 为 nil 已放行, 需在此拦截按 early_eof 处理
			// 以触发重试或失败, 而不是把空响应当成功交付给客户端。
			// 豁免: 上游已发白名单内的非空终止原因(finish_reason/stop_reason)时, 即使无内容
			// 也视为合法的空完成(模型主动停止), 不拦截以免对合法空响应误判失败。
			if !sawContent && !sawFinishSeen {
				tail := errBodySnippet(event.Data)
				return nil, false, false, fmt.Errorf("%w: terminal event without preceding content or finish signal, %d windowed event(s), tail: %s", errStreamEarlyEof, len(window), tail)
			}
			return window, true, true, nil
		}
		if verdict.hasContent {
			return window, false, false, nil
		}
	}
	if err := events.Err(); err != nil {
		return nil, false, false, err
	}
	// 走到这里说明流已耗尽且从未出现内容信号与协议终止事件: 提前 EOF, 整轮判失败。
	// 该形态下不存在"聚合出合法空终止终态"的交付口径, 直接以哨兵错误换目标或免费重试。
	tail := ""
	if len(window) > 0 {
		tail = errBodySnippet(window[len(window)-1].Data)
	}
	return nil, false, false, fmt.Errorf("%w: upstream closed the stream after %d windowed event(s), tail: %s", errStreamEarlyEof, len(window), tail)
}

// errBodySnippetLimit 错误文本中允许嵌入的上游报文片段上限(字节):
// 错误会进入请求状态 r.Error 并随状态流广播给全部观察者, 完整响应体(可达数 MB)
// 一旦嵌入会以 MB 级字符串在内存与 SSE 扇出中反复复制, 截断到诊断够用的片段即可。
const errBodySnippetLimit = 8 * 1024

// streamWindowMaxEvents / streamWindowMaxBytes 是有效性窗口预读阶段的安全预算:
// 首个内容信号出现前的结构帧通常只有 1-3 个(message_start/content_block_start 等),
// 超过该预算说明上游在持续发送无内容事件(心跳/keepalive/空 ping), 本轮尚未写给客户端,
// 可安全换目标重试。预算足够宽松以容纳任意正常协议的前导帧序列, 只拦截异常/恶意上游。
const (
	streamWindowMaxEvents = 4096
	streamWindowMaxBytes  = 8 * 1024 * 1024 // 8 MiB
)

// readUpstreamErrorBody 只保留有限诊断内容，避免异常上游用超大错误体占满 relay 内存。
// 多读一个字节用于判断是否发生截断；错误文本最终仍由 errBodySnippet 做 UTF-8 截断。
func readUpstreamErrorBody(body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, errBodySnippetLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > errBodySnippetLimit {
		data = append(data[:errBodySnippetLimit], []byte("...[truncated]")...)
	}
	return data, nil
}

// errBodySnippet 把上游报文截断为诊断片段(UTF-8 边界安全), 供错误文本嵌入。
func errBodySnippet(data []byte) string {
	return truncateUTF8Bytes(string(data), errBodySnippetLimit)
}

// rejectZeroOutput 在有效性窗口内出现协议终止事件后判定是否为零输出响应:
// 仅当上游明确上报 output==0 且聚合出的终止原因不在合法空输出终态白名单内时判无效。
func rejectZeroOutput(ctx context.Context, format llm.APIFormat, inbound transformer.Inbound, window []*httpclient.StreamEvent) error {
	if len(window) == 0 {
		return errors.New("upstream stream ended without events")
	}
	body, meta, err := inbound.AggregateStreamChunks(context.WithoutCancel(ctx), window)
	if err != nil {
		// 聚合失败无法证明输出为零, 不触发重试。
		return nil
	}
	if uerr := validateRoundUsage(meta.Usage, gjson.GetBytes(body, "choices.0.finish_reason").String()); uerr != nil {
		return fmt.Errorf("%w: %s", uerr, errBodySnippet(window[len(window)-1].Data))
	}
	return nil
}
