# Agent Note: 工具轮次的终止原因下发与思考-only 空回答拦截

Status: implemented

## 问题

opencode 一类 Agent CLI 经本网关调用模型时，模型明确返回了工具调用（`read` / `pwsh` 等），网关请求日志里 `finish_reason` 也是 `tool_calls`，但客户端收到后把整轮当作正常完成，不再派发工具，会话就此结束。

排查到三个独立成因，都发生在下发侧：

### 成因 1：合成收尾帧硬编码 stop

上游是合法终态但缺 `[DONE]` 哨兵时，转发循环会调用 `terminalStreamFrames` 补一帧收尾。该函数把 `finish_reason` 写死为 `"stop"`，于是同一轮里先下发了 `tool_calls` 终止块、随后又下发一帧 `stop`。客户端以最后一个 `finishReason` 为准，工具轮次被判成自然结束。

### 成因 2：上游错报 stop

上游在携带工具调用增量的轮次里报 `finish_reason: "stop"`（部分兼容实现在工具轮次收尾时不做区分）。网关原样转发，客户端同样认为整轮完成。

### 成因 3：完全渠道透传跳过空回答校验

`sendPassthrough` 在 `PassThroughBodyEnabled` 下直接返回原始响应体，跳过 `validateRoundAnswer`。上游返回 `finish_reason: "stop"` 且 `message` 只有 `reasoning_content`、没有正文也没有工具调用时，这轮"看起来成功但没有答案"的响应被原样交给客户端。

## 决定

### 事件判定补两个信号

`streamEventVerdict` 增加 `toolCall`（本事件是否携带工具调用增量）与 `finishReason`（本事件携带的白名单内非空终止原因原文）。`analyzeChatStreamEvent` 从 `delta.tool_calls` 与 `choices[].finish_reason` 填充；Anthropic 分支从 `content_block_start.content_block.type == "tool_use"`、`content_block_delta.delta.type == "input_json_delta"` 与 `message_delta.delta.stop_reason` 填充。

### 终态统一校正

`resolveTerminalReason(format, reason, sawToolCall)` 是唯一的校正入口：本轮出现过工具调用增量时，自然结束一律升级为工具终态（OpenAI Chat 为 `tool_calls`，Anthropic 为 `tool_use`）；其余终态（`length` / `max_tokens` / 已声明的工具终态）原样保留；终态缺失时按协议默认值兜底。Responses 协议没有 `finish_reason` 字段，不参与校正。

### 两条下发路径都走校正

- 转发期：本轮出现工具调用增量后，携带 `stop` / `end_turn` 的终止块在下发前用 `rewriteStreamFinishReason` 原地改写为工具终态，再进入聚合与日志，保证客户端与日志看到同一终态。
- 补帧期：`terminalStreamFrames(format, reason)` 接收校正后的终态，不再写死 `stop` / `end_turn`。

### 透传非流式补空回答校验

`sendPassthrough` 在完全透传分支保留 `validateRoundAnswer` 调用：`stop` + 仅有 `reasoning_content` 的轮次整轮判无效，按既有换渠道重试口径处理，而不是把空回答当成功交付。其余校验（`validateResponse` / `validateRoundUsage`）在透传下仍跳过。

## 备选方案

### 为什么不直接把常量改成 `tool_calls`

无条件的 `tool_calls` 会让纯文本轮次也报工具终态，客户端拿到空工具列表后空转或直接报错，比"提前结束"更难排查。工具终态只能由本轮实际出现的工具调用增量推导。

### 为什么只在补帧处校正、不改写转发期终止块

只能覆盖成因 1。上游明确发了 `stop` 的轮次（成因 2）会以 `terminalSeen == true` 走正常转发，不会触发补帧，客户端仍收到 `stop`。既然判定信号已经拿到，转发期一处改写即可覆盖两种成因。

### 为什么不在 axonhub/llm 的 inbound 聚合里改

`openAIInboundStream.finalize` 本身已有"有工具则补 `tool_calls`"的逻辑，但它只在 `s.usage` 为真时执行，上游不报用量就不补帧；且该库是外部依赖，改它要维护 vendor 偏离，而终态属于本网关的下发契约，在本仓库收口更合适。参考 [空流与终止原因硬化](2026-09-17-empty-stream-and-finish-reason-hardening.md)。

### 为什么透传仍不恢复其余校验

完全透传的设计口径是"响应原样交给客户端"，`validateResponse` / `validateRoundUsage` 会改变透传语义并引入扣费与终态记账假设。思考-only 空回答是其中唯一会让客户端误判成功的形态，单独拦截即可。

## 后果

- **收益**：工具轮次在下发侧保持 `tool_calls` / `tool_use`，Agent CLI 正常派发工具；日志与客户端终态一致，排查时不再出现"日志是 tool_calls、客户端当 stop"的错位。思考-only 空回答在透传非流式路径不再被当作成功完成。
- **代价**：转发期对终止块多一次 `sjson` 改写，仅在该轮出现过工具调用时触发，每轮至多一次。Anthropic 的工具调用识别依赖 `content_block_start` / `content_block_delta` 的事件类型，若上游把工具块塞进其他事件类型则识别不到，退化为原行为。
- **已知上限**：`function_call` 老格式与 Responses 协议不做工具终态校正，保持上游原值。
- **测试**：`internal/relay/finish_toolcall_test.go` 覆盖校正规则、合成帧终态、缺 `[DONE]` 的工具轮次、上游错报 `stop` 的改写，以及纯文本轮次不被误改。`fallback_test.go` 的 `terminalStreamFrames` 调用已同步新签名。

## 验证

```
go test -v -run 'TestResolveTerminalReason|TestTerminalStreamFramesCarryReason|TestForwardStreamingToolCall' ./internal/relay/
go test ./internal/relay/
go test ./...
```
