# Agent Note: 匿名灰度桶单测由概率断言改为固定随机源断言

Status: implemented

## 问题

`web-next/src/lib/flags.test.ts` 中 `getAnonymousBucket：同会话返回同值；clearAnonymousBucket 后可重新分桶` 一例用 `expect(c).not.toBe(a)` 断言"清除缓存后重新分桶的值不等于旧值"。匿名桶为 `Math.floor(Math.random()*100)` ∈ [0,99]，共 100 个可能值，两次独立随机的碰撞概率为 1/100 = 1%，而非该处注释所称"碰撞概率 ~0"。1% 的失败率在频繁 CI 下约每百次触发一次，使 `web-next CI / Lint + typecheck + unit tests` job 间歇性红，且无法靠重跑稳定消除——这是测试自身的逻辑缺陷，不是被测代码的问题：最新提交 `9292c86` 仅升级前端 dev 依赖（`package.json`/`pnpm-lock.yaml`/`pnpm-workspace.yaml`），未触及 `flags.ts`，且其提交说明只声称 typecheck/lint/build/size 全绿、未提 `pnpm test`。

## 决定

该测试改用 `vi.spyOn(Math, "random")` 固定随机源，断言确定值而非"两次随机不碰撞"：

- 首次分桶前 `mockReturnValue(0.001)`（→ 桶 0），断言 `a === 0` 且第二次调用复用缓存、`Math.random` 仅被调用一次——钉死"同会话稳定"。
- `clearAnonymousBucket()` + `sessionStorage.clear()` 后 `mockReturnValue(0.999)`（→ 桶 99），断言 `c === 99` 且 `Math.random` 累计调用两次——钉死"清除后确实重新分桶（缓存已失效）"。
- `expect(c).not.toBe(a)` 保留，但此时为确定性事实（99 ≠ 0），不再依赖概率。
- 用 `try/finally` 包裹并 `random.mockRestore()` 还原 `Math.random`，避免污染同文件后续依赖真实随机的 `sticky=false` 用例（该例用 1000 次采样的比率落在 (0.4,0.6)）。

## 备选方案

- **直接 `.skip`/删除该测试** — 最强论据是一行改动、立即止警；否掉因为它覆盖的契约（`clearAnonymousBucket` 后能重新分桶）是真实的，关掉会丢覆盖率，且"报警即关"会逐渐掏空门禁价值、掩盖未来真正的回归。
- **断言 `sessionStorage` 键被移除而非值不同** — 最强论据是只测 `clearAnonymousBucket` 的副作用、不碰随机；否掉因为它无法证明"清除后下次调用会重新摇号"这一核心契约（键被移除但模块内存缓存若未清，仍会返回旧值），且仍需 spy 才能验证重新分桶，并不更简单。
- **放宽为多次重试取不同值** — 最强论据是保留概率语义、改动小；否掉因为它只是把单次 1% 降到 1%^n，本质仍是概率断言，n 不够大时仍会偶发，n 够大则拖慢测试且语义模糊，治标不治本。

## 后果

- **收益**：该用例从约 1% flaky 变为完全确定性，`web-next CI / Lint + typecheck + unit tests` 不再因该例间歇红；连跑 20 次零失败，全套 575 例全绿，typecheck/lint/test:cov（90% 阈值）均通过。
- **代价与已知上限**：测试现在显式依赖"`Math.random` 是分桶唯一随机源"这一实现细节；若未来 `getAnonymousBucket` 改用 `crypto.getRandomValues` 或其它随机源，需同步更新 spy 目标。`try/finally + mockRestore` 是局部清理，不依赖 `afterEach` 的 `vi.unstubAllGlobals` 是否还原 `spyOn`，故对 Vitest 版本行为不敏感。

## 验证

- `web-next/src/lib/flags.test.ts` 该用例已改为固定随机源断言，`git diff` 可见。
- `cd web-next && pnpm typecheck && pnpm lint && pnpm test && pnpm test:cov` 全绿；`flags.ts` 行覆盖 95.65%、函数覆盖 100%，超 90% 阈值。
- 该用例连跑 20 次（`pnpm vitest run src/lib/flags.test.ts`）零失败。
