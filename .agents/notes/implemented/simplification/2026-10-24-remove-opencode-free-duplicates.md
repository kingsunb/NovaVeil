# Agent Note: 下线 OpenCode Free 2/3 内置副本渠道

Status: implemented

## Problem

[多副本供分代理转发](../feature/2026-10-15-opencode-free-dual-channel-for-proxy.md) 在内置清单里加了 `"OpenCode Free 2"` / `"OpenCode Free 3"`，与 `"OpenCode Free"` 三份同配置（同 BaseURL、Key、OpencodeCompat、CustomHeader、Models），仅 Name 不同。其记录的代价随之坐实：上游模型列表或请求头指纹变更需同步三处，渠道列表出现三个同端点条目，管理员难以分辨三者差异。维护者决定收回多副本方案，内置 OpenCode 免费档只保留两个职责清晰的渠道：`/inference` 档主渠道与 legacy `/zen` 档渠道。

## Decision

- `BuiltinFreeChannels`（`internal/builtin/builtin.go`）只保留 `"OpenCode Free"` 与 `"OpenCode Free Old"`，删除两个副本条目，清单总数 17 → 15（`TestBuiltinFreeChannelsDefinitions` 计数断言同步修改）。
- 新增迁移 024 `deleteRetiredOpencodeFreeDuplicates`（`internal/db/migrate/024.go`，注册在 `RegisterAfterAutoMigration`）：事务内删除 `builtin = true AND name IN ("OpenCode Free 2","OpenCode Free 3")` 的渠道行，并按 `op.ChannelDel` 的口径级联清理派生数据——先清引用其模型的 `Group.active_item_id` 置 0，再删 `GroupItem` 行与 `ModelEvalRank` 行，最后删 `ChannelModel` 与 `Channel` 行。`builtin = false` 的同名自定义渠道不受影响。
- 执行顺序保证删除不被补建：`cmd/start.go` 先 `db.InitDB` 跑完迁移 024，后调 `EnsureBuiltinChannels`；清单里已无这两个条目，被删的内置行不会在启动时重建。

## 取代检查

- 取代 [多副本供分代理转发](../feature/2026-10-15-opencode-free-dual-channel-for-proxy.md) 的加副本决定，旧篇保留原始动机与备选分析并互链；其中「自定义渠道每次启动被清 `is_free` 与 `opencode_compat`」的清理不变量仍生效，仍是自定义渠道无法承担 OpenCode 免费转发的根因。
- 正交 [OpenCode Free Old legacy zen 渠道](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)：本篇不动 `"OpenCode Free Old"`；该篇正文中提及 Free 2/3 的段落是加渠道当时的事实记录，不回改。
- 全库检索 `Free 2` / `Free 3` 仅命中上述两篇笔记与本篇；代码侧仅 `builtin.go` 清单与迁移 024，前端与 relay 层无按名引用。

## Alternatives considered

- **只删清单条目、不写迁移** — 最强论据是零删除风险，存量行静默保留；否掉是因为 `EnsureBuiltinChannels` 不再管理这两个渠道，管理员列表会永久残留两个来源不明、无法解释的同端点内置渠道，代码事实与库内事实脱节，违背「内置渠道清单是单一事实源」的设计。
- **迁移只停用不删行（对齐 023 UncloseAI 先例）** — 最强论据是保留渠道行的历史关联可读性且删除更保守；否掉是因为 Free 2/3 与主渠道完全同配置，停用行没有独立信息量，只会长期占据渠道列表、迫使管理员手动删除；用量等历史数据按 `channel_id` 关联，删行后仍可留存，与 `ChannelDel` 对普通渠道的行为一致。
- **保留副本但抽公共定义消除重复** — 最强论据是保留多代理能力的同时把三处收敛为一处；否掉是因为维护者已决定不需要多副本，保留两个死条目只为消重复是负资产，直接删除更符合 YAGNI。

## Consequences

- **收益**：OpenCode 免费档内置渠道收敛为 `/inference` 主渠道 + legacy `/zen` 渠道两条，上游模型列表或指纹变更只需同步两处；渠道列表不再有三个同端点条目。
- **代价与已知上限**：升级即不可逆删除存量库中 Free 2/3 行及其分组成员关系、评估排序与生效成员指针；曾绑定在副本上的独立代理配置随之消失，且「同一免费端点多代理出口」能力随副本一起下线（自定义渠道受清理不变量约束无法替代）。重访信号：管理员重新需要 OpenCode Free 分代理出口时，重新评估按 Key/渠道级代理模板或放宽清理不变量。

## Verification

- 迁移行为有契约测试 `internal/db/migrate/024_test.go`：断言 builtin Free 2/3 行、其 ChannelModel/GroupItem/ModelEvalRank 连带删除、`active_item_id` 清零、`builtin = false` 同名渠道保留、重复执行幂等。
- 渠道清单由 `internal/builtin/builtin_test.go` 的 `TestBuiltinFreeChannelsDefinitions` 计数 15 与 Name 唯一性守护。
- 用户验证命令：`go test ./internal/db/migrate/ -run TestDeleteRetiredOpencodeFreeDuplicates -v`、`go test ./internal/builtin/ -v`。
