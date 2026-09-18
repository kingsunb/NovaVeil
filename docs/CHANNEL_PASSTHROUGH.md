# 完全渠道透传 (Complete Channel Passthrough)

> 本文档描述完全渠道透传的**当前运行机制**（事实）。决策理由、备选方案与参考实现见 [Agent Note](../.agents/notes/implemented/feature/2026-09-11-complete-channel-passthrough.md)。前端开关已实现（渠道编辑器「完全渠道透传」开关，见 web-next/src/pages/channels/channel-editor.tsx）。

## 机制

完全渠道透传是渠道级开关 `PassThroughBodyEnabled`（`Channel` 模型字段，`gorm:"not null;default:false"`，默认关）。开启后该渠道接受任意客户端协议，请求体、URL 路径、响应体全部原样透传至上游，不做任何协议转换。**完全透传不跳过分组路由**：请求仍经过 提取 model → 查分组 → 选渠道（含 failover、Key 轮询）→ 透传至上游。「完全」指任意协议都原样转发，不是绕过路由。

判定与执行：

- `supportsNativeFormat(channel, format)`：当 `channel.PassThroughBodyEnabled` 为 true 且渠道非 custom（固定回复）时，任意格式均返回 true，走 `sendPassthrough` 而非 `sendConverted`。
- `buildPassthroughRequest`：当 `PassThroughBodyEnabled` 为 true 且非 `##` 完整 URL 时，用客户端原始请求路径 `raw.Path`（如 `/v1/messages`，剥去 `/v1` 前缀）替代 `upstreamPath(format)`，交给 `BuildRequestURL` 与 base 拼接。
- Auth 按客户端协议（`format`）适配：Anthropic 格式用 `X-API-Key`，其余用 `Bearer`。完全透传下无需修改，auth 由 format 决定而非渠道类型。
- 非流式响应：当 `PassThroughBodyEnabled` 为 true 时，`sendPassthrough` 跳过 `outbound.TransformResponse` / `validateResponse` / `validateRoundUsage` / `validateRoundAnswer`，直接返回原始响应体。outbound transformer 按渠道类型创建（如 OpenAI 渠道 → OpenAI outbound），但完全透传下响应格式由客户端协议决定（可能是 Anthropic），用渠道 outbound 解析会失败。
- 流式响应：`sendPassthroughStream` 用 `format`（客户端格式）调用 `analyzeStreamEvent` 和 `inboundForFormat`，不依赖 outbound transformer，多协议透传下流式事件分析天然正确。
- custom 渠道：`supportsNativeFormat` 和 `buildOutbound` 对 custom 始终返回 `passthrough=false`，透传对它无意义。

## 支持透传的 handler

| Handler | 透传 | 说明 |
|---------|------|------|
| `compatible_handler.go` (OpenAI Chat) | ✅ | 对话 |
| `claude_handler.go` (Anthropic) | ✅ | Claude Messages |
| `image_handler.go` (图片) | ✅ | 图片生成/编辑 |
| `responses_handler.go` (Responses) | ✅ | OpenAI Responses |
| `gemini_handler.go` (Gemini) | ✅ | Gemini |
| `rerank_handler.go` (Rerank) | ✅ | 重排序 |
| `audio_handler.go` (语音) | ❌ | 无透传，始终经 adaptor |
| `embedding_handler.go` (嵌入) | ❌ | 无透传，始终经 adaptor |

## 透传跳过 / 保留的内容

**跳过**：请求体协议转换（`ConvertOpenAIRequest` 等）、字段过滤（`RemoveDisabledFields`）、参数覆盖（`ApplyParamOverride`）、推理强度转换（`ReasoningEffort`）、系统提示词注入、响应解析与校验。

**保留**：渠道选择 + 故障转移、预扣费 / 后扣费、Model mapping（模型名替换）、Header 透传（独立机制）、多 Key 轮询（Key 选择在 `sendPassthrough` 之前完成）。

## Header 透传（独立机制）

自定义头透传通过渠道的 `CustomHeader` 实现：`internal/helper/fetch.go` 的 `applyCustomHeaders` 将自定义头写入上游请求（受保护的凭据头不覆盖真实渠道凭据）；自定义头值支持 `{client_header:xxx}` 占位符，`internal/relay/channel.go` 在构造请求时将该片段替换为客户端请求头 `xxx` 的实际值。

## applyChannelConfig 在透传下的行为

完全透传下 `applyChannelConfig` 仍执行：multipart 请求跳过所有 JSON 修改只应用自定义头；非对话 JSON 跳过 `applyChannelModelLimits` 但 `ParamOverride` 仍应用；对话 JSON 全量应用（model limits + ParamOverride）。`ParamOverride` 仍会修改请求体 JSON——若需更纯粹的透传（完全不动请求体），可在 `applyChannelConfig` 中检查 `PassThroughBodyEnabled` 并跳过；当前保留 `ParamOverride` 以允许管理员在透传时注入参数。

## 调用链路

### 完全透传开启时

```
客户端请求 (任意协议)
  → handler.go: Forward(format)
    → readLimitedHTTPRequest: raw.Path = "/v1/messages"
    → buildOutbound(channel, format)
      → supportsNativeFormat(channel, format)
        → channel.PassThroughBodyEnabled == true → return true
      → passthrough = true
    → sendPassthrough(format, raw, channel, outbound, streaming)
      → buildPassthroughRequest: path = TrimPrefix(raw.Path, "/v1") → url = base/v1/messages
      → auth = X-API-Key (format == AnthropicMessage)
      → body = raw.Body (原样)
      → 非流式: PassThroughBodyEnabled → 直接返回原始响应体
      → 流式: sendPassthroughStream → analyzeStreamEvent(format) → 原样转发 SSE 事件
    → 写回客户端
```

### 完全透传关闭时

```
客户端请求
  → supportsNativeFormat(channel, format)
    → PassThroughBodyEnabled == false → 按渠道类型 + 格式硬编码匹配
  → 匹配: sendPassthrough (同协议透传)
  → 不匹配: sendConverted (经 axonhub 转换)
```

## 边界情况

1. **不计费**：完全透传下 `usage = nil`，不产生用量记录和扣费。需计费则要按客户端格式额外实现响应体解析。
2. **不校验**：跳过 `validateResponse` / `validateRoundUsage` / `validateRoundAnswer`，上游返回的 200 下发错误、空输出、异常 finish_reason 均不会被拦截重试。
3. **ParamOverride 仍生效**：`applyChannelConfig` 仍注入 `ParamOverride`，完全不动请求体需在 `applyChannelConfig` 中也检查 `PassThroughBodyEnabled`。
4. **自定义头仍追加**：`CustomHeader` 仍会被追加到上游请求。
5. **多 Key 轮询不受影响**：Key 选择和轮询在 `sendPassthrough` 之前完成。
6. **`##` 模式**：BaseURL 以 `##` 结尾时 URL 已完整不追加路径，`PassThroughBodyEnabled` 对 URL 无影响，但仍影响响应处理（跳过 TransformResponse）。
7. **custom 渠道**：`PassThroughBodyEnabled` 对 custom（固定回复）渠道无效。
8. **Gemini / Volcengine 渠道**：`buildOutbound` 对这两种渠道硬编码 `return outbound, false, err`。改为 `return outbound, passthrough, err` 不会出错（透传下不用于 TransformResponse），但 Gemini 上游通常不支持 OpenAI/Anthropic 协议，开了也没意义。

## 前端

渠道编辑器（`web-next/src/pages/channels/channel-editor.tsx`）已实现「完全渠道透传」开关，位于高级设置区域，绑定 `pass_through_body_enabled` 字段，随保存写入渠道配置。
