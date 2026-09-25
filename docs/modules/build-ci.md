# 构建与 CI（scripts · docker · .github/workflows）

前端先、后端后、发布即扫描过的字节——这是构建链的三个不变式。

## 怎么做的

### 构建（scripts/build.sh）

- **顺序硬约束**：`rm -rf static/out` → `web-next pnpm install --frozen-lockfile && pnpm build`（vite outDir 指向 `../static/out`）→ `go build`。`static/static.go` 用 `//go:embed all:out` 在编译期抓产物——**不先构建前端，连 `go test` 都过不了**（`static/out/.gitkeep` 保证干净克隆可编译）。
- Go 构建参数：`-trimpath -ldflags "-X conf.Version/... -s -w" -tags=jsoniter`，`CGO_ENABLED=0`；启动即校验 Go ≥ 1.26，旧工具链的误导错误提前拦截。
- 参数矩阵：`--local`（本机架构，跳过许可证与 zip）、`--targets all`（7 个非 cgo 平台）、`--include-android`（需 NDK r28c，仅正式发布）。
- 发布归档 = 改名二进制 + README + LICENSE + 两份许可证清单（go list -deps 与 pnpm licenses 归一化，fail-closed 校验）+ SHA256SUMS；linux 产物另存 `build/docker/<platform>/` 供 Dockerfile 按 TARGETPLATFORM COPY。
- 版本注入靠 ldflags，手动 `go build` 的产物显示 dev。

### 运行与 Docker

- `run-local.sh`：找对应架构二进制 → 杀旧进程 → nohup 启动 → 端口等待 → `GET /` 必须 200 → 打印初始密码与版本；只换进程、保留 `data/`。
- Compose 生产模板：`NOVAVEIL_IMAGE` 必填且建议 pin `@sha256:<digest>`；非 root 10001:10001、只读 rootfs + /tmp tmpfs、`cap_drop: ALL` + no-new-privileges、pids/mem/cpus 限额、宿主仅绑 `127.0.0.1:8888`、外部卷预置 0700@10001。
- Dockerfile：Alpine 按 digest pin、二进制 0555 root 所有、entrypoint umask 0077、wget healthcheck。

### CI（7 个 workflow）

| workflow | 触发 | 职责 |
|---|---|---|
| test | push / PR | 先 `pnpm build` 再 `go test`（go:embed 依赖） |
| web-next-ci | web-next 变更 | typecheck + lint + vitest（覆盖率阈值）+ size-limit + Playwright E2E/a11y |
| docker-publish | push main | test-gate → 构建多架构 → 推 GHCR `:latest` / `:sha-<short>` |
| verify-notes | push / PR | Agent Note 门禁三连（树 + 格式 + 归档封印），tsx 固定版本 |
| security-scan | 周一 cron / 手动 | go vet + gosec（high，排除 G115）+ govulncheck + pnpm audit |
| build | 仅手动 | 80+ 分钟全审计：Trivy secret 扫 → 多平台构建 → 漏洞门禁 + SBOM + 镜像校验 + smoke → artifact |
| template-check | issue / PR | 模板合规自动关闭 |

- lefthook：pre-commit 跑前端 typecheck+lint，pre-push 跑 agent-notes 校验；test/build 一律留给 CI。
- 所有 action pin commit SHA；`persist-credentials: false`。

### 工具链

Go 1.26.7 / Node 22.19 / pnpm 11.21 在 CI 硬编码；本地开发用 `GOTOOLCHAIN=auto` 让 1.26.7 工具链自动下载进 `GOMODCACHE`，与系统 Go 隔离（详见 [STANDALONE_DEPLOYMENT.md](../STANDALONE_DEPLOYMENT.md) §2）。

## 设计想法

- **发布绝不重新 build，只 docker load 已验证归档**：被漏洞扫描 / SBOM / smoke 验证过的字节必须与发布字节一致——供应链可审计；`PUBLISH.sha256` 为正式 release job 预留。
- **测试要跑就得先构建前端**：`//go:embed all:out` 在编译期抓产物，CI 的 test workflow 专门先跑 `pnpm build`——这个"怪"顺序是 embed 机制的直接后果。
- **许可证 fail-closed**：归档必须含真实许可证报告，`--skip-licenses` 强制搭配 `--no-archive`——发布物不带许可证清单就不允许出货。

## 已知边界

- CI 的 `go test` 以 `-race` 运行（超时 20m）：新增竞态须修复而不是移除该开关。
- `docker-publish` 推 `:latest` / `:sha-<short>` 可变 tag 且 `provenance:false`（无构建出处证明）；生产必须 pin digest。
- 协议转换核心依赖 `looplj/axonhub/llm` 未打 tag 的 pseudo-version（go.sum 锁哈希，防篡改不防上游策略变化）；`tmaxmax/go-sse` 经 replace 指向 looplj fork（上游缺 Stream API），供应链集中度偏高。
- `web-router` 的 nginx 基础镜像已按 digest 固定；升级时须与 `web-next/Dockerfile` 的 runtime 阶段同步并核对 digest。
- `.gitignore` 的 `pnpm-lock.yaml` 规则实际命中已跟踪的 `web-next/pnpm-lock.yaml`（对已跟踪文件无效）——这是一个潜在陷阱：若 lockfile 一旦脱离跟踪会被静默忽略。
- web-next 的 Dockerfile 内 pnpm 版本与 CI 需对齐时以 CI（11.21+）为准。

## 深入阅读

- [STANDALONE_DEPLOYMENT.md](../STANDALONE_DEPLOYMENT.md)、[SECURE_DEPLOYMENT.md](../SECURE_DEPLOYMENT.md)、[BACKUP_RESTORE.md](../BACKUP_RESTORE.md)
- fork 决策：[2026-09-21-remove-gin-contrib-sse-fork-keep-go-sse-fork](../../.agents/notes/implemented/process/2026-09-21-remove-gin-contrib-sse-fork-keep-go-sse-fork.md)
