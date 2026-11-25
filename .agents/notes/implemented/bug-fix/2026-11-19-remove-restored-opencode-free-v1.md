# Agent Note: 彻底删除 OpenCode Free V1 渠道

Status: implemented

## 问题

2026-11-17 为 v2.3 客户端对齐恢复了一条 `OpenCode Free V1` 做旧客户端复现。用户现已明确不需要该渠道，要求删除。

更早的迁移 29 只覆盖升级当次，启动顺序是 `db.InitDB`（含迁移 29）后 `EnsureBuiltinChannels`；只要 `BuiltinFreeChannels` 仍保留 V1，迁移删除的行会在同一轮启动被种子重新补建。已记录迁移 29 成功的存量库也会因此永久残留 V1。

## 决定

- `internal/builtin/builtin.go` 的 `BuiltinFreeChannels` 删除 `OpenCode Free V1`，免费渠道 17 → 16；`OpenCode Free V2` 与两条 V2 备用保留。
- 新增迁移 32（`internal/db/migrate/032.go`），复用迁移 29 的 `deleteRetiredOpencodeFreeV1Channels`：按 `builtin = 1` 精确匹配四条 V1 渠道名，事务内级联清理模型、分组成员、评估排序、生效成员指针、密度与账号池；`builtin = 0` 的同名自建渠道保留。
- 旧名 `OpenCode Free Old` 由迁移 28 负责：目标存在则迁移账号后删除，目标缺失则直接删除账号、密度与渠道；新增迁移 33 清理历史遗留的无主账号池，迁移 34 清理无主密度。当前库已同步清理该行。
- 迁移 29 不改动，负责首次升级；迁移 32 负责迁移 29 已记录但被种子补回的存量库。
- 迁移 32 仍是一次性记录：若旧实例、回滚容器或恢复备份在记录成功后又把 V1 补回，最新版本不会重跑。`EnsureBuiltinChannels` 启动时因此每次调用 `migrate.DeleteRetiredOpencodeFreeV1Channels` 做幂等清理，复用迁移 29 的级联删除口径；只删 `builtin = 1` 的四条 V1 名称，同名自建渠道保留。
- 测试同步：`TestBuiltinFreeChannelsDefinitions` 计数 16；`TestBuiltinFreeChannelsOpencodeLaneMapping` 把 V1 列入 retired；`TestBuiltinOpencodeFreeRetiredChannels` 要求所有 OpenCode 兼容内置渠道必须是 v2；`TestEnsureFreeBuiltinChannelsRetiresReintroducedV1` 覆盖「迁移记录成功后被重新补回」的场景。

## 取代检查

- 部分取代 [对齐 opencode v2.3.0 客户端形态](2026-11-17-opencode-v23-client-alignment.md) 中「恢复一条 OpenCode Free V1」的决定；该篇 v2.3 头集、project 派生与迁移 30 等其余决定不变。
- 重新落实 [下线 OpenCode 1.x 免费档渠道](../feature/2026-11-17-retire-opencode-v1-channels.md) 的删除结论，并补上该篇未覆盖的「迁移后种子补建」缺口。
- `OpencodeLane="v1"` 的整形代码与模板保留，供自建渠道复现使用。

## 备选方案

- **只删数据库行**：能立刻让当前库干净；否掉是下一次启动会按种子重新补建，问题复发。
- **只删种子条目**：新库不再出现 V1；否掉是迁移 29 已记录的存量库不会再次执行删除，V1 行永久残留。
- **继续保留 V1 供旧客户端对照**：能复现 1.x 形态；否掉是用户已明确不要，且官方停售，保留只会占据渠道列表。
- **直接修改迁移 29 的名单或再执行一次 29**：迁移 29 已冻结，改写历史迁移会破坏已升级库的迁移记录语义。

## 后果

- 新库与升级库都不再出现 V1；升级库会删除 V1 的模型、分组成员与评估排序，历史用量仍按模型名保留。
- 仍需要 1.x 形态做实验的实例可自建渠道并把 `opencode_lane` 设为 `v1`，代码路径仍支持。
- 内置免费渠道总数变为 16，OpenCode 免费档收敛为 V2 主渠道与两条 V2 备用。
- 启动清理是幂等的：新库与无残留库每次启动零改动；旧库即使 V1 在迁移 32 之后被重新补回，也会在下次启动被删除。

## 验证

由用户执行（AGENTS 禁止 AI 运行 build/test/vet）：

```sh
go test ./internal/builtin -run TestBuiltin -v
go test ./internal/db/migrate -run 'TestDeleteRetiredOpencodeFreeLegacyChannelsWithoutTarget|TestDeleteRetiredOpencodeFreeOldWithoutV1|TestDeleteRetiredOpencodeFreeV1Channels|TestDeleteOrphanOpencodeProxyAccounts|TestDeleteOrphanTokenDensities' -v
pnpm verify-notes
```

启动后检查：

```sql
SELECT id, name, opencode_lane, enabled FROM channels WHERE builtin = 1 AND name LIKE 'OpenCode Free%';
```

期望只剩 `OpenCode Free V2` 与其两条备用，且没有 v1 lane 行。
