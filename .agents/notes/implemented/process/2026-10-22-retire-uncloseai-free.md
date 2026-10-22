# Agent Note: 下线 UncloseAI Free 内置渠道

Status: implemented

## 问题

UncloseAI 的公开测试已关闭，`https://hermes.ai.unturf.com/v1/models` 返回 401 与 `public_beta_closed`。该渠道仍在内置清单中，且存量库里的内置渠道默认开启 AutoSync；启动同步会持续向上游发请求并记录 WARN。仅从 `BuiltinFreeChannels` 删除定义不会处理存量行，因为 `EnsureBuiltinChannels` 只补建缺失渠道，不覆盖已有记录。

## 决定

- 从 `BuiltinFreeChannels` 删除 `UncloseAI Free` 定义，并同步文件头渠道清单。
- 迁移 `023` 只把 `builtin = true AND name = 'UncloseAI Free' AND enabled = true` 的存量行置为 `enabled = false`。不删除行，不改管理员自建的同名渠道；条件带当前状态，重复执行为零行更新。
- 模型同步跳过 `Enabled = false` 的渠道。停用渠道不再发起上游模型同步，管理员重新启用后恢复原有 AutoSync 行为。
- 内置渠道仍由 `op.ChannelDel` 保护不可删除，后台可继续看到该历史渠道并显示停用状态。

## 备选方案

- **只删内置定义**：改动最小，但存量行仍保持启用且 AutoSync 会继续请求已关闭的上游，无法消除启动噪声，未选。
- **迁移直接删除渠道行**：能立即消失，但丢失历史与管理员可见性，也绕过内置渠道只能停用的既有约束，未选。
- **迁移同时关闭 AutoSync**：也能阻止同步，但会把「停用」和「以后重新启用时是否继续自动同步」耦合在一起；同步任务按启用状态过滤更符合停用语义，未选。
- **迁移按名称更新所有同名渠道**：SQL 更短，但会误停管理员自建渠道，未选。

## 后果

- **收益**：新库不再生成该渠道；存量库升级后目标内置行立即停用，不再访问已关闭的上游，启动日志不再出现 `public_beta_closed` 警告。
- **代价与已知上限**：历史模型与渠道记录继续保留，后台仍可搜索到停用渠道；迁移只处理 `builtin = true` 的行，因此管理员自建同名渠道保持原状态。
- 重访信号：上游重新开放公测或替换端点时，需要重新评估是否恢复内置定义；恢复时应新增迁移或明确的重新启用路径，而不是改写本次下线迁移。

## 验证

- `go test -count=1 ./internal/db/migrate/` 覆盖目标行停用、非内置同名行不变、无匹配不报错，以及第二次执行零行更新。
- `go test -count=1 ./internal/task/` 覆盖停用渠道不触发模型同步。
- `go test -count=1 ./internal/builtin/` 的渠道清单断言确认数量为 17 且不含 `UncloseAI Free`。
