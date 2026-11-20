# Agent Note: 流式 SSE 还原按通道独立缓冲，中途不返回空字节

Status: implemented

## 问题

模型流式返回（`text/event-stream`）时占位符可能被拆到多个 SSE event 中（`{{CONNSTR_zk` + `pmqx}}`），必须缓冲拼接后才能完整匹配还原。难点有四个：正文 delta 与 tool 参数 delta 共用一个缓冲会把上一字段尾巴吐进下一字段；并行工具调用时每 chunk 只带一个元素；中途返回空字节会被 chunked 语法当成终止块；终止事件前各通道滞留的半截占位符必须吐净否则吞掉最后几个字。这是脱敏功能最难的部分，总决策见 [网关层脱敏](../feature/2026-09-10-gateway-native-request-masking.md)。

## 决定

`internal/relay/mask/stream_restore.go` 增量还原器：

1. **每个增量文本槽位用独立通道键缓冲**（`DefaultChannel` / `ReasoningChannel` / `ToolChannelPrefix + "<index>"`），半截占位符只在本通道缓冲，不跨字段串；`Push`/`Flush` 委托给 `PushChannel(DefaultChannel, …)`/`FlushChannel(DefaultChannel)`，`FlushChannels` 返回按通道名索引的残留。
2. **并行工具用 `tool_calls[].index` 标识**（协议标明增量属于第几个工具），数组位置只用于改写当前帧的 JSON 字段。索引必须能精确解码为非负 Go `int`；缺失、负数、溢出以及小数或指数表示（包括 `1.0` / `1e0`）直接报错，不分配默认槽位。补发工具参数使用单元素 `tool_calls` 数组并显式携带原索引，避免按索引构造稀疏数组。
3. **单字段全部待拼接时只清空该字段**，仍处理同帧的其他正文、推理及工具字段并保留事件元数据；跨通道处理不会因某字段暂无输出而提前退出。
4. **终止事件前吐净各通道滞留缓冲**，补发用最后一个同通道事件做模板（克隆真实事件保证客户端 SDK 字段校验通过），补发事件不带 `finish_reason`。
5. 一个 SSE 事件到手立刻还原、立刻下发，首字延迟不等于整段生成时长。
6. `restoreStreamEvent` 把索引及 JSON 改写错误返回转发器。转发器记录首个错误、抑制当前坏帧并结束流，不补发待拼接残留或成功终止帧。

## 实现边界

第 4 条的模板克隆与终止前补发尚未落地：`handler.go` 的现有补发仍位于事件循环之后，`mask_integration.go` 构造最小增量事件，未缓存最后的真实事件模板。因此这次索引修复只保证补发工具的身份与数组结构，不能作为所有 SDK 的收尾兼容性证明。正文和工具都只读取 `choices[0]`；多 choice（即：一次请求返回多个候选回答）不在当前还原契约内。

## 备选方案

- **单一全局缓冲拼接** — 最强论据是实现最简；否掉因为正文 delta 与 tool 参数 delta 共用缓冲，实测会把上一个字段的尾巴吐进下一个字段，表现为字段被清空、内容错位。
- **按数组下标标识工具槽位** — 最强论据是直接用 chunk 里 tool_calls 的数组位置；否掉因为并行工具调用时每个 chunk 通常只带一个元素，index=0 和 index=1 的增量都拿到下标 0，两个工具的跨包缓冲直接串在一起。
- **攒到流末一次性还原** — 最强论据是逻辑最简、无跨 chunk 状态；否掉因为首字延迟等于整段生成时长，流式失去意义。
- **中途块返回空字节占位** — 最强论据是保持 chunked 连续性；否掉因为 `b""` 按 chunked 语法写出 `0\r\n\r\n` 即终止块，客户端据此判定响应结束、停止读取并关连接。

## 后果

- **收益**：逐事件还原、字段不串尾，单元素交错工具帧按协议索引隔离，非法工具身份不会被默认为 0 后继续下发。
- **代价与已知上限**：每通道独立缓冲的状态机复杂度上升，跨 chunk 拆分/并行工具/终止前吐净需单测守死。重访信号：上游新增 SSE 事件结构（如新增量字段类型）需补对应通道键。

## 验证

流式还原在 `internal/relay/mask/stream_restore.go` + `*_test.go`；接入 `internal/relay/handler.go` 流式分支。设计溯源见 [docs/脱敏开发/03-流式还原设计.md](../../../../docs/脱敏开发/03-流式还原设计.md)。

`internal/relay/mask_integration_test.go` 包含 `0/1` 与 `3/9` 工具索引交错分片、同帧多通道待拼接、非法索引、非零索引补发及转发失败收尾的回归源码。按仓库执行口径，本轮未运行编译与测试；由用户执行 `go test ./internal/relay/... -run 'Test(RestoreStreamEvent|FlushStreamRestorer|ForwardMaskedStream)'`。
