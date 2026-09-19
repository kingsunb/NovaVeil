# Agent Note: 模型评估「更新 auto 分组」以容错分类替代整体中止

Status: implemented

## 问题

「更新 auto 分组」（`op.GroupReplaceItemsByName`）原实现对排序条目逐条校验渠道启用与模型存在，任一条不可用即整体中止并返回 `ErrGroupReplaceTargetMissing`，面板报 400。三条真实场景被它挡死：

1. 渠道被停用但其模型仍可用且曾被评估有效——排序里这条记录永远无法入组，只能重新启用渠道或手工改分组；
2. 渠道模型删除/改名后 `model_eval_ranks` 残留失效记录——只要它还在排序里，整个「更新分组」动作持续失败，必须先手工清理；
3. 多条排序混合「正常 + 停用 + 失效」时，一条失效拖死全部有效成员。

原设计把「不产生半空分组」的事务原子性强化成了「任一不可用即中止」，把数据一致性代价转嫁成了功能不可用。docs 的 REQ-010 需求第 4 条已就地改写为容错语义（事实同步）。

## 决定

改为逐条容错分类、事务内原子写入（`internal/op/group.go`）：

- **渠道停用但模型仍在**：保留入组且 priority 保持原排名，计入响应 `kept_disabled`；
- **模型删除/改名**（缓存未命中）与**渠道整体已删**（防御分支）：计入 `cleaned_stale`，其 `model_eval_ranks` 行在同事务内按主键删除（`deleteEvalRanksByIDs`），不中止、不入组；
- **全部失效**：仅清理失效排序并返回 `ErrGroupReplaceNoRankable`（400），不产生空分组；
- 响应新增 `kept_disabled` / `cleaned_stale`（`GroupReplaceReport`，仅含渠道 ID/名称与模型名，不含任何凭据字段）；
- 删失效排序与「清旧成员 + 写新成员」同一事务，回滚时不留半程状态；
- `ErrGroupReplaceTargetMissing` 随之失去产生方，删除该 sentinel 与 handler 的对应分支。

**事务内按库复核**：分类读的是渠道模型缓存，删除事务提交晚于缓存失效时命中结果可能已过期，直接落库会留下悬空分组成员。路由侧虽把悬空成员按「暂不可用」等待重扫（`relay/handler.go` 的 nil ChannelModel 分支），但候选扫描不会跳过它（`channelDisabledForRouting` 对 nil 返回 false），会以 `MemberRetryIntervalSeconds`（默认 2s）间隔被反复选中、`exclude` 被重置导致无法排除，直到路由轮次/时长耗尽，饿死组内健康成员——不应依赖该兜底。故在替换分组的写事务内对命中项按库 `Pluck` 复核 `channel_models` 存在性，miss 者降级清理：SQLite 写事务全库串行，复核之后到提交之前不会再有删除事务提交，窗口闭合；MySQL/PG 下窗口亦缩至近零。渠道删除与其模型删除同事务级联，复核模型存在性即可覆盖渠道删除。降级成员不再计入 `kept_disabled`，避免同一记录在两类明细中重复出现。

## 备选方案

- **保留整体中止，仅细化报错文案** — 最强论据是改动最小、错误语义显式；否掉因为用户仍需手工清理残留排序才能用，停用渠道场景依旧无解，一致性代价只是从报错转嫁为不可用。
- **静默跳过失效条目、不清理排序** — 最强论据是实现最简单；否掉因为失效排序会永久残留并在每次更新时反复出现，违背「同一渠道+模型仅保留最新结果」的排序语义，且用户得不到任何反馈。
- **删除路径（`ChannelDel`/模型同步）加 `modelEvalRankMu` 与分类串行** — 最强论据是锁内分类+写入完全串行、同样闭合竞态；否掉因为这是「未来每个删模型的路径都必须记得拿锁」的脆弱隐式约定，且管不住手工编辑分组等其他悬空成员来源；事务内按库复核不依赖调用方纪律。
- **复核放在锁外/事务外** — 最强论据是少一层嵌套；否掉因为复核结果与写入之间仍可插入删除事务，窗口不闭合，等于没修。

## 后果

- **收益**：停用渠道的模型可入组、失效排序自动清理、混合场景不再被单条拖死；「更新 auto 分组」从「任一不可用即 400」变为「能入多少入多少 + 明细反馈」；悬空分组成员竞态在写入侧闭合，不再依赖路由侧的等待重扫兜底。
- **代价与已知上限**：停用渠道的模型入组后不参与选路（路由侧按停用渠道跳过成员），占优先级位但不出力，管理员需依据 `kept_disabled` 自行判断；渠道停用发生在分类之后时（复核只查模型存在性），`kept_disabled` 明细可能漏报该条——成员照常入组且路由按停用跳过，属反馈精度损失而非行为错误。重访信号：出现「停用渠道的模型需保持分组优先级并参与选路」的真实需求时，重议「停用保留」语义。

## 验证

`internal/op/group_replace_test.go`：`TestGroupReplaceItemsByNameDisabledChannel`（停用保留、priority 不变）、`TestGroupReplaceItemsByNameCleansStaleModel`（失效清理）、`TestGroupReplaceItemsByNameMixedSkipsNothing`（混合三态）、`TestGroupReplaceItemsByNameAllStaleOnlyCleans`（全失效仅清理不产空分组）、`TestGroupReplaceItemsByNameRollbackAtomic`（事务回滚原子性）、`TestGroupReplaceItemsByNameRevalidatesAllStaleInTx` / `TestGroupReplaceItemsByNameRevalidatesMixedInTx`（事务内按库复核降级：绕过缓存直接删 `channel_models` 行模拟删除事务晚于缓存失效提交）。`internal/op/channel_eval_rank_cleanup_test.go`：`deleteEvalRanksByIDs` 幂等与精准删除。`internal/server/handlers/model_eval_rank_test.go`：响应含 `kept_disabled` / `cleaned_stale`、空数组序列化为 `[]` 而非 `null`、400/500 分支映射。
