# Agent Note: 撤销思考-only 轮次的 relay 侧补偿补丁

Status: implemented

## 问题

2026-10-28 的一批 relay 改动把「思考轮次失败或断流」当成上游与协议层的问题，在网关下发侧叠了三层补偿：分组开关把思考-only 轮次的 `stop` 终态改写成 `tool_calls`、转发期与补帧期统一校正工具轮次终态、终止块之后被截断的流按完整响应收尾。

真正根因不在 relay：免费档走 bun sidecar，`Bun.serve` 默认 `idleTimeout` 为 10 秒且对流式响应同样生效，思考阶段静默超过阈值就被本进程掐断连接，Go 侧只读到 `unexpected EOF`（见 [关闭 bun sidecar 的 10 秒空闲掐流](../bug-fix/2026-10-28-bun-sidecar-idle-timeout.md)）。sidecar 修复后这些补偿失去触发场景，留着反而把失败伪装成成功：空回答被当成工具轮次，传输截断被当成完整交付。

## 决定

这批 relay 改动整体撤销，代码回到 `c7fef92` 状态，相关能力不再存在：

- `GroupRelayConfig` 删 `reasoning_fallback_to_text` 字段、兼容键 `reasoning_only_as_tool_calls` 的反序列化分支与默认值；前端分组编辑器的开关与类型定义同步删除。
- 删 `applyReasoningOnlyAsToolCalls`、窗口预读的改写分支、`rewriteWindowStopFinish`；零输出校验恢复「先按原终态校验、再交付」的原顺序。
- 删 `resolveTerminalReason`、`rewriteStreamFinishReason`、`streamEventVerdict` 的 `toolCall` 与 `finishReason` 信号；`terminalStreamFrames` 恢复单参数签名，合成收尾帧回到协议默认终态。
- 删 `isStreamTruncation` 豁免；终止原因之后被截断的流重新按「提交后失败」静默截断。
- 完全渠道透传的非流式路径恢复「不解析、不校验」原口径，不再补 `validateRoundAnswer`。

思考-only 空回答回到默认口径：非流式整轮判 `errNoAnswerStop` 并换目标重试，已提交的流抑制终止帧后静默截断，不把空回答伪装成成功。

保留两项与本功能无关的改动：bun sidecar 的 `idleTimeout: 0` 根因修复，以及 agent-notes 归档封印的换行归一。

## 备选方案

### 保留开关、默认关闭

代价是留着分组开关、兼容键、前端控件与文档字段，且开关一旦被打开仍然把失败轮次伪装成工具轮次。诉求是删行为，不是留可选项。

### 只删开关、保留工具轮次终态校正

校正的驱动场景就是「改写出的 `tool_calls` 又被收尾帧降级为 `stop`」，开关删掉后只剩「上游在工具轮次错报 `stop`」这一条兜底。该兜底是独立判断，需要真实案例支撑，不在本次保留范围内；日后重现再单独引入。

### 保留截断收尾豁免

该豁免只认 `io.EOF` 与 `io.ErrUnexpectedEOF`，并要求已见白名单终止原因，看似安全。但它把传输层截断按成功交付；sidecar 修好后剩下的同类错误才是真截断，掩盖它违背「不造假」。

### 把 `stop` 加进合法空输出白名单

同样能让零输出思考轮次通过，但会连真正的空回答一起放行，客户端收到空成功比收到失败更糟。

## 后果

- **收益**：relay 少三层补偿与一个分组配置，失败语义回到单一来源；空回答与传输截断重新可观测，不再被记成成功。
- **代价**：若上游确实存在「`stop` + 仅 `reasoning_content`」的坏轮次，客户端仍整轮重试；这是显式失败，比伪装成工具轮次更可控。
- **作用面**：仅 OpenAI Chat 客户端协议的思考轮次；Anthropic 与 Responses 本来就不参与改写。
- **重新引入条件**：先在身份干净的下游形态下单发确认坏轮次确实来自上游，再单独评估；不得复用已删的开关名，也不得把传输层 EOF 当作生成完成的证据。

## 验证

```sh
go build ./... && go vet ./...
go test ./internal/relay/ ./internal/model/
cd web-next && pnpm typecheck
pnpm verify-notes
```

- 前两条确认回滚后编译与单测通过，`internal/relay` 既有的 no_answer_stop 回归仍应全绿。
- 第三条确认前端无残留字段引用。
- 第四条校验笔记树结构与格式。
