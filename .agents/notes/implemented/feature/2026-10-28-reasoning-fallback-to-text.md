# Agent Note: 分组级 reasoning 降级为正文

Status: implemented

## 问题

OpenCode 免费档上的 DeepSeek 思考模型会间歇性产出「只思考、不回答、不调工具」的坏轮次：上游先持续下发 `delta.reasoning_content`，最后以 `"finish_reason":"stop"` 结束，既没有 `content`，也没有 `tool_calls`。

网关现有的 `errNoAnswerStop` 会把这类轮次判为失败：非流式换目标重试，已提交的流抑制终止帧并静默截断。这个默认口径保证不把空回答伪装成成功，但用户如果接受「思考即正文」，坏轮次本可以被救回来，不必整轮重试。

## 决定

新增分组级开关 `reasoning_fallback_to_text`（`GroupRelayConfig.ReasoningFallbackToText`），默认 false，旧分组 JSON 反序列化自动为关。

开启后，仅当 OpenAI Chat 客户端协议同时满足以下条件时降级：

- `choices[0].finish_reason == "stop"`
- `choices[0].message` 没有文本回答与工具调用
- `choices[0].message.reasoning_content` 是非空字符串

非流式与折叠流路径由 `applyReasoningFallback` 把推理原文写入 `message.content`，保留 `finish_reason` 与 `reasoning_content` 原样后再走既有校验。流式路径在转发循环累计首条 choice 的推理增量，命中条件时把全文补进终止块的 `delta.content`，然后按正常 stop 收尾；不再置 `noAnswerStop`。

开关只认顶层分组配置。`sendPassthrough` 与 `sendConverted` 通过新增的布尔参数接收，探测与渠道测试路径固定传 false。

## 备选方案

- **默认开启降级**：实现最简单，但会把上游失败轮次伪装成成功，思考中的计划和试探可能被下游当作回答，违反「不把坏响应当成功交付」的默认口径。
- **按模型名自动判断**：只对 DeepSeek 免费档生效，但模型名会变，代理重命名后判断失效，且仍无法区分「思考正好等于正文」与「思考只是计划」。
- **把推理写入历史 assistant 消息供下一轮继续**：历史 `reasoning_content` 的协议语义不等于正文，部分上游不支持回传，且会污染会话状态；本次只处理当前轮交付。
- **在网关内自动追加一次「请继续」重试**：会改变客户端看到的轮次语义，且重试成本与重复调用风险不可控。

## 后果

- **收益**：开启开关的分组可以把部分思考-only 坏轮次救成可用回答，减少整轮重试与客户端断开。
- **代价**：思考文本会先以 `reasoning_content` 下发一次，终止块再以 `content` 补发一次，客户端可能看到重复内容；如果思考里只是「我要调用某工具」，下游会收到这段计划文本但不会收到真实工具调用。
- **默认关闭**：存量分组行为不变，无需数据迁移。
- **作用面**：仅 OpenAI Chat 客户端协议；Anthropic 的 `max_tokens` 截断与 Responses 的 `incomplete` 仍按既有异常路径处理。

## 验证

```sh
go test ./internal/relay/ -run 'TestApplyReasoningFallback|TestForwardNonStreamReasoningFallbackToText|TestForwardStreamingReasoningFallbackToText' -v
go test ./internal/relay/ -run 'TestForwardStreamingNoAnswerStopSilentTruncation|TestForwardNonStreamNoAnswerStopFailoverToHealthyMember' -v
cd web-next && pnpm test src/lib/types.test.ts
```

- 前两组验证开关开启后的非流式与流式降级，以及开关关闭时既有失败语义不变。
- 前端用例校验 `DEFAULT_GROUP_RELAY_CONFIG.reasoning_fallback_to_text` 默认 false。
