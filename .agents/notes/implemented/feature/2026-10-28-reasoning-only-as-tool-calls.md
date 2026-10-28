# Agent Note: 分组级思考-only 轮次改写为 tool_calls

Status: implemented

## 问题

OpenCode 免费档上的 DeepSeek 思考模型会间歇性产出「只思考、不回答、不调工具」的坏轮次：上游先持续下发 `delta.reasoning_content`，最后以 `"finish_reason":"stop"` 结束，既没有 `content`，也没有 `tool_calls`。

网关现有的 `errNoAnswerStop` 会把这类轮次判为失败：非流式换目标重试，已提交的流抑制终止帧并静默截断。这个默认口径保证不把空回答伪装成成功，但依赖 `finish_reason` 决定是否继续的客户端拿不到可续跑信号。

## 决定

复用原分组的旧键 `reasoning_fallback_to_text`（`GroupRelayConfig.ReasoningOnlyAsToolCalls`），只保留一个开关，默认 false。旧分组已保存的 `true` 继续生效，不新增第二个字段，也不做数据迁移。

开启后，仅当 OpenAI Chat 客户端协议同时满足以下条件时改写终态：

- `choices[0].finish_reason == "stop"`
- `choices[0].message` 没有文本回答与工具调用
- `choices[0].message.reasoning_content` 是非空字符串

非流式与折叠流路径由 `applyReasoningOnlyAsToolCalls` 把 `finish_reason` 改为 `tool_calls`，`content`、`reasoning_content` 与用量原样保留，再走既有校验。流式路径在终止块直接改写 `finish_reason`，并同步终态跟踪值，避免后续收尾逻辑又把它恢复成 `stop`；不再置 `noAnswerStop`。

改写只改终态，不伪造 `tool_calls` 数组。开关只认顶层分组配置，探测与渠道测试路径固定传 false。

## 备选方案

- **把推理内容降级为 `content`**：实现过，但依赖 `finish_reason` 续跑的客户端仍把它当自然结束，实际不继续，救不回坏轮次。
- **新增第二个开关并保留旧开关**：部署后会出现两个语义相近的开关，旧值还需要迁移；直接复用旧键更符合「一个开关一个行为」。
- **同时伪造一个 `tool_calls` 数组**：能让响应结构自洽，但会凭空创造一个工具调用，客户端可能执行不存在的工具或报参数错误，属于伪造上游行为。
- **默认开启改写**：会把上游失败轮次伪装成工具轮次，存量分组行为突变，因此保持默认关闭。
- **只做失败重试**：不改变协议，但每次坏轮次都要整轮重试，免费档成本与延迟都高。

## 后果

- **收益**：开启开关的分组可以把思考-only 坏轮次变成 `tool_calls` 终态，让依赖该终态的客户端继续流程，不再整轮失败。
- **代价**：响应只有 `finish_reason: "tool_calls"`，没有真实 `tool_calls` 数组；不符合严格协议校验的客户端可能直接报错或空转。这是显式开关承担的取舍。
- **默认关闭**：存量分组行为不变，无需数据迁移。
- **作用面**：仅 OpenAI Chat 客户端协议；Anthropic 与 Responses 的截断终态仍按既有异常路径处理。

## 验证

```sh
go test ./internal/relay/ -run 'TestApplyReasoningOnlyAsToolCalls|TestForwardNonStreamReasoningOnlyAsToolCalls|TestForwardStreamingReasoningOnlyAsToolCalls' -v
go test ./internal/relay/ -run 'TestForwardStreamingNoAnswerStopSilentTruncation|TestForwardNonStreamNoAnswerStopFailoverToHealthyMember' -v
cd web-next && pnpm test src/lib/types.test.ts
```

- 前两组验证开关开启后的非流式与流式终态改写，以及开关关闭时既有失败语义不变。
- 前端用例校验 `DEFAULT_GROUP_RELAY_CONFIG.reasoning_fallback_to_text` 默认 false。
