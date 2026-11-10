# Agent Note: 依赖升级批次 3 与安全扫描恢复

Status: implemented

## 问题

2026-11-10 的依赖审计发现三组彼此关联的问题：

- `.github/workflows/security-scan.yaml` 连续 8 次失败，卡在 gosec v2.29.0 无法解析 Go 1.27.3 的 export data；其后的 govulncheck 与 `pnpm audit` 从未执行，安全扫描形同停摆。
- 16 个 Dependabot PR（#1–#18）全部被 `template-check.yaml` 以“未按模板填写”自动关闭；依赖更新通道被模板机器人堵死。
- `pnpm audit --prod` 为 0，但 `pnpm audit --dev` 有 9 high + 1 moderate。其中 7 条 high 来自 `@size-limit/preset-app -> @size-limit/time -> estimo -> nanoid/extract-zip/basic-ftp/zod`，2 条来自直接依赖 happy-dom 20.14.6。

同时，前端与 Go 直接依赖存在一批 patch/minor 升级；`go get` 还提示间接依赖 `github.com/gabriel-vasile/mimetype@v1.4.15` 已被撤回（retracted），原因是处理特制 CDF 文件时呈二次时间复杂度。

修复工具链后，gosec v2.30.0 首次运行报 4 条 high：3 条 G404（体检问题池与上下文填充使用 math/rand）和 1 条 G101（中文错误文案含“密码”）。前两者只用于非安全随机化，后者不是凭据，均为误报。

## 决定

- `security-scan.yaml` 固定 gosec v2.30.0、govulncheck v1.9.0；两者使用的 `golang.org/x/tools v0.52.0` 能解析 Go 1.27.3 的 export data。
- `template-check.yaml` 的 PR job 跳过 `dependabot[bot]`，让 Dependabot 回到正常 PR 流程；普通 PR 仍执行原有模板检查。
- Go 直接依赖升级 7 个：brotli v1.3.0、gin-contrib/cors v1.7.11、gin-contrib/sse v1.2.1、klauspost/compress v1.20.4、gjson v1.20.2、sjson v1.2.9、x/sys v0.50.0。
- 间接依赖只修 `mimetype` v1.4.15 -> v1.4.16（撤回版本）与 `tidwall/pretty` v1.2.1 -> v1.3.0（sjson 的传递升级），不全量升级其余 71 个间接依赖。
- 前端升级 15 个 patch/minor：`@tanstack/react-query`、`@tanstack/react-virtual`、`lucide-react`、`vite`、`@vitejs/plugin-react`、`@tailwindcss/vite`、`tailwindcss`、`@playwright/test`、`@vitest/coverage-v8`、`vitest`、`eslint`、`globals`、`knip`、`typescript-eslint`、`rollup-plugin-visualizer`。
- `@size-limit/preset-app` 换为 `@size-limit/file` ^14.3.0，并从 `web-next/.size-limit.json` 删除 3 处 `"running": false`；`running` 是 time 插件专属选项，删除 preset-app 后保留它会触发 `pluginlessConfig`。`pnpm size` 继续按 gzip 计算。
- happy-dom 升 ^21.0.4，修 2 条 high。
- 保持 TypeScript ~6.0.3、`@types/node` ^22.20.5、minimatch moderate 不强制 overrides；理由分别是 TS 7 跨大版本、`@types/node` 要匹配 CI Node 22.19.0、minimatch 跨 11.x 且 high 门禁不拦。
- 保持 `go-sse` 的 replace 指向 `looplj/go-sse` fork；上游 v0.12.0 仍缺 `Stream`/`NewStreamWithConfig`，且改了 EOF 截断语义，不能只凭版本号切换。
- 对上述 4 条 gosec 误报按仓库既有风格就地加 `#nosec G404`/`#nosec G101` 并写明原因，不在 workflow 全局排除规则。

## 备选方案

- **只把 size-limit 升到最新，保留 preset-app**：`@size-limit/preset-app` 14.3.0 已是当前最新，estimo 链未跟进，7 条 high 不会消失；未选。
- **用 pnpm overrides 强推 nanoid/extract-zip/basic-ftp/zod**：能消 advisory，但 zod 3 -> 4 会破坏 chromium-bidi，且覆盖范围大、后续难维护；未选。
- **直接删除 size-limit**：能消 7 条 high，但丢失 gzip 预算门禁；未选。
- **一次性升级 TypeScript 7 与 `@types/node` 26**：跨大版本且与本轮补丁无关，容易混入构建回归；暂缓。
- **继续让 template-check 关闭 Dependabot PR**：依赖更新通道被堵死，即使安全扫描恢复也没有自动升级；未选。
- **继续用 gosec v2.29.0 等待上游修 x/tools**：CI 已红 8 周，等待没有确定日期；未选。
- **在 workflow 全局 `-exclude=G404,G101`**：会掩盖其他位置的真实随机数/凭据问题；未选。

## 后果

- **收益**：开发链 high 从 9 降到 0（仍余 1 条 moderate minimatch）；生产依赖保持 0；安全扫描恢复可执行；Dependabot PR 不再被模板机器人误杀；`pnpm size` 仍保留 gzip 预算。
- **代价与边界**：`@size-limit/file` 不再提供运行时间/加载时间指标；原配置本来就是 `running: false`，没有实际指标损失。happy-dom 21、vitest 5.1 与 ESLint 10.14 需要实际构建与测试验证。Go 间接依赖未全量升级；TypeScript 7、`@types/node` 26、minimatch moderate 仍待跟进。
- **重访信号**：`@size-limit/time` 上游修复 estimo 链后可重新评估；CI Node 升到 24 时同步 `@types/node`；typescript-eslint 明确支持 TS 7 后再升级。gosec 结果依赖 4 处带理由的 `#nosec`；新增同类代码需重新评估，不能默认照抄。

## 验证

- `cd web-next && pnpm install && pnpm audit --prod && pnpm audit --dev --audit-level=high && pnpm build && pnpm lint && pnpm test && pnpm size`
- `go get github.com/andybalholm/brotli@v1.3.0 github.com/gin-contrib/cors@v1.7.11 github.com/gin-contrib/sse@v1.2.1 github.com/klauspost/compress@v1.20.4 github.com/tidwall/gjson@v1.20.2 github.com/tidwall/sjson@v1.2.9 golang.org/x/sys@v0.50.0 github.com/gabriel-vasile/mimetype@v1.4.16 && go mod tidy && go test ./... && go vet ./...`
- `gh workflow run security-scan.yaml && gh run watch`
