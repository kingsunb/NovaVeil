# Agent Note: 静态审计中的口令校验限速、Cookie Path、nginx 安全头与非流式响应上限

Status: implemented

## 问题

2026-11-01 全仓静态审计（[报告](../../../../docs/audit/2026-11-01-static-audit.md)）确认四项可低成本修复的缺口：

1. 已登录会话的 `change-password`、`change-username` 在旧密码错误时直接返回 400，不进入登录失败计数。管理 API 令牌桶默认关闭，拿到管理员 cookie 后可以无限撞旧密码。
2. 认证 cookie 的 Path 为 `/`，同主机其它应用的请求会携带它；`SameSite=Lax` 挡不住同站请求。
3. 分拆部署的 `web-next/nginx.conf` 与 `web-next/router.nginx.conf` 只有 `X-Frame-Options: SAMEORIGIN`，CSP 没有 `frame-ancestors`，也没有 HSTS，弱于 Go 侧 `securityheaders.go`。
4. 上游非流式成功响应没有体积上限：axonhub `httpclient` 对状态码小于 400 的响应直接 `io.ReadAll`，只有错误响应才截断。异常上游可用一个 200 撑大进程内存。

## 决定

- **口令校验限速**：`changePassword` 与 `changeUsername` 在解析正文前先走 `loginLimiter`，key 为 `credential-verify:<ClientIP>`。旧密码错误调用 `recordFailure`，成功调用 `reset`；两者共用一个命名空间，与登录接口的 `<ClientIP>` 命名空间隔离，互不阻塞。`changeUsername` 的非预期错误改为返回 `resp.ErrDatabase` 并记录日志，不再把 `err.Error()` 回给客户端。
- **Cookie Path**：`AuthCookiePath` 改为 `/api`，设置与清除共用同一常量，管理 API 之外不再携带认证 cookie。
- **nginx 安全头**：两份 nginx 模板各 5 个 `add_header` 区块统一为 `X-Frame-Options: DENY`、CSP 追加 `frame-ancestors 'none'`、新增 `Strict-Transport-Security: max-age=31536000`。不加 `includeSubDomains`，避免影响同域其它子域。
- **非流式响应上限**：`internal/relay/responselimit.go` 提供 `withResponseBodyLimit`，把渠道客户端包成计数读取的 RoundTripper，上限 64 MiB（与请求解压上限一致）。`Content-Length` 超过上限在读取前直接失败；分块响应按实际字节计数，恰好 64 MiB 的正文放行。包装只用于 `sendPassthrough` 与 `sendConverted` 的非流式分支，客户端流式分支不设总字节上限；OpenCode 免费档把非流式请求折叠成上游流式时仍受同一上限约束，因为结果最终要整体落入内存。

## 备选方案

- **为口令校验单开计数表或进程内计数**：语义更独立，但多副本不共享、重启即丢；复用 `login_attempts` 表成本更低，且已有的 fail-close 语义正好符合口令校验必须写库的前提。
- **直接依赖管理 API 令牌桶**：默认关闭，且按路由划分，开启前没有任何保护；把改密接入登录限速不依赖额外开关。
- **Cookie 用 `__Host-` 前缀**：前缀要求 `Path=/`，与收窄发送范围直接冲突；且 cookie 不区分端口，同主机其它端口挂在 `/api` 的应用仍会收到它，这一点无法用 Path 消除。
- **nginx 用 `include security-headers.conf` 去重**：更 DRY，但需要镜像额外 COPY 文件并保证路径；沿用 [2026-09-21 的内联重复决定](./2026-09-21-web-nginx-csp-and-add-header-inheritance.md)，等镜像允许片段文件再收敛。
- **在 handler 里检查响应长度**：太晚，`io.ReadAll` 已经分配完整正文；必须在传输层拦截。
- **给 Response.Body 套 `io.LimitReader`**：超出上限时只会静默截断，客户端拿到坏 JSON 却以为成功；需要显式失败。
- **把流式响应也封顶**：长时间 SSE 会正常超过 64 MiB，封顶等于新增截断故障。
- **fork 或升级 axonhub 修 `io.ReadAll`**：依赖为 pseudo-version，改动面与升级风险都大；本地 RoundTripper 包装不动依赖。

## 后果

- **收益**：改密口令爆破受 15 分钟 5 次的窗口约束，多副本与重启后依然有效；认证 cookie 只在 `/api` 路径发送；分拆部署的安全头与 Go 侧一致；异常上游的大响应在 64 MiB 处失败，不再无限占用内存。
- **代价与已知上限**：改密在数据库不可用时 fail-close，这是复用登录限速的必然结果；存量 `Path=/` 的旧 cookie 会保留到浏览器过期（登出或改密轮换 JWT 密钥后其 token 立即失效，但浏览器仍会继续携带该 cookie 直到过期）；`Path=/api` 的 cookie 不区分端口；HSTS 在明文监听上被浏览器忽略，只有外层终止 TLS 并透传响应头时才生效；合法非流式响应超过 64 MiB 会被判失败，重访信号是出现真实的大响应场景；`changeUsername` 的数据库错误不再回显具体原因，排查只能看服务端日志。
- 关联实现：[请求体预算](./2026-09-22-relay-body-budget-sticky-mask-stream.md)、[Cookie Secure 与管理 API 限速](./2026-09-21-cookie-secure-behind-tls-proxy-and-admin-api-limiting.md)。

## 验证

- `internal/server/handlers/user_ratelimit_test.go`：5 次旧密码错误后第 6 次 429；登录命名空间不受影响；改密成功后计数清零。
- `internal/server/middleware/auth_test.go`：Set-Cookie 的 `Path` 等于 `AuthCookiePath`。
- `internal/relay/responselimit_test.go`：恰好等于上限放行；分块超限报 `ErrUpstreamResponseTooLarge` 且错误文本不含正文；声明长度超限时不读取正文。
- 用户执行：`go test ./internal/server/handlers ./internal/server/middleware ./internal/relay -count=1`，`go vet ./...`。
