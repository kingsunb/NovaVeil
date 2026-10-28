# Agent Note: 终止原因之后被截断的流按完整响应收尾

Status: implemented

## 问题

OpenCode 免费档的思考轮次会让客户端反复「断开」重连，服务端错误日志留下一条 `unexpected EOF`（归类 `upstream_network`）。链路是：上游先下发推理增量，再下发携带 `finish_reason` 与用量的终止块，然后在分块结束前把连接断掉——SSE 解码器把该读取失败写进 `Err()`，转发循环把它当成首个真实失败，于是 `frameFailure` 非空、不再补发 `[DONE]`/`message_stop`，按「提交后失败」静默截断并记失败。

但此时生成其实已经完整结束：白名单内的终止原因已经到达客户端，缺的只是协议哨兵。客户端看到「有 `finish_reason`、没有 `[DONE]`」的流会判定断流并重试整个请求，把已完整的回答丢弃重算。

这和既有的「干净 EOF 但缺哨兵」形态是同一个语义，只是传输层用 `io.ErrUnexpectedEOF` 而不是 `io.EOF` 告诉了我们同一件事，旧逻辑却把它们判成了相反的结果。

## 决定

在转发循环取到 `events.Err()` 之后、定稿之前插入一条豁免：`roundFinished && !polluted && isStreamTruncation(firstErr)` 时清空 `firstErr`，让流程落回既有的「缺哨兵补帧」分支——补发协议终止帧、按成功定稿、继续采集 reasoning 回放缓存。

- `isStreamTruncation` 只认 `io.ErrUnexpectedEOF` 与 `io.EOF`：两者都表示「字节流结束」这一件事，不包含上下文取消、超时、`ECONNRESET` 等无法证明内容已完整的错误。
- `roundFinished` 是硬门槛：只有白名单内的非空终止原因已经解码成功，才承认生成结束。终止块本身被截断（解码失败）时该标志为假，仍按真截断失败处理，静默截断且不补发任何终止帧。
- `polluted` 优先级不变：异常终止原因已被抑制的流仍静默截断，不补成功的哨兵。

## 备选方案

- **维持原状（一律按失败静默截断）**：服务端能证明生成已经结束，却要求客户端把完整回答丢掉重试，免费档每条回答都要重算一遍，且面板上永远是失败。不可取。
- **只补哨兵、仍按失败定稿**：客户端不再重试，但同一响应在面板上是失败、在客户端是成功，成员还被计一次提交后连击并可能冷却，语义分裂。若后续需要把「上游传输抖动」在可观测性上单独留痕，可以在这条豁免处改为记一条告警而不改终态，留给后续决定。
- **把 `ECONNRESET`/`EPIPE` 也纳入豁免**：连接被重置时同样可能已经收全内容，但无法区分「收全后重置」与「中途重置」，先只认 EOF 家族。
- **在 SSE 解码器层把 `ErrUnexpectedEOF` 直接当 EOF**：会连带掩盖应答体中途被截断的传输问题，影响面超出本仓，且丢掉错误分类。

## 后果

- **收益**：终止块之后的传输截断不再导致客户端重试完整回答，思考-only 轮次改写出的 `tool_calls` 终态也能真正送达，客户端可以继续下一轮；服务端不再把这类轮次记为 `upstream_network` 失败。
- **代价**：极少数「`finish_reason` 之后还应有内容」的非常规上游会被当完整响应交付。多 choice 流以任一条 choice 的终止原因为整轮完成信号，与既有「干净 EOF 补帧」同一口径。
- **上限**：终止原因之前就断开的流仍判失败并静默截断，不补哨兵；上游若在生成中途断开，客户端行为与改动前一致。

## 验证

```sh
go test ./internal/relay/ -run 'TestForwardTruncationAfterFinishDeliversSentinel|TestForwardTruncationBeforeFinishStillFails' -v
go test ./internal/relay/ -run 'TestForwardStreamingNoAnswerStopSilentTruncation|TestForwardStreamingReasoningOnlyZeroOutput' -v
go test ./internal/relay/    # 全量回归
```

- 第一条用 `http.Hijacker` 手工写出分块 SSE 后不发结束分块直接断开，构造真实的 `unexpected EOF`：见到 `finish_reason` 后断开时补 `[DONE]` 并按成功定稿；未见到终止原因就断开时仍静默截断、按失败定稿。
- 第二条确认思考-only 的既有失败/改写语义不受本豁免影响。
