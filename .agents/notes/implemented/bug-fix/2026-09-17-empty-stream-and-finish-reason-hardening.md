# Agent Note: 空流合成终止事件检测与 finish_reason 白名单扩展

Status: implemented

## 问题

对接 opencode zen 端点（`https://opencode.ai/zen`，`union-alpha` 模型，Anthropic 原生协议）时暴露两个流验证缺陷：

### 缺陷 1：空流被当成功交付

zen 在降级状态下对 `/v1/messages` 返回 HTTP 200 + 0 字节 SSE body。转换路径（OpenAI Chat 客户端 → Anthropic 上游）中，axonhub/llm 的 Anthropic outbound transformer（`transformer/anthropic/outbound_stream.go`）**无条件向流追加 `llm.DoneStreamEvent`**（即 `[DONE]` 哨兵），即使上游返回 0 字节。这使得空流变成"只含 `[DONE]` 的单事件流"。

`readStreamWindow`（`internal/relay/upstream.go`）预读该流时：
- `[DONE]` 被判定为 `terminal=true`（终止事件），触发 `rejectZeroOutput` 检查；
- `rejectZeroOutput` 聚合窗口事件得到 nil usage（无 Anthropic 事件可聚合），`validateRoundUsage(nil, "")` 在 `usage == nil` 时返回 nil（设计为"未上报用量不等于 0"）；
- 结果：空流通过所有校验，以"成功终止"交付给客户端——客户端收到 `data: [DONE]\n\n` 后认为请求成功但无内容。

passthrough 路径（Anthropic 客户端 → Anthropic 上游）不受此缺陷影响：原始 SSE 为空时 `readStreamWindow` 在循环结束时返回 `errStreamEarlyEof`（无事件可读）。但转换路径的合成 `[DONE]` 绕过了该检查。

### 缺陷 2：finish_reason "other" 被判异常终止

部分第三方 OpenAI 兼容上游在模型因非标准原因正常停止时使用 `finish_reason: "other"`。该值不在 `validChatFinishReason` 白名单（`""`, `"stop"`, `"length"`, `"tool_calls"`, `"function_call"`）中，被 `analyzeChatStreamEvent` 判定为 `abnormalErr`，导致整轮按失败处理并换目标重试。所有成员返回同一值时请求最终失败，客户端无法获得上游已产出的有效响应。

## 决定

### 修复 1：readStreamWindow 检测"无内容无终止原因的合成终态"

在 `readStreamWindow` 的预读循环中增加两个跟踪标志：

- `sawContent`：窗口内是否出现过任何内容承载信号（`verdict.hasContent`，即文本增量/工具调用/推理内容）；
- `sawFinishSeen`：窗口内是否出现过白名单内的非空终止原因（`verdict.finishSeen`，即 OpenAI Chat 的 `finish_reason` 或 Anthropic 的 `stop_reason` 为白名单内非空值）。

当终止事件（`verdict.terminal == true`）到达时，检查顺序为：

1. **先调用 `rejectZeroOutput`**：如果上游明确上报了 0 输出用量（`usage.CompletionTokens == 0`），返回 `errZeroOutput`（更具体的错误分类，优先于 early_eof）；
2. **再检查 `!sawContent && !sawFinishSeen`**：如果终止事件前既无内容信号也无白名单内终止原因，返回 `errStreamEarlyEof`。这精确命中"上游 0 字节 + outbound transformer 合成 `[DONE]`"的形态——合成 `[DONE]` 不携带 `finish_reason`，故 `sawFinishSeen` 为 false；
3. **豁免**：如果上游已发白名单内的非空终止原因（如 `finish_reason: "stop"` + `[DONE]`），即使无内容增量也视为合法空完成（模型主动停止），不拦截。这保留了 `TestReadStreamWindowDeliversWhenUsageMissing` 验证的行为。

`rejectZeroOutput` 先于 `!sawContent && !sawFinishSeen` 执行，确保 `TestReadStreamWindowRejectsExplicitZeroOutput`（上游明确上报 `completion_tokens=0`）仍返回 `errZeroOutput` 而非 `errStreamEarlyEof`，保持错误分类的精确性。

### 修复 2：validChatFinishReason 白名单加入 "other"

在 `validChatFinishReason`（`internal/relay/protocol.go`）的 switch 中增加 `"other"` 分支。`validUnifiedFinishReason` 委托给 `validChatFinishReason`，统一层同步放行。

`"other"` 是合法的生成终止信号（模型已停止），不是错误或拒答。`"content_filter"` 仍判异常（拒答式终态按设计换目标重试），与 `validAnthropicStopReason` 中 `"refusal"` 的放行不矛盾——axonhub 把 Anthropic `refusal` 映射为 `content_filter`，但 Anthropic 原生协议的 `refusal` 仍在 Anthropic 白名单内放行。

## 备选方案

### 为什么不修改 axonhub/llm 的 outbound transformer（不追加合成 DoneStreamEvent）？

axonhub/llm 是外部依赖模块（`github.com/looplj/axonhub/llm`），修改需 fork 并维护 vendor 偏离。且合成 `[DONE]` 的行为对正常流是正确的（OpenAI Chat 协议要求 `[DONE]` 哨兵收尾）——只在空流时才是问题。在 NovaVeil 侧拦截更精确、影响面更小。

### 为什么不在 rejectZeroOutput 中检查聚合后 body 是否有内容？

聚合 body 的内容检查需要按格式分别实现（OpenAI Chat 检查 `choices[].delta.content`、Anthropic 检查 `content[].text` 等），且 aggregator 对 `[DONE]` 的处理（跳过）使得聚合 body 为空——判断"空 body"与"nil usage"的语义重叠，难以区分"合法空完成"与"合成终态"。用 `sawContent` / `sawFinishSeen` 在事件级别判断更精确，且复用已有的 `analyzeStreamEvent` 判定，零额外解码。

### 为什么不把 "other" 映射为 "stop" 而是加入白名单？

映射会丢失上游的原始语义信息（"other" 不等于 "stop"）。加入白名单让原始值透传给客户端，客户端可自行解释。axonhub 的统一层不做额外映射，保持中间层透明。

## 后果

- **收益**：转换路径的空流不再被当成功交付，客户端收到明确的错误响应（`errStreamEarlyEof` → 重试 → 耗尽后返回 `暂无可用渠道`）而非误导性的空 `[DONE]`。`finish_reason: "other"` 的响应不再被误判为异常终止，避免对健康响应的无谓重试。
- **代价与已知上限**：`!sawContent && !sawFinishSeen` 检查在极少数合法空完成场景（模型主动停止且上游不发 `finish_reason`/`stop_reason` 只发 `[DONE]`/`message_stop`）会触发 early_eof 重试。这类上游不符合 OpenAI/Anthropic 协议规范（正常完成应携带终止原因），重试一次后仍空则按失败处理，行为可接受。如果后续发现合规上游被误拦截，可考虑放宽为仅检查 `!sawContent`（不要求 `sawFinishSeen`），但当前选择更保守的口径以防止合成终态漏网。
- **测试覆盖**：`internal/relay/bug1_regression_test.go` 验证五种空流形态（`[DONE]`-only、`finish_reason`-only、`message_stop`-only、结构帧+`message_stop`、`response.completed`-only）均返回 `errStreamEarlyEof`，覆盖 OpenAI Chat、Anthropic、OpenAI Response 三种客户端协议格式；正常流（内容+终止）和合法空完成（`finish_reason: "stop"` + `[DONE]`、`stop_reason: "end_turn"` + `message_stop`）不受影响。`TestReadStreamWindowOtherFinishReasonExempt` 验证 Bug 1 × Bug 3 交互路径：`finish_reason: "other"` + `[DONE]` 无内容时因 `finishSeen=true` 豁免早期 EOF。`finish_guard_test.go` 的白名单契约表 `TestValidChatFinishReason` 和统一层回归 `TestValidateResponseFinishWhitelist` 均已同步补充 `"other"` 用例。现有测试 `TestReadStreamWindowDeliversWhenUsageMissing` 和 `TestReadStreamWindowRejectsExplicitZeroOutput` 保持通过。

## 验证

```
go test -v -run 'TestReadStreamWindowTerminalWithoutContent|TestReadStreamWindowContentBeforeTerminal|TestValidChatFinishReasonOther' ./internal/relay/
go test ./internal/relay/    # 全量回归
go test ./...                # 全包回归
```

集成验证（zen 降级状态下）：三种客户端协议的流式请求经 NovaVeil 转换/透传路径，修复前 OpenAI Chat 返回 `data: [DONE]`（HTTP 200 空成功）、Anthropic 挂起直到超时，修复后均返回 `{"error":{"message":"暂无可用渠道"}}`（HTTP 400，重试耗尽后正确报错）。非流式请求不受影响（zen 非流式路径返回有效 JSON 响应）。
