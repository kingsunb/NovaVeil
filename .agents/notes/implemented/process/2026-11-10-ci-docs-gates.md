# Agent Note: CI 发布门禁、Note diff 门禁与文档事实修正

Status: implemented

## 问题

2026-11-10 的跨切面静态审计确认四条门禁与文档缺口：

- `docker-publish` 在 push `main` 后直接 buildx 推送 `:latest` / `:sha-<short>`，没有 Trivy、SBOM、镜像校验或运行时 smoke；“只发布已验证字节”的文档承诺与事实不符，且手动 `build` 的全审计不参与自动发布。
- `test.yaml` 与 `build.yaml` 对 `go test` 首次失败自动重试，flaky 被吞；`docker-publish` 却不重试，三处策略不一致。
- `AGENTS.md` 要求非平凡 PR 同 PR 带 Agent Note，但 `verify-notes` 只校验已有笔记，PR 完全不写笔记也能通过。
- 文档与事实漂移：web-next 页面测试与巨型组件行数、Go 工具链说明、pnpm lock 忽略规则、Compose 非 root 范围、`run-local.sh` 默认监听地址均有过期或错误描述。

## 决定

- `docker-publish` 改为 `build-verify-publish`：test-gate 增加前端单测；先构建 4 平台镜像归档（386/armv7 使用无 Bun 的 runtime target，因为官方 `oven/bun` 只提供 amd64/arm64），再由 `verify-image-archives.sh` 执行 Trivy HIGH/CRITICAL、CycloneDX SBOM、按架构校验与 native smoke；全部通过后用 `PUBLISH.sha256` 校验归档，`docker load` 已验证的 amd64/arm64 字节，打 per-arch `:sha-<short>-<arch>` 标签，再组 `:sha-<short>` 与 `:latest` 多架构 manifest 推送。发布阶段不再 buildx 重建。
- 删除 `test.yaml` 与 `build.yaml` 的 `go test` 重试；失败即红。
- 新增 `scripts/agent-notes/verify-pr-note.ts`：对 PR base..head 的 diff 检查非平凡路径（`internal/`、`cmd/`、`web-next/`、`scripts/`、`.github/`、`docs/modules/`、`docs/脱敏开发/`、部署/备份/路由契约文档、根构建文件）是否新增或更新 `.agents/notes/` 活跃笔记；`skip-agent-note` 与 `dependencies` 标签豁免，纯 lockfile 变更内置豁免。`verify-notes` workflow 在 PR 事件调用，并随 opened/synchronize/reopened/labeled/unlabeled 重跑。
- `security-scan` 增加 push `main` 与 PR 的依赖清单 paths 触发；根 `package.json` 的 `tsx` 固定 `4.23.15`。
- `run-local.sh` 默认绑定 `127.0.0.1`；等待改为 curl `/healthz`，停止优先读 pid 文件并校验命令行，保留 `pgrep` fallback。
- 文档同步：web-next 页面测试与行数、build-ci 发布链路与 lock 规则、AGENTS 仓库结构、SECURE_DEPLOYMENT 非 root 范围与本地 HTTP 定位、STANDALONE_DEPLOYMENT 的 run-local 默认值。

## 备选方案

- **继续 buildx 直接 push，事后扫描**：实现最省事，但扫描对象不是发布字节，无法满足“发布字节 = 扫描字节”；未选。
- **只保留手动 build 全审计，停用 docker-publish**：审计最强，但 main 失去自动镜像；未选。
- **所有 PR 一律要求 Note**：规则最简单，但依赖与排版 PR 会被迫写空洞笔记；未选。
- **Note 门禁只扫源码不扫 docs 契约**：误报更低，但本仓库的部署/路由契约变更同样需要因果记录；未选。
- **发布时仍用 buildx 重建并复用 cache**：能保留 provenance，但仍无法证明重建字节与扫描字节一致；未选。

## 后果

- 收益：main 发布只推送已验证归档，per-arch 标签、`PUBLISH.sha256` 与 SBOM artifact 可追溯；flaky 不再被重试掩盖；无 Note 的非平凡 PR 在 `verify-notes` 处失败并输出可操作清单。
- 代价与已知上限：main 发布预计升至 80 分钟量级（与手动 build 的全矩阵审计同量级）；多架构 manifest 由 `docker manifest` 手工组装，未生成 buildx provenance，生产仍需 pin digest；386/armv7 镜像不含 Bun sidecar，免费档不可用，且它们不进入发布 manifest；`skip-agent-note` 标签可被滥用（保留 GitHub 审计日志）；Note 门禁是启发式路径匹配，新增顶层目录需同步维护规则；`security-scan` 的 PR 触发只覆盖依赖清单，不覆盖全部代码变更。
- 运行时镜像在构建时执行 `apk upgrade` 拉取 Alpine v3.21 安全补丁；基础 rootfs 仍按 digest 固定，但包版本随 Alpine 仓库滚动，同一 digest 不保证字节级可复现，Trivy 扫描的是升级后的最终归档。

## 验证

- 用户执行 `pnpm verify-notes` 校验树、格式与归档封印；新增笔记必须通过。
- 用户执行 `AGENT_NOTE_PR_BASE_SHA=<base> AGENT_NOTE_PR_HEAD_SHA=<head> pnpm verify-pr-note` 复现 PR diff 门禁。
- 发布链路需在 CI 实跑：`bash scripts/build.sh --targets linux/amd64,linux/386,linux/arm64,linux/arm --no-archive`、`scripts/build-image-archives.sh build/image-archives`、`scripts/verify-image-archives.sh build/image-archives`；本次只做静态修改，未执行。
