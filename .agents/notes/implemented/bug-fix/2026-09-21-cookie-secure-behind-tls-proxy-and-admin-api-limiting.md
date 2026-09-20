# Agent Note: 反代 TLS 下 Cookie Secure、配置校验与管理 API 限速

Status: implemented

## 问题

2026-09-20 后续审计：

1. **SEC-02**：`SetAuthCookie` 只在 `c.Request.TLS != nil` 或 `cookie_secure` 配置为 true 时给 auth cookie 加 `Secure`。TLS 在反向代理终止时 `c.Request.TLS == nil`，`cookie_secure` 默认 false，auth cookie 会在 HTTP 通道里往返。
2. **OLD-25**：配置反序列化后没有 host/port/path 校验，坏配置到运行期才以不可读错误暴露。
3. **OLD-26**：管理 API 只有登录接口限速，其他已认证接口没有基本速率保护。

## 决定

- **SEC-02**：`SetAuthCookie` 增加判断 `X-Forwarded-Proto: https`（大小写不敏感）：当反代已经终止 TLS 并传入该头时，cookie 带 `Secure`。该决定只把 cookie 设得更严格，不依赖 `XFF` 展开。
- **OLD-25**：`conf.Config.Validate()` 在 `Load` 末尾强制校验：`server.host` 非空、`server.port` 在 1-65535、`database.path` 非空且不含 NUL；启用管理 API 限速时阈值必须为正。新增 `internal/conf/config_test.go`。
- **OLD-26**：新增 `internal/server/middleware/admin_ratelimit.go`，对 `/api/v1` 已认证非登录接口做内存令牌桶限速（按 客户端IP+路由 维度）。默认 `security.admin_api_rate_limit_enabled=false`，启用时可配 `security.admin_api_rate_limit_per_minute`（默认 120）与 `security.admin_api_rate_limit_burst`（默认 30）。登录路由在鉴权前，不受此限。

## 备选方案

- **依赖公开 `Secure` 配置位修复 SEC-02**：需要每个反代部署记得开 `cookie_secure`，默认不安全；选择同时识别反代头。
- **管理 API 直接接入登录失败限速器**：语义不同（登录失败限速按 IP 且是失败维度），且需要鉴权后才生效；选择独立小型令牌桶。

## 后果

- **收益**：反代部署默认不泄漏 auth cookie；坏配置 fail-fast；管理 API 有了可配置的基础速率保护。
- **代价与已知上限**：`X-Forwarded-Proto` 可被直连客户端伪造，但伪造的后果仅是把 cookie 设成 Secure。内存限速为单实例进程内限速，多实例部署需要共用令牌桶存储（重访信号：出现多实例后端 + 同一反代 IP 放大请加 Redis）。

## 验证

- `internal/server/middleware/admin_ratelimit_test.go`：启用时超阈值 429，禁用时直通。
- `internal/server/middleware/auth_test.go`：`X-Forwarded-Proto: https` 时 cookie 含 Secure。
- `internal/conf/config_test.go`：非法 port/host/path/限速参数返回错误。
- 全量 `go build ./...`、`go test ./...`、`go vet ./...` 通过。