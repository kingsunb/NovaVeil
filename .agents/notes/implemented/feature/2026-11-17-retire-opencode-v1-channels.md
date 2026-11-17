# Agent Note: 下线 OpenCode 1.x 免费档渠道

Status: implemented

## 问题

1.9.5 是 opencode 1.x 的最后一个版本，官方已不再为 1.x 客户端提供免费模型。Nova 此前出厂四条 v1 渠道：`OpenCode Free V1`、`OpenCode Free V1 账号池`（keyed）、`OpenCode Free V1 备用1`、`OpenCode Free V1 备用2`。它们打同一个 `/inference` 端点、只靠 `opencode_lane="v1"` 区分请求形态，官方停售后这些渠道只会稳定失败。

同时 v2 只有一条主渠道，缺少 v1 原先的备用副本，实例无法把主渠道与备用渠道分别挂到不同出口做故障隔离。

## 决定

- `BuiltinFreeChannels` 删除全部四条 v1 渠道；新增 `OpenCode Free V2 备用1`、`OpenCode Free V2 备用2`，与 `OpenCode Free V2` 同构（同端点、public Key、`opencode_lane="v2"`、同一份种子模型），供实例单独挂 resin/WARP 代理。
- 不提供 v2 的 keyed 账号池。keyed 形态与 1.x 免费档的配额模型绑定，v2 没有对应需求；管理员需要多凭据时仍可自建渠道。
- 内置渠道只补建不删除，因此新增迁移 29：按 `builtin = 1` 精确匹配四条 v1 渠道名，级联清理模型、分组成员、评估排序与生效成员指针，避免老库升级后残留不可用渠道。
- `OpencodeLane="v1"` 的整形代码（v1 模板、v1 UA、v1 键序）保留不动：它由渠道字段驱动，没有渠道引用后即死路径，删除属于另一件事。

## 备选方案

- 保留 v1 渠道但默认停用：仍会出现在渠道列表里诱导误配，且 v1 已无可用模型，没有保留价值。
- 把 v1 账号池的 Key 迁到 v2 主渠道：主渠道是 public 免费形态，混入真实 Key 会改变其鉴权语义；用户明确不要账号渠道。
- 迁移里静默保留 v1 渠道行的 Key 与代理账号：渠道删除后这些行失去归属，成为孤儿数据，不如随渠道一并清理并提前告知运维导出。

## 后果

- 新库出厂只有 V2 / V2 备用1 / V2 备用2 三条 OpenCode 免费渠道，总免费渠道数由 18 降为 16。
- 老库升级时四条 v1 渠道被删除，其模型与分组成员一并移除；挂在 `OpenCode Free V1 账号池` 上的 Key 与代理账号台账不再可用，需要在升级前自行导出。
- 若某实例仍需要 1.x 形态做对照实验，可自建渠道并把 `opencode_lane` 设为 v1，代码路径仍支持。
- 备用渠道与主渠道同名同模型，分组里会同时出现三条候选；默认启用，因此不做人工配置也会参与轮询——这与 v1 备用渠道的历史行为一致。

## 验证

由用户执行（AGENTS 禁止 AI 运行 build/test/vet）：

```sh
go test ./internal/builtin ./internal/db/migrate -run 'TestBuiltin|TestDeleteRetiredOpencodeFreeV1' -v
go test ./...
```

启动后检查：

```sql
SELECT id, name, opencode_lane, enabled FROM channels WHERE builtin = 1 AND name LIKE 'OpenCode Free%';
```

期望只剩 `OpenCode Free V2` 与其两条备用，且没有 v1 lane 行。
