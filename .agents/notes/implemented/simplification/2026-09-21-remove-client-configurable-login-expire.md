# Agent Note: 移除客户端可配置登录会话有效期

Status: implemented

## 问题

登录接口此前接受客户端提交的 `expire`，服务端将其映射为 JWT 有效期并钳制到 24 小时；前端只暴露“信任此设备”二值开关，无法选择中间时长。该契约增加了前后端对登录会话时长的共同维护面，也使调用方可以自行决定凭证窗口。

## 决定

登录会话有效期改由服务端固定为 24 小时，不再暴露客户端可配置字段：

- `internal/server/auth/auth.go` 的 `GenerateJWTToken` 不再接收 `expiresSec`，固定签发 `sessionMaxAge = 24 * 3600` 秒的 JWT，并继续返回该值供 cookie `MaxAge` 使用。
- `internal/model/user.go` 的 `UserLogin` 删除 `Expire` 字段。
- `internal/server/handlers/user.go` 登录成功后直接调用无参 `GenerateJWTToken()`。
- 前端 `web-next/src/lib/api.ts`、`web-next/src/store/auth.tsx`、`web-next/src/lib/types.ts` 删除 `expire` 传递，登录调用只传用户名和密码。
- 前端 `web-next/src/pages/Login.tsx` 删除“信任此设备（24 小时内免登录）”复选框及其状态/计算逻辑；不再把登录有效期作为用户可选项。
- 相关认证与 API 测试已同步更新。

当前登录行为是：每次成功登录后签发 24 小时有效的 JWT/cookie；登出仍会轮换 JWT 密钥，使已签发 token 失效。

## 备选方案

### 为什么不保留客户端 `expire` 并加登录页选择器？

最强论据是保留现有后端契约，只补前端下拉框即可让用户选择 15 分钟、1 小时、4 小时或 24 小时。该方案实现面较小，但它要求继续维护客户端可配置凭证窗口，并保留 `UserLogin`、认证函数和前端 login 链路上的 `expire` 契约；本次需求是删除这套后端能力，因此不采用。

### 为什么不固定为 15 分钟？

15 分钟是此前未勾选“信任此设备”时的默认窗口，安全边界更短，但会让所有管理员每次登录都面临短会话，且与当前产品体验相比变化过大。本次确定采用固定 24 小时。

### 为什么不放宽到 24 小时以上？

这会增加被盗 cookie 的可用窗口，并重新触发此前登录有效期审计（L-8）的安全评估。现有 24 小时上限保持不变，不引入更长的凭证生命周期。

## 后果

- **收益**：登录会话生命周期只有一个服务端事实，不再需要前后端同步维护 `expire` 语义；客户端无法通过请求体改变 JWT 或 cookie 的有效期。
- **收益**：删除了 `expiresSec` 分支和 `maxAgeCeiling` 钳制逻辑，认证签发路径更短、更容易验证。
- **代价与已知上限**：此前未勾选“信任此设备”时的 15 分钟会话窗口被统一为 24 小时；这是本次固定策略的明确行为变化。若需要更短的默认会话，应重新评估安全要求后再改固定值，而不是恢复客户端配置。
- **代价与已知上限**：前端登录页不再提供时长选择，管理员只能在登录时接受固定的 24 小时窗口。

## 验证

- 后端：`go build ./...`、`go vet ./...` 通过。
- 后端相关测试：`go test ./internal/server/auth/ ./internal/server/middleware/ ./internal/model/ ./internal/server/handlers/` 通过。
- 前端：`pnpm typecheck`、`pnpm lint` 通过。
- 前端相关测试：`pnpm exec vitest run src/store/auth.test.tsx src/lib/api.test.ts` 通过，共 48 条测试。
