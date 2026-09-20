# Agent Note: docker-publish 测试门禁与 GitHub Actions SHA 固定

Status: implemented

## 问题

2026-09-20 后续审计的 DevOps 域确认：

- **DEV-01/H-05**：`docker-publish` 在 push 到 `main` 时不跑任何测试/类型检查/漏洞门禁就构建并推送 `:latest` 与 `:sha-*`；且 `docker/build-push-action@v6` 是 mutable tag。
- **DEV-07**：`verify-notes` 使用 `actions/checkout@v4`、`actions/setup-node@v4` 两个 mutable tag，且每次 `npx tsx` 现拉 latest，门禁结果可随上游漂移。
- **DEV-08**：`.gitignore` 未覆盖 `.env`、`*.pem`、`*.key`。

## 决定

- `docker-publish` 拆成两个 job：`test-gate` 先跑 `web-next` 的 `pnpm install --frozen-lockfile && pnpm typecheck`，再跑 `go test -count=1 -timeout=10m ./... && go vet ./...`；`build-and-publish` 增加 `needs: test-gate`，测试不过不登录、不 push。
- `docker/build-push-action` 固定到完整 commit SHA `48aba3b46d1b1fec4febb7c5d0c644b249a11355`（对应 `v6.10.0`，该 SHA 通过 `git ls-remote https://github.com/docker/build-push-action.git refs/tags/v6.10.0` 获取）。
- `verify-notes` 的 `actions/checkout` 固定到仓库既有 SHA `11bd71901bbe5b1630ceea73d27597364c9af683`（v4.2.2），`actions/setup-node` 固定到仓库既有 SHA `49933ea5288caeca8642d1e84afbd3f7d6820020`（v4.4.0），并显式 `persist-credentials: false`。
- `verify-notes` 的三条 `npx tsx` 改为 `npx --yes tsx@4.23.15`，避免 latest 漂移；根 `package.json` 没有 lockfile 也没有声明 `tsx` devDependency，因此无法改用 `pnpm exec tsx` 或 `npx --no-install tsx`。
- `.gitignore` 增加 `.env`、`.env.*`、`*.pem`、`*.key`。

## 备选方案

- 把 `docker/build-push-action` 只 pin 到 `v6.10.0` 完整 tag：能满足“非 @v6”，但 tag 仍可移动，审计要 SHA，未选。
- 在 `test-gate` 里跑完整前端单测和构建：更接近测试门禁理想，但 push 路径会显著变慢，且前端单测已由 `web-next-ci` 与手动 `build` 工作流覆盖，未选。
- 把 `verify-notes` 改成 `pnpm exec tsx`：需要根 `package.json` 有 `tsx` devDependency 且提交根 lockfile；当前根包只是私有 agent-notes 工具、lockfile 被 `.gitignore` 忽略，未选。
- 直接使用 `npx --no-install tsx`：runner 未预装 tsx，会失败，未选。

## 后果

- 收益：`main` push 只有在 Go 测试、Go vet、前端类型检查都通过后才会构建并推送镜像；三个 workflow 的 action 与 tsx 版本不再漂移；敏感本地文件默认不进入 git。
- 代价：`docker-publish` 增加 `test-gate` 耗时（pnpm install + typecheck + go test/vet，设计为 30 分钟上限）。`go test` 在 fresh checkout 上不依赖前端构建产物（`static/out/.gitkeep` 已跟踪），当前成立；若未来有测试要求真实前端资产，需在 gate 里加 `pnpm build`。
- 已确认不修 DEV-06：`template-check.yaml` 的 PR 必填行已在先前的 bug-fix note 中完成修复。

## 验证

- 用 Python YAML 解析 `.github/workflows/docker-publish.yaml` 与 `.github/workflows/verify-notes.yml` 通过。
- `git ls-remote` 复核 `docker/build-push-action` 的 `v6.10.0` tag SHA 为 `48aba3b46d1b1fec4febb7c5d0c644b249a11355`。
- `cd /workspace/NovaVeil && go build ./...` 复核：失败点仅在前置脏工作区的 `internal/op/apikey.go`/`channel.go`（`undefined: seal`/`err`，来自早前审计修复对 `internal/seal` 包的未完成接线），与本批 `.github/.gitignore/docs` 变更无关，本批未因此追加改动。
- `cd /workspace/NovaVeil && pnpm verify-notes` 通过。