# Agent Note: 恢复登录「信任此设备」复选框（持久 24h / 单次会话二选一）

Status: implemented

## 问题

登录页的「信任此设备（24 小时内免登录）」复选框在 2026-09-21 的登录会话加固中，连同客户端可配置 `expire` 一起被删除，登录一律固定签发 24 小时 cookie。但「记住设备」（持久 24h）与「单次会话」（浏览器关闭即失效）是两种主流且合理的会话偏好：管理员在共享或临时机器上不希望勾选后仍留下 24 小时 cookie，而一刀切 24h 让「不记住」无从表达。

同时，登录页长期常显一行「首次登录请使用控制台初始化提示的管理员凭据」提示——它在非首次、管理员早已改密之后仍然出现，对日常登录是误导噪音；且登录页本身无法在未登录态得知是否处于「必须改密」的首次状态，无法精确到「仅首次显示」。

## 决定

在保留 2026-09-21 安全边界的前提下恢复二选一，并清理登录页提示：

1. **恢复复选框，默认勾选**。`web-next/src/pages/Login.tsx` 新增 `remember` 状态（默认 `true`），提交时经 `login(username, password, remember)` 透传；勾选 = 持久 24 小时，取消 = 单次会话（会话 cookie，浏览器关闭即失效）。
2. **安全边界不变：仍不接受客户端自报任意时长**。`internal/model/user.go` 的 `UserLogin` 只新增布尔 `Remember bool`（`json:"remember"`），不恢复 `Expire` 字段；`internal/server/auth/auth.go` 的 `GenerateJWTToken(remember bool)` 仍固定签发 24 小时 JWT `exp`（服务端有效期上限不变），仅用 `remember` 决定返回给 `SetAuthCookie` 的 cookie `maxAge`：
   - `remember=true` → `sessionMaxAge = 24*3600`（持久 cookie）；
   - `remember=false` → `0`（`net/http` 省略 `Max-Age`，渲染为会话 cookie）。
3. **前端链路统一**：`web-next/src/lib/types.ts` 的 `UserLoginRequest` 新增 `remember?: boolean`；`web-next/src/lib/api.ts` 的 `login` 改收 `UserLoginRequest`；`web-next/src/store/auth.tsx` 的 `login` 签名扩为 `(username, password, remember = true)`。
4. **删除登录页「首次登录请使用控制台初始化提示的管理员凭据」提示**——不做「仅首次显示」的条件渲染，因为登录页在未登录态无法可靠获知 `MustChangePassword`，且该提示对已改密管理员是纯噪音。

## 备选方案

- **恢复成历史任意 `expire` 客户端提交** — 最强论据是最贴近 2026-09-21 之前的契约、改动面小；否掉因为会重新打开 L-8 已关掉的「客户端自报凭证窗口」攻击面，而本次需求只有「记住 / 不记住」二选一，布尔足以表达。
- **只删提示、不恢复复选框** — 最强论据是改动最小、维持 2026-09-21 的「登录会话只有一个服务端事实」；否掉因为「记住设备 vs 单次会话」本身是合理且常见的管理员偏好，且布尔二选一不破坏固定 24h 上限，收益明确。
- **「不勾选」映射为更短的固定 JWT exp（如 15 分钟）而非会话 cookie** — 最强论据是服务端也显式收紧凭证窗口、不依赖浏览器关闭行为；否掉因为「单次有效」的字面语义就是浏览器会话，会话 cookie 直接表达，无需再引入第二套服务端时长；且 15 分钟固定时长在 2026-09-21 已被否过（见旧笔记「为什么不固定为 15 分钟」）。
- **登录页条件显示首次提示（新增无鉴权状态端点）** — 最强论据是保留新用户首次登录指引；否掉因为新增一个未鉴权端点会泄漏「系统是否首次初始化」的状态，且该提示的受众极窄，删除更简单。

## 后果

- **收益**：管理员可在共享/临时机器上主动选择「不记住」，避免残留 24 小时 cookie；「记住设备」偏好仍然可用；登录页不再向已改密用户展示误导性首次提示。
- **代价与已知上限**：会话 cookie 仍带 SameSite=Lax/HttpOnly/Secure 加固，且服务端 JWT `exp` 恒 24 小时，「不勾选」的凭证仍以 JWT 24h 为硬上限（浏览器不关时最多 24h）；未勾选时依赖浏览器关闭即失效，属客户端行为而非服务端强制。恢复的是布尔二选一，不恢复任意时长下钻。
- **取代关系**：本篇**部分取代** [2026-09-21 移除客户端可配置登录会话有效期](../simplification/2026-09-21-remove-client-configurable-login-expire.md) 的「删除『信任此设备』复选框、登录页不再提供时长选择」两处；其核心安全决定——移除客户端 `expire` 字段、JWT/cookie 固定 24h 上限、登出轮换密钥——全部保持成立，故该笔记保留为历史因果并互链，不归档。

## 验证

- 后端：`go build ./...`、`go vet ./...` 通过。
- 后端测试：`go test ./internal/model/ ./internal/server/auth/ ./internal/server/middleware/ ./internal/server/handlers/` 通过；`auth` 包 `TestGenerateJWTTokenRememberControlsCookieMaxAge` 钉死 remember=true→24h、false→0；`middleware` 包 `TestSetAuthCookieSessionScope` 钉死 maxAge=0 时不渲染 `Max-Age`（会话 cookie）。
- 前端：`pnpm typecheck`、`pnpm lint` 通过。
- 前端测试：`Login.test.tsx`（复选框默认勾选、取消勾选 remember=false 透传、首次提示已删除）、`api.test.ts`（login 请求体透传 remember）、`store/auth.test.tsx` 通过，全量 `pnpm test` 542 条通过。