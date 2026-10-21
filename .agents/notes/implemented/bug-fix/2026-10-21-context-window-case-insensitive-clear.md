# Agent Note: 上下文窗口清理按模型名大小写不敏感

Status: implemented

## 问题

`setModelContextWindow` 原先只读写 `modelName` 对应的精确 map 键（`internal/server/handlers/model_eval.go:122` 一带），而运行时 `lookupModelLimit` 先精确匹配、再按 `strings.EqualFold` 匹配（`internal/relay/channel.go:215-228`），`effectiveContextWindow` 直接复用该结果（`internal/relay/channel.go:244-249`）。因此配置键为 `GPT-4o` 时，用 `gpt-4o` 清除不会删掉旧键；写入则可能新增第二个仅大小写不同的键。旧实现产生的重复键还会让后续清除留下另一个变体的窗口。本笔记补齐[上下文窗口手工定义与统一实测密度校准](../feature/2026-10-21-context-window-manual-limits-and-uniform-density.md)的清除语义。

## 决定

- `setModelContextWindow` 收集目标 map 中所有与传入模型名 `strings.EqualFold` 相等的现有键；没有匹配时才使用传入名称新建。
- 写入与清除都作用于这些现有键，不再产生新的重复大小写键。
- 清除时逐个把 `ContextWindow` 置空；仅当条目同时没有 `MaxOutput` 与 `ThinkingLevel` 时才删除，其它模型键原样保留。
- 运行时查找契约不变：仍为精确优先、再忽略大小写；缺口只在写侧。

## 备选方案

- **只匹配第一个大小写变体**：代码更少，但旧缺陷已可能落盘重复键，清除一个变体后运行时仍会命中另一个，无法保证实际清除；不选。
- **把所有配置键统一迁移为小写**：键规范彻底统一，但会改变持久化数据与展示键，且需处理同名不同限制的合并策略；超出本次最小修复；不选。
- **修改运行时查找为只忽略大小写、不精确优先**：不能解决写入重复键，且改动了已验证的读取契约；不选。

## 后果

- **收益**：`GPT-4o` 配置可被 `gpt-4o` 正确清除或更新；历史重复键的窗口也能一并清理，其它限制与无关模型不受影响。
- **代价与已知上限**：若历史重复键各自的 `MaxOutput` 或 `ThinkingLevel` 已冲突，清理仍保留各自条目，不在本次自动合并；当出现需要唯一键的展示或迁移需求时再处理。

## 验证

- `go test ./internal/server/handlers/ -run 'TestSetModelContextWindow'`
- 静态检查：`setModelContextWindow` 使用 `strings.EqualFold` 收集现有键，`internal/relay/channel.go` 的 `lookupModelLimit` 未改动。
