# Agent Note: 供应链安全扫描、Dependabot 与 CODEOWNERS

Status: implemented

## 问题

2026-09-20 后续审计 DevOps 域指出：

- **DEV-02**：自动 CI 没有 `pnpm audit`/`govulncheck`/SAST；安全审计只存在于手动触发的 `build` 工作流。
- **OLD-23**：没有 gosec/golangci/staticcheck 之类的 Go SAST。
- **OLD-24 / OLD-32**：仓库没有 Dependabot，也没有 CODEOWNERS。

## 决定

- 新增 `.github/workflows/security-scan.yaml`：`schedule`（每周一 06:00 UTC）+ `workflow_dispatch` 手动触发。
- 该工作流以离线可保证的 `go vet ./...` 作为 Go SAST 基线；在 `web-next` 目录执行 `pnpm install --frozen-lockfile && pnpm audit --audit-level=high` 作为前端依赖漏洞门禁。
- 工作流注释中写明后续在 runner 可安装外部工具时追加的顺序：`gosec ./...`、`golangci-lint run`、`govulncheck ./...`。不在本次强行安装这些工具，避免 workflow 因网络/工具链不可达而不稳定。
- 新增 `.github/dependabot.yml`：`gomod`（`/`）与 `npm`（`/web-next`，web-next 使用 pnpm，lockfile 为 `web-next/pnpm-lock.yaml`）每周六检查，每组 open PR 上限 5。
- 新增 `.github/CODEOWNERS`：`* @kingsunb`（与 `go.mod` 的 `github.com/kingsunb/NovaVeil` 对应）。

## 备选方案

- 在 `security-scan.yaml` 里直接 `go install golang.org/x/vuln/cmd/govulncheck@latest` 或安装 gosec：离线/无网络 runner 会失败，`go install` 也不进锁文件，与本工作流“非入侵、结果确定”的目标冲突；未选。
- 把 `pnpm audit` 放进 PR 级 `web-next-ci`：反馈更快，但每次 PR 都会受 npm 审计接口抖动影响；选择周频/手动，必要时再提升频率。
- CODEOWNERS 留空或用不存在的 owner：无效且会误导 PR reviewer；`@kingsunb` 与仓库模块路径一致，是最小可用的真实 owner 占位。
- 对 DEV-04 的 fork/伪版本依赖立即换源或删除 replace：上游无等价的 tagged 版本，盲改会破坏 `go build`，未作；以本笔记 + 周扫描 + Dependabot 作为风险缓释。

## 后果

- 收益：仓库首次具有周期性的供应链安全扫描、依赖更新提案和清晰的 code review 归属。
- 代价与边界：`go vet` 是轻量基线，不是 gosec/govulncheck 级别；`pnpm audit` 结果取决于 npm 审计接口；Dependabot 对 `github.com/looplj/axonhub/llm` 这类伪版本直接依赖与 `replace` 指向的个人 fork 可能无法给出常规更新 PR，风险没有消除，只是被周期性监控。
- 前端审计默认 `--audit-level=high`：低于 high 的漏洞不使 job 失败，避免审计接口与生态噪音阻塞开发。

## 验证

- 新增的 `.github/workflows/security-scan.yaml` 与 `.github/dependabot.yml` 通过 Python YAML 解析。
- `.github/CODEOWNERS` 符合 GitHub CODEOWNERS 的最小格式（`* @owner`）。
- `go vet ./...` 未在本地成功执行：工作区中前置审计修复尚未完成接线（`internal/op` 构建错误），与本次新增文件无关。