# Agent Note: 上游工具调用参数非法时快速失败

Status: implemented

## 问题

DeepSeek 等上游偶尔在 OpenAI Chat 的 `tool_calls[].function.arguments` 中生成漏转义的 JSON 字符串。模型示例里 `old_string` 的普通引号已转义，但内嵌 base64 片段的一对引号没有转义，客户端按 JSON 解析时报 `Expected ',' or '}' after property value at position 122/125`。NovaVeil 原先把这段 `arguments` 原样透传给客户端：非流式客户端拿到无法执行的工具调用，流式客户端在完整收尾后才发现参数无法解析。

2026-11-14 复现自 KeyHome 会话：turn 89、90、91 连续以 `MALFORMED_RESPONSE` 结束，用户后两轮只回复“继续”。这是上游模型生成问题，不是 NovaVeil 的 JSON 序列化或工具别名适配层损坏；但网关应在交付前识别并让本轮失败重试，而不是把坏参数当成功响应。

## 决定

- 非流式 OpenAI Chat：在 `validateRoundAnswer` 之后、工具别名还原之前调用 `validateToolCallArguments`。只检查字符串类型的 `message.tool_calls[].function.arguments` 与旧式 `message.function_call.arguments`；空字符串放行，非字符串忽略，非 Chat 协议不检查。非法时返回 `errInvalidToolCallArguments`，复用既有换目标重试。
- 流式官方工具缓冲：`officialToolStream.flushPending` 在合成工具调用前检查累计 `arguments`。任一非空参数不是合法 JSON 时，记录 `errInvalidToolCallArguments`、置 `aborted`、清空 pending/order，不产出任何工具调用事件；`consume` 的 finish 与 `[DONE]` 分支在错误状态下不再追加终止帧，`Err()` 返回该错误，让 handler 按流错误结束并由客户端重试。
- 不自动修复、不补引号、不重新编码模型生成的参数。网关只做交付前校验，避免把错误调用伪装成成功。

## 备选方案

- **自动修复 JSON**：例如给内嵌引号补转义或截断到可解析位置；可能改变工具语义，且无法判断模型真实意图；否掉。
- **只告警后继续透传**：保留“客户端或许能自己容错”的可能，但实际客户端会直接失败，且流式收尾后无法再重试；否掉。
- **只做非流式校验**：非流式能换目标重试，但流式是主要使用形态，坏参数仍会交付；否掉。
- **对所有流式协议做通用缓冲校验**：需要额外缓冲任意协议的完整工具参数，改变转发路径和内存上限；本次只覆盖已有官方工具缓冲层，避免扩大范围；暂不做。

## 后果

- **收益**：非法工具参数不再作为成功响应交付；非流式可自动换目标重试，流式会以错误断流，客户端可重试。
- **代价与已知上限**：模型连续生成坏参数时可能连续失败，消耗重试次数与 token；流式在已有内容后才发现坏参数时，客户端会看到已输出内容后断流；空参数仍放行，由工具自身处理；非 OpenAI Chat 协议依赖其结构化输入校验。
- 上游成功返回后才会进入本校验，不改变路由、计费或用量统计口径。

## 验证

- `go test ./internal/relay -run 'TestValidateToolCallArguments|TestOfficialToolStreamAbortsInvalidArguments' -count=1`
- `go test ./internal/relay -count=1`
- `pnpm verify-notes`

以上命令由用户执行。本次不修改模型输出，只改变网关的失败边界。
