# Agent Note: 移除免费渠道模型评估确定性失败自动删除

Status: implemented

## 问题

2026-09-23 引入的「免费渠道模型评估确定性失败(401/403/404)累计 3 次自动删除」(见
[原实现笔记](../../archived/feature/2026-09-23-free-channel-model-eval-fatal-auto-delete.md)) 会在上游对某个仍健康的
免费模型稳定误返 4xx、或免费模型短暂转收费后又恢复时，把管理员仍想保留的模型从列表里永久删掉；删除是持久的、
`EnsureBuiltinChannels` 只补缺失渠道不重建被删模型，只能靠管理员手工重新添加。这种「凭三次观测就替管理员做删除
决定」的自动清理被要求移除，回到免费渠道模型由管理员手工管理删除。

## 决定

整体移除该功能，评估失败路径不再做任何自动累计/删除：

- 删 `internal/op/channel_model_fatal.go`（阈值常量 `ChannelModelFatalEvalLimit` + 累计/达阈值级联删除函数
  `ChannelModelRecordFatalEvalFailure`）及其测试。
- 删 `internal/eval/fatal.go`（`isFatalEvalStatus` 401/403/404 判定）及其测试。
- `internal/eval/scheduler.go` 的 `executeTask` 移除 cleanup 调用与 `modelDeleted` 守卫，评估失败后的
  `ModelEvalPruneTarget` + `ModelEvalRankUpsert` 恢复无条件执行（与评估成功路径一致）。
- `internal/model/channel.go` 删 `EvalFatalFailures` 字段；迁移 018 drop `channel_models.eval_fatal_failures` 孤儿列。
- `docs/modules/eval.md` 删两处「确定性失败自动清理」事实描述。

失败模型此后留在列表里，由管理员在渠道编辑器手工删除；评估历史照常落库留痕。

## 备选方案

- **加管理开关保留能力** — 默认关/按渠道可配是否自动清理。否掉因为要新增配置面与校验，而诉求是移除行为本身，
  不是要一份可选自动清理。
- **从硬删除改成软禁用/标记失效** — 累计后不物理删、只标记 disabled。否掉因为要引入状态位与前端展示，超出本次
  移除范围，且仍保留「自动」语义。
- **只停用 scheduler 调用、留死代码** — 否掉因为留 `EvalFatalFailures` 字段与未调用函数，违背「删行为就去干净」，
  还会留下孤儿列与死测试。

## 后果

- **收益**：免费模型不再被自动删除，列表稳定性交还管理员掌控；`eval_fatal_failures` 列移除，schema 更简单。
- **代价**：失效免费模型不再自动消失，会继续留在列表并被自动分组/评估反复选中报错，需管理员手工删除——这是
  移除该行为后的预期负担。
- **数据**：迁移 018 drop 列，旧库该列的累计值随列一起丢弃（功能已移除，无保留意义）。
- 原 note 归档为历史快照，不再作为当前行为权威。

## 验证

- `go build ./...`、`go vet ./...`、`go test ./internal/eval/ ./internal/op/ ./internal/db/migrate/`。
- `pnpm verify-notes`（新 note 格式 + 归档封印）。