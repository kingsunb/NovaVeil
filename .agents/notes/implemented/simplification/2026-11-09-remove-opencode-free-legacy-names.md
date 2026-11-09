# Agent Note: 下线 OpenCode Free / OpenCode Free Old 旧名内置渠道

Status: implemented

## 问题

`BuiltinFreeChannels` 同时保留 `OpenCode Free V2` / `OpenCode Free V1` 与旧名兼容条目 `OpenCode Free` / `OpenCode Free Old`。两条旧名是 2026-11-02 为复用线上存量渠道与代理账号池而恢复的兼容入口；它们与 V2/V1 同端点、同 Key、同种子模型，造成渠道列表重复，AutoSync 对同一端点重复同步，源码需要维护两套相同定义。用户确认旧渠道可以下线，并要求保留 V2/V1、V1 备用1/2 与 V1 账号池。存量库里的旧渠道行和 `opencode_proxy_accounts` 台账仍需处理，否则源码删除后旧渠道会被启动补建或留下不可达账号。

## 决定

1. `internal/builtin/builtin.go` 的 `BuiltinFreeChannels` 删除 `OpenCode Free`（v2）与 `OpenCode Free Old`（v1）两条定义，条目数从 20 降到 18；保留 `OpenCode Free V2`、`OpenCode Free V1`、`OpenCode Free V1 账号池`、`OpenCode Free V1 备用1/备用2`。包文档中旧名兼容说明同步删除。`EnsureBuiltinChannels` 不再按旧名补建渠道。
2. 新增迁移 028 `deleteRetiredOpencodeFreeLegacyChannels`（`internal/db/migrate/028.go`，`RegisterAfterAutoMigration`），在事务内：
   - 只查 `builtin = true` 且名称命中旧名的渠道行；`builtin = false` 的同名自建渠道不动。
   - 按映射把 `OpenCode Free` 的 `opencode_proxy_accounts` 行迁到 `OpenCode Free V2`，把 `OpenCode Free Old` 的行迁到 `OpenCode Free V1`。复制时 `ID` 置 0、`channel_id` 改目标，使用 GORM `clause.OnConflict{DoNothing: true}` 保留目标已有同 `(channel_id, key_id, account)` 账号的统计与冷却状态；复制后删除源渠道的账号行。
   - 按迁移 024 与 `op.ChannelDel` 的口径级联：清引用旧渠道模型的 `Group.active_item_id`（置 0），删 `GroupItem`、`ModelEvalRank`，再删 `ChannelModel` 与 `Channel`。迁移可重复执行，第二次为空操作。
3. 旧渠道的评估与探测历史（`ModelEval`、`ModelEvalStats`、`ModelEvalQueueTask`、`ContextProbeRecord`）不在 028 的清理范围，沿用现有删除口径；这些行可能继续引用已删除的渠道 ID。

## 升级顺序与孤儿账号

`cmd/start.go` 在数据库初始化之后调用 `builtin.EnsureBuiltinChannels`，因此 028 执行时目标 V1/V2 行可能尚未补建。目标存在时按上述映射迁移账号；目标缺失时 028 仍删除旧渠道，但保留旧渠道名下的账号行，避免不可恢复的数据丢失。该迁移记录写入后不会重跑，目标随后由 `EnsureBuiltinChannels` 补建，这些账号不会自动再迁移，成为不可达孤儿。正常升级路径（存量库已有 V1/V2）不触发该边界；若线上出现孤儿账号，需要后续清理或一次性迁移。

## 取代检查

- 完全取代恢复 OpenCode Free / OpenCode Free Old 旧名内置渠道（2026-11-02）：该篇为复用线上旧渠道而恢复旧名，并明确“后续再考虑删除”；本篇删除旧名并把账号迁到 V1/V2，旧篇已归档，文末保留历史快照链接。
- 部分取代 [/zen 免费档关闭，免费档统一到 /inference 并显式区分客户端形态](../bug-fix/2026-10-26-opencode-free-zen-closed-inference-only.md)：该篇否掉“删除 OpenCode Free Old 并级联清理账号池”的理由是旧渠道挂约 2100 条账号；本篇先迁账号再删渠道，该否定条件消失。该篇的 `/inference` 端点与 `OpencodeLane` 结论仍有效，双方互链。
- 部分重叠 [OpenCode Free Old 内置渠道走 legacy /zen/v1 端点](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)：该篇新增的旧渠道已被本篇删除；其 `/zen` 端点结论已由 2026-10-26 笔记取代，保留为历史动机记录，双方互链。
- 沿用 [下线 OpenCode Free 2/3 内置副本渠道](./2026-10-24-remove-opencode-free-duplicates.md) 的迁移级联口径，不改变其决定。
- [Keyed 免费档渠道](../feature/2026-11-06-opencode-keyed-free-channel.md) 的账号池机制不变；该篇 Problem 中“旧名兼容渠道”是历史叙述，不回改。

## 备选方案

- **只删源码定义，不加迁移** — 改动最小；否掉：存量库旧渠道行仍在渠道列表和路由中，`EnsureBuiltinChannels` 也不会删除它们，用户要求的“删除项目里的旧渠道”没有完成。
- **把 V2/V1 改名回旧名，保留旧渠道 ID** — 可让账号池继续挂原 ID，省掉迁移；否掉：用户要求保留 V2/V1 的现有命名与配置，改名会重写已有渠道身份，且 2026-11-02 笔记已确认先保留 V2/V1。
- **删除旧渠道时不迁移账号，级联清理或保留孤儿** — 实现最简单；否掉：旧渠道名下可能挂着生产代理账号（2026-10-26 笔记记录约 2100 条），直接丢弃或留成不可达行都不可接受。
- **迁移时覆盖目标已有同账号行** — 可让旧渠道的统计覆盖目标；否掉：目标行可能已有更新的成功与冷却状态，覆盖会回退数据；`DoNothing` 保留目标行。
- **在 028 里调用 `builtin.EnsureBuiltinChannels` 补建目标** — 可消除目标缺失的孤儿边界；否掉：迁移包不能反向导入 `builtin`（`builtin` 依赖 `db`，`db` 依赖 `migrate`），且补建职责属于启动后的 `EnsureBuiltinChannels`。
- **只迁移 `key_id` 为空的 public 账号** — 旧渠道本身是 public，似乎够用；否掉：旧渠道历史上可能挂过 keyed 台账，按 `channel_id` 全量复制更稳，唯一索引负责去重。
- **同时清理 `ModelEval` / `ModelEvalStats` / `ModelEvalQueueTask` / `ContextProbeRecord`** — 可避免历史行引用已删除渠道；否掉：现有 `ChannelDel` 与迁移 024 都不清理这些表，扩大删除面会改变既有口径，本次只复用 024 的级联范围。

## 后果

- **收益**：源码与渠道列表只剩 V2/V1 与备用/账号池，旧名不再被启动补建；旧渠道的代理账号迁到对应保留渠道后继续参与账号池与体检；目标已有账号的统计不被旧数据覆盖。
- **代价与已知上限**：028 不可逆删除旧渠道行、模型与分组成员关系，以及旧渠道的生效成员指针；目标缺失时账号成为不可达孤儿且不会自动重迁移；`ModelEval`、`ModelEvalStats`、`ModelEvalQueueTask`、`ContextProbeRecord` 可能保留指向已删除渠道 ID 的历史行；`builtin = false` 的同名自建渠道不受影响。重访信号：线上出现旧渠道账号未迁移、`opencode_proxy_accounts` 出现指向已删除渠道的孤儿行，或需要清理评估与探测历史时。

## 验证

```sh
go test ./internal/builtin/ -run 'TestBuiltinFreeChannelsDefinitions|TestBuiltinFreeChannelsOpencodeLaneMapping|TestOpenCodeFreeSeedProtocolsAreVerified' -v
go test ./internal/db/migrate/ -run 'TestDeleteRetiredOpencodeFreeLegacyChannels' -v
pnpm verify-notes
```

- `TestBuiltinFreeChannelsDefinitions` 校验内置清单为 18 条，`TestBuiltinFreeChannelsOpencodeLaneMapping` 校验旧名未注册且 V2/V1 的 lane 与端点不变。
- `TestDeleteRetiredOpencodeFreeLegacyChannels` 覆盖旧渠道级联、账号迁移、目标重复账号保留、重复执行幂等；无目标用例覆盖孤儿账号边界。
- 线上复核：

```sh
sqlite3 data.db "select id,name,builtin from channels where name in ('OpenCode Free','OpenCode Free Old')"
sqlite3 data.db "select channel_id,key_id,account from opencode_proxy_accounts order by channel_id,key_id,account"
```

第一条应无 `builtin=1` 的旧名行；第二条应显示旧账号已落在 V2/V1 的渠道 ID 上（目标缺失时仍显示旧渠道 ID）。

## 相关

- [/zen 免费档关闭，免费档统一到 /inference 并显式区分客户端形态](../bug-fix/2026-10-26-opencode-free-zen-closed-inference-only.md)
- [OpenCode Free Old 内置渠道走 legacy /zen/v1 端点](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)
- [下线 OpenCode Free 2/3 内置副本渠道](./2026-10-24-remove-opencode-free-duplicates.md)
- [Keyed 免费档渠道](../feature/2026-11-06-opencode-keyed-free-channel.md)

[历史快照：恢复 OpenCode Free / OpenCode Free Old 旧名内置渠道](../../archived/bug-fix/2026-11-02-restore-opencode-free-builtin-names.md)
