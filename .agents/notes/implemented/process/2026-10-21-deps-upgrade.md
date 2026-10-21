# Agent Note: 依赖升级批次 1/2 与暂缓项边界

Status: implemented

## 问题

2026-10-21 的依赖审计列出前端 4 个 A 级补丁、Go 2 个直接依赖以及 71 个间接依赖。若一次升级全部项目，锁文件、LLM 协议适配和工具链迁移会混在同一批变更中，失败后难以判断来源，也无法按风险单独回滚。仓库需要一个明确的低风险边界，并记录暂缓项的前置条件。

## 决定

- `web-next` 升级 `@tanstack/react-query` 5.104.5、`@tanstack/react-virtual` 3.14.21、`postcss` 8.5.31、`vite` 8.3.5，保留现有 `^` 范围写法。
- `vite@8.3.5` 发布未满 24 小时，已加入 `web-next/pnpm-workspace.yaml` 的 `minimumReleaseAgeExclude`；不降级 `vite`，也不放宽全局供应链策略。
- Go 只升级 `github.com/andybalholm/brotli` v1.2.6 与 `github.com/looplj/axonhub/llm` 伪版本 `v0.0.0-20261021081413-cbdf9a6d4a3d`。后者是仓库实际声明的模块路径，任务文本中的 `github.com/axonhub/llm` 不对应现有依赖。
- `go.mod` 的 `go 1.26.7` 与 `toolchain` 指令不变；所有 Go 验证使用 `GOTOOLCHAIN=go1.26.7`，不依赖服务器默认的 Go 1.27.1。
- CI 工具链口径为 pnpm 11.21.0、Go 1.26.7、Node 22.19.0；验证必须使用与 CI 相同的版本。
- 不手工升级 71 个间接 Go 依赖，也不升级 TypeScript 7、`@types/node` 26、ESLint 10、typescript-eslint 8.72、knip 6.42、lucide-react 1.61、happy-dom 21 及两个 dev 漏洞传递链。它们分别受 peer 约束、运行时版本、迁移成本或上游修复状态阻塞。
- 批次 1 与批次 2 分开提交，使前端锁文件与 Go 模块变更可以独立回滚。

## 备选方案

- **一次升级全部 73 个 Go 可升级项**：能让 `go.mod` 一次接近最新，但会把 71 个依赖上游节奏的模块与本任务绑定，扩大协议和构建回归面，未选。
- **每个包单独提交**：回滚粒度最细，但会制造多次锁文件重算和重复验证，且同批补丁之间没有独立业务语义，未选。
- **保持全部依赖不动**：改动为零，但会放弃审计已确认的低风险补丁与直接依赖修复，未选。
- **同时升级前端 minor/major 以清空 outdated**：表面上更彻底，但 TypeScript 7 被 typescript-eslint 的 `<6.1.0` peer 约束阻塞，happy-dom 21 需要测试环境适配，未选。

## 后果

- **收益**：两个直接 Go 依赖保持精确锁定，前端 4 个补丁进入锁文件；`go.mod` 无连带间接依赖变化，前后端验证均通过。
- **代价与边界**：服务器默认 Go 与 CI 不一致，后续执行必须显式使用 `GOTOOLCHAIN=go1.26.7`；`vite@8.3.5` 的供应链例外需在发布满 24 小时后移除。dev 依赖漏洞与上述暂缓项仍然存在。上游放宽 peer、Node 运行时升级或发布迁移说明后，应重新评估对应项。

## 验证

- `npx -y pnpm@11.21.0 -C web-next install --frozen-lockfile`：通过，锁文件满足供应链策略。
- `npx -y pnpm@11.21.0 -C web-next build && npx -y pnpm@11.21.0 -C web-next typecheck && npx -y pnpm@11.21.0 -C web-next lint && npx -y pnpm@11.21.0 -C web-next test`：通过。
- `GOTOOLCHAIN=go1.26.7 go build ./... && GOTOOLCHAIN=go1.26.7 go vet ./...` 与指定包测试通过。
- `git diff -- go.mod`：仅两个直接依赖版本变化，无 `// indirect` 行变化。
