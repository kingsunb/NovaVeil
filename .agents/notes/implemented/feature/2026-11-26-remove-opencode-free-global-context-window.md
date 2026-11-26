# Agent Note: 移除 OpenCode 免费档全局上下文窗口设置项

Status: implemented

## 问题

此前为免费档（`isOpencodeFreeLane`）引入了全局上下文窗口设置项 `opencode_free_context_window`，在渠道手动窗口缺失时兜底。这使窗口出现两个来源（渠道手动值 + 全局隐式值），免费档窗口会隐式生效、行为不易解释，也多了全局配置与默认种子/校验分支的维护面。现决定下线该配置项，窗口只保留渠道手动值。

## 决定

- 删除常量 `SettingKeyOpencodeFreeContextWindow`、`OpencodeFreeContextWindowMin/Max`、`DefaultSettings` 里的种子行与 `Validate` 中的校验分支。
- `effectiveContextWindow` 只认渠道手动 `ModelLimits[name].ContextWindow`；删除免费档全局回退分支与 `opencodeFreeContextWindow` 读取函数。
- 前端设置页删除「全局 OpenCode 免费档上下文窗口」输入框、保存逻辑与对应 state。
- 迁移 35 删除旧库可能已存在的 `opencode_free_context_window` 设置行。
- 保留上下文预估日志可见性（`estimated_tokens`、每次尝试的窗口/密度等）不变。

## 备选方案

- **保留设置但默认 0**：等于现状，但仍是一个隐式配置面与维护点，未选。
- **保留 UI 仅在开启时生效**：同上，未选。
- **连同上下文预估日志一起删**：用户只要求删窗口，日志展示仍需保留，未选。
- **不清理旧库行**：旧库残留一行不会被读取的死配置，未选。

## 后果

- **收益**：窗口来源只剩渠道手动值，行为可解释；少一个全局隐式生效面与前端配置项。
- **代价与已知上限**：免费档要过滤超长请求需逐渠道填手动窗口；未填等于窗口未知放行（与全局窗口引入前一致），可能依赖上游 400 兜底。迁移 35 会删除旧库该设置行。

## 验证

- `go test ./internal/relay/ -run 'ContextFit|EffectiveContextWindow|ObserveRoundContext|FreeLane' -count=1` 通过。
- `go test ./internal/db/migrate/ -run TestDeleteRetiredOpencodeFreeContextWindowSetting -count=1` 通过。
- `go vet ./internal/relay ./internal/model ./internal/db/migrate` 通过。
- `cd web-next && pnpm typecheck && pnpm test src/pages/Settings.test.tsx` 通过。
- 全仓检索仅迁移 `035` 保留 `opencode_free_context_window` 字面量，其余代码/前端无残留。
