# Agent Note: 瘦身 Windows 构建产物并收敛 CI 存储

Status: implemented

## 问题

2026-11-09 的构建产物体积审计确认：

- `build/novaveil.exe` 为 65.73MiB，来自 `docs/STANDALONE_DEPLOYMENT.md` 第 3.2 节的裸 `go build -o novaveil main.go`。PE 中 DWARF 约 15.61MB、`.symtab` 约 4.07MB，合计约 19.68MB；发布二进制不需要这些调试信息。`scripts/build.sh` 本身已带 `-trimpath`、`-tags=jsoniter` 与 `-s -w`，缺口只在文档里的手动命令。
- `web-next-ci` 的 `Upload dist` 没有任何 `actions/download-artifact` 消费者；coverage 与 Playwright 报告默认保留 14 天，且 Playwright 报告在通过时也上传。
- `test` 与 `docker-publish` 在纯文档变更（`docs/**`、`.agents/**`、`**/*.md`）时仍完整运行，浪费 runner 时间与缓存。
- GHCR 只推不删，`ghcr.io/kingsunb/novaveil` 的 `:sha-*` 版本无界增长。
- `docker-publish` 的 GHA cache 使用 `mode=max`，缓存层更全但占用更多 GHA 存储配额。

## 决定

- 手动构建命令统一为 `go build -trimpath -tags=jsoniter -ldflags="-s -w" -o novaveil main.go`：`-s -w` 去掉符号表与 DWARF，`-trimpath` 去掉本机路径，`jsoniter` 与 `scripts/build.sh` 的发布构建一致。Windows 产物预计从约 66MB 降到约 46MB。
- `scripts/build.sh` 的 Windows 目标保持不变；文档补充 `scripts/build.sh --targets windows/amd64` 示例。
- 删除 `web-next-ci` 的 `Upload dist`；coverage 与 Playwright 报告保留 7 天；Playwright 报告改为 `if: failure()`。
- `test` 的 push/PR 与 `docker-publish` 的 push 增加 `paths-ignore`：`docs/**`、`.agents/**`、`**/*.md`。
- 新增 `.github/workflows/ghcr-cleanup.yaml`：每周一 04:00 UTC 运行，保留最近 20 个 GHCR 版本，`ignore-versions: "^latest$"`；action 固定到 `e5bc658cc4c965c472efe991f8beea3981499c55`（v5.0.0）。
- `docker-publish` 的 `cache-to` 从 `type=gha,mode=max` 改为 `type=gha,mode=min`。
- 与 [docker-publish 测试门禁与 SHA 固定](2026-09-20-docker-publish-test-gate-and-action-pinning.md) 同域不取代：那篇管测试门禁与 action pin，本篇管产物/缓存/触发与旧版本清理。与 [前端 gzip 预算](2026-09-23-raise-frontend-js-gzip-size-budget.md) 不重叠：那篇是 size-limit 门槛。

## 备选方案

- **UPX 压缩**：能把 Windows 产物压到约 20MB，但会增加启动解包开销、提高杀软误报率、破坏调试与签名流程；发布二进制默认不采用。
- **保留 dist artifact**：便于人工下载前端产物，但全仓没有 `actions/download-artifact` 消费者，删除后由 `web-next-ci` 的构建与 size-limit 继续覆盖。
- **Playwright 报告始终上传**：通过时上传可留档，但报告只在失败时才有诊断价值；改 `if: failure()` 减少无意义 artifact。
- **GHCR 永久保留所有版本**：可复现任何旧 digest，但 `:sha-*` 无界增长；选保留 20 个并在 docs 标注旧 digest 会被删除的风险。
- **GHA cache 保持 `mode=max`**：缓存更全、冷构建更快，但占用更多 GHA 存储；选 `mode=min` 换取存储空间，接受冷构建略慢。

## 后果

- 收益：Windows 发布产物预计减少约 20MB；`web-next-ci` 的 artifact 上传点从三个减为两个，且 Playwright 仅失败时上传；纯文档 push 不再触发 test/docker-publish，纯文档 PR 不再触发 test；GHCR 版本数有上界。
- 代价与已知上限：Windows 产物不再含 DWARF/符号表，线上崩溃只能用带调试信息的构建复现；GHCR 只保留最近 20 个版本，生产 pin 的旧 digest 会被删除；`mode=min` 下冷构建会重新拉取更多层。
- 工作区现有的 `build/novaveil.exe` 仍是旧构建，需重新构建后才会变小；本批不改本地文件。
- 重访条件：需要调试 Windows 线上崩溃时临时用带调试信息的构建；GHCR 保留数不够用时调整 `min-versions-to-keep` 并同步 docs。

## 验证

- 运行 `pnpm verify-notes` 校验树结构、格式与相对链接。
- `git diff --check` 无空白错误。
- 推送后 `gh workflow list` 能看到 `ghcr-cleanup`，`gh workflow run ghcr-cleanup.yaml` 可手动触发。
- Windows 重新构建后 `ls -lh novaveil.exe` 约 46MB。
