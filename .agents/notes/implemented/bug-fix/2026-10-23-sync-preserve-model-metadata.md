# Agent Note: 同步目录缺失时保留模型能力元数据

Status: implemented

## 问题

OpenCode 渠道的自动同步会每轮重建 auto 模型。旧实现只从上一轮抄回 `UpstreamProtocol`，其余能力字段全部从零值开始，再尝试用目录覆盖。目录拉取失败或某个已同步模型暂时不在目录中时，`ContextWindow`、`MaxOutput`、`Reasoning`、`ToolCall`、`InputModalities`、`MaxReasoningEffort` 会被写成零值，管理台随之显示“窗口未知 / 不支持工具 / 不支持推理”，管理员容易误判并手工补录。

`max_reasoning_effort` 被清零还会破坏已有的 `thinking_level = "auto"`：展开结果为空串，思考参数注入退化成不注入，客户端选择的高档位可能被上游拒绝。目录元数据的最初落盘与展示见 [OpenCode 目录模型元数据展示](../feature/2026-10-16-opencode-catalog-model-metadata-display.md)，自动等级的注入语义见 [从目录提取最大思考等级](../feature/2026-10-20-max-reasoning-effort-from-catalog.md)。

产品口径是：目录不可用或模型不在目录里时，逐字段保留上一轮的非空元数据；只有目录明确提供新值时才覆盖。

## 决定

- `internal/task/sync.go` 重建 auto 模型时，先从同名旧模型整体继承 `UpstreamProtocol` 与六个能力字段，再处理本轮的 `fetchModels` 列表。
- 只有 `catalog[modelName]` 条目存在才进入覆盖路径：协议、上下文窗口、输出上限、输入模态、最大思考等级分别要求非空或大于零；`Reasoning` 与 `ToolCall` 则以目录值为准，目录明确声明的 `false` 也会覆盖旧值。
- 目录拉取失败、目录中没有该模型，或目录字段为空/零值时保留继承值；首次出现且无旧值的模型仍从零值开始，避免为未知能力编造默认值。
- 重建后的对象在目录失败且模型集合未变时与旧值完全一致，既有 `protocolDirty`、`metadataDirty` 检测不会触发 `op.ChannelUpdate`，因此不产生多余 DB 写入。
- 同步仍只重建 `ChannelModelSourceAuto` 行；手动模型和管理员维护的 `model_limits` 不进入这条重建路径。

## 备选方案

- **目录失败时跳过整个渠道同步**：可以完全避开元数据变化，但模型列表的新增、删除也会被冻结，一次目录抖动会无限推迟模型清单同步；因此只收窄为能力字段不覆盖。
- **目录字段为零值也照常覆盖**：实现最直接，但目录中的 `0`、`false`、空串表示“未提供”或“未知”的情况会被当成撤回收能力，仍会重现本次数据丢失；因此数值、模态与思考等级只在明确非空时覆盖。
- **仅在目录请求失败时保留，漏项仍清零**：能覆盖网络抖动，但目录成功返回局部内容或单模型被上游暂时遗漏时仍会清空该模型；因此失败与漏项统一沿用旧值。

## 后果

- **收益**：目录短暂不可用或漏项不再抹掉上下文窗口、输出上限、推理/工具调用能力、输入模态与思考等级；`thinking_level = "auto"` 不会因一次同步抖动失效，也不会产生同值更新。
- **代价与已知上限**：某模型在目录中持续缺项时，旧能力会一直保留，直到目录重新给出明确值；布尔能力可由目录条目中的 `false` 显式撤回，但数值型与模态型字段没有显式清空语义。手动模型与手动限制不参与同步，这是有意边界。

## 验证

- `TestSyncCatalogFailureKeepsModelMetadata` 覆盖目录报错时协议与六个字段全部保留，并用 GORM 更新回调断言 `channel_models` 更新次数为 0。
- `TestSyncCatalogMissingEntryKeepsModelMetadata` 覆盖目录成功但只含另一个模型：旧模型保留，目录中的新模型写入新值。
- `TestSyncCatalogUpdatesModelMetadata` 覆盖目录给出新协议、窗口、输出、模态与思考等级时正常覆盖，同时验证目录声明的 `false` 能覆盖旧能力。
- 可在仓库根目录运行 `go test ./internal/task -run 'TestSyncCatalog' -count=1`；笔记门禁使用 `npx -y tsx scripts/agent-notes/verify-agent-note-tree.ts` 与 `npx -y tsx scripts/agent-notes/verify-agent-note-format.ts`。
