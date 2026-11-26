# Agent Note: 手动发布 Windows exe 的 Release 工作流

Status: implemented

## 问题

首个发布版本需要一个可分发的 Windows 可执行文件。仓库原有的 `build.yaml` 仅 `workflow_dispatch`、只产出 Linux 归档，`docker-publish.yaml` 只产出容器镜像，两者都不产 Windows exe、也不创建 GitHub Release；仓库无任何 git tag，`conf.Version` 默认 `dev`。约束是：普通提交与 tag 推送都不要自动构建 Windows，只在维护者明确发版时手动构建一次；Docker 发布维持现状。另外，升级 axonhub 引入的间接依赖 `github.com/coder/websocket v1.8.15` 的 `LICENSE.txt` 文本未被许可证 fail-closed 识别，导致 `scripts/build.sh` 直接失败。

## 决定

- 新增 `.github/workflows/release.yaml`，触发方式仅 `workflow_dispatch`（手动），输入 `version`（不含 `v` 前缀的版本号，按 `x.y.z[-pre]` 校验）与 `prerelease` 布尔值；不监听 `push` 或 tag。
- 单一 `windows-release` job（ubuntu-latest，30 分钟上限）：checkout（fetch-depth 0）→ 固定 pnpm 11.21.0 / Node 22.23.5 / Go 1.27.4 → `go test -count=1 -timeout=10m ./...` → `bash scripts/build.sh --targets windows/amd64`（复用前端构建、许可证清单与 zip 归档，`GOOS=windows CGO_ENABLED=0`）→ 校验 zip 与 `SHA256SUMS` → `gh release create "v<version>" --target <commit> --generate-notes [--prerelease]`。
- 版本号注入：`VERSION=<version>` 传入 `scripts/build.sh`，经 ldflags 注入 `conf.Version`；tag 固定为 `v${version}`。
- 幂等：`gh release view || gh release upload --clobber`，同一版本重复运行不会失败。
- 修复许可证识别：`scripts/licenses/go-module-report.mjs` 的 ISC 正则放宽为 `Permission to use, copy, modify, and(?:\/or)? distribute`，覆盖 coder/websocket 的 `and distribute` 变体（仍按 ISC 归类）。
- `docker-publish.yaml` 不改动。

## 备选方案

- **tag 推送自动构建**：能实现"打 tag 即发版"，但用户明确要求每次提交/打 tag 都不自动构建、只在明确发版时手动触发，未选。
- **push main 检测版本号变化后自动构建**：需要新引入仓库版本文件作为单一真相源，改动面大且非必要，未选。
- **第三方 action（softprops/action-gh-release）发布**：仓库要求 action pin 到 commit SHA，新增长期 SHA 依赖不划算，改用 runner 预装的 `gh` CLI。
- **只 `go build` 直传裸 exe、不经 `build.sh`**：更快，但会跳过许可证与 `SHA256SUMS` 的 fail-closed 校验，违背发布纪律，未选。

## 后果

- **收益**：发版完全由维护者手动掌控，普通提交与 tag 不会产生构建；一次点击即产出带许可证清单与校验和的 Windows exe 并创建 Release。
- **代价与已知上限**：需要仓库令牌具备 `contents: write`；只覆盖 `windows/amd64`；若需要代码签名或 Windows arm64 需扩展本工作流。许可证识别仍是白名单式，未来新依赖若用非常见许可证文本需再扩充。

## 验证

- `npx js-yaml .github/workflows/release.yaml` 解析通过。
- `go list -deps -json ./... | node scripts/licenses/go-module-report.mjs` 退出 0，`github.com/coder/websocket` 归类为 `ISC`。
- 本地 `bash scripts/build.sh --targets windows/amd64`（`VERSION=1.0.0`）退出 0，产出 `build/archives/novaveil-windows-amd64.zip`（内含 `novaveil.exe`）与 `SHA256SUMS`。
- `pnpm verify-notes` 通过。
