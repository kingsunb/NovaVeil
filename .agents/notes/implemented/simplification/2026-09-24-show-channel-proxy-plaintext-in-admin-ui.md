# Agent Note: 管理台直接显示渠道代理明文，移除列表掩码与眼睛揭示

Status: implemented

## 问题

[2026-09-22 密封自定义头/头模板/JWT/代理与列表掩码渠道代理](../bug-fix/2026-09-22-seal-header-proxy-and-mask-list.md) 为了不让带 `userinfo` 的代理 URL 进列表 JSON，把列表/创建/更新响应里的非空 `channel_proxy` 一律打成精确 `****`，明文只走 `POST /api/v1/channel/proxy/:id` 按需揭示；前端眼睛态、`****` 哨兵「保留已存代理」与复制渠道去掩码由 [web 明文不进 mutation 缓存](../bug-fix/2026-09-22-web-mutation-cache-and-nav-guards.md) 落地。这带来持续摩擦：(1) 管理员在列表和编辑页永远看不到完整代理地址，要点眼睛、多一次接口往返，编辑态一旦沿用掩码就得靠 `****` 哨兵「保留已存代理」，逻辑绕；(2) 只为「隐藏」就维护了一整套机制——后端的揭示接口与 `maskChannelProxyForList`、前端的眼睛状态与 `revealProxy`/`hideProxy`、`buildChannelUpdateRequest` 里删除 `****` 的分支。管理员界面本身是高权会话，Key 尚可按需揭示，唯独代理走了一条更绕的隐藏路径，收益不成比例。

## 决定

管理台（列表 + 编辑页）直接显示完整 `channel_proxy`（含 `userinfo`），不再掩码、不再按需揭示、不再用 `****` 哨兵「保留已存代理」：

- 后端：删除 `POST /api/v1/channel/proxy/:id`（`getChannelProxy` + `channelProxyView` 类型）与 `maskChannelProxyForList`；`channelAdminSummary` 原样透传 `channel_proxy`；`fetchModel` 里 `****` 哨兵回查库的兜底删除；`internal/op/channel.go` 更新恢复无条件写 `channel_proxy`（不再比较 `redactedSecret`）。
- 前端：删除 `CHANNEL_PROXY_MASK`、`buildChannelUpdateRequest` 对 `****` 的删除分支、编辑页眼睛状态与 `revealProxy`/`hideProxy`，代理字段改普通 Input；列表复制渠道时不再把 `****` 映射成空串；删除 `api.getChannelProxy`。
- `Eye`/`EyeOff` 图标仍保留给 Key 揭示，不受影响。

数据库备份导出打码、日志 `MaskProxySecret`、`proxy_pool`/`proxy_url` 密封均不在本次范围，维持原样——本次只改管理台显示路径。

## 备选方案

- **维持掩码 + 揭示（现状）**：守住「列表不回代理明文」，但管理员本就能看 Key 明文，代理再多一次揭示往返收益很低；已否决。
- **只摘眼睛、保留掩码与哨兵**：列表仍 `****`，编辑页只回显掩码、提交仍用哨兵保留；这保留两套语义（存储哨兵 + 显示掩码），没减复杂度；已否决。
- **列表掩码、编辑页打开时拉明文填充**：仍多一次请求，且明文会进 React Query 缓存，与直接显示无实质差别；已否决。

## 后果

- **收益**：管理台一眼看到完整代理，删掉揭示接口、`NoStore` 审计揭示路径与整套 `****` 哨兵/眼睛状态；后端少一个接口，前端少一块状态机。
- **代价/风险**：完整代理（可能含 `user:pass`）进入列表 API 响应、React Query 缓存与浏览器网络面板。这与 Key 明文揭示同级，「能看 Key 即能看代理」已被接受；含口令的代理应视为与渠道 Key 同等敏感。
- **边界**：备份导出仍把 `channel_proxy` 打成 `****`；日志里的代理仍走 `MaskProxySecret` 去 `userinfo`；落库仍用 `nv1:` 密封。这些不动。

## 验证

- `internal/server/handlers/secret_redaction_test.go`：`TestChannelAdminSummaryMasksProxyUserinfo` 改为 `TestChannelAdminSummaryKeepsProxyPlaintext`（断言透传原文，不再断言 `****` 与不泄漏），删除 `TestMaskChannelProxyForListKeepsEmpty`。
- `internal/op/seal_at_rest_test.go`：`TestChannelUpdateKeepsProxyWhenListMaskEchoed` 改为 `TestChannelUpdateWritesProxyPlaintext`（更新直接写新代理）。备份导出 `****` 打码的断言不动。
- `web-next/src/pages/Channels.test.tsx`、`web-next/src/lib/api.endpoints.test.ts`：删除揭示/哨兵用例，改为「列表与编辑页直接显示完整代理、保存原样提交」。
- `go build ./...`、`go test ./internal/server/handlers/ ./internal/op/`、`pnpm typecheck`、`pnpm lint`、`pnpm exec vitest run`。