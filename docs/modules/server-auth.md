# HTTP 服务与鉴权（internal/server）

server 是唯一的 HTTP 入口：gin 路由、管理台 JWT Cookie 鉴权、`/v1` API Key 鉴权、登录限速、安全头与 SSE。

## 怎么做的

### 路由注册

`internal/server/router/` 是声明式路由注册表：handlers 经 `init()` 自注册，`RegisterAll` 统一挂载；注册未知 method 直接报错而不是静默落 GET。管理台 `/api/v1/*` 与转发 `/v1/*` 分两组，全部组显式挂中间件——结构上不存在裸路由。

### 管理台鉴权（JWT Cookie）

- HS256 算法钉死，拒绝其他 alg；`exp` / `iss` / `aud` 强制校验，登录时长 24 小时封顶（服务端固定，客户端不可配置）。
- 签名密钥 32 字节 crypto/rand 生成，以 `nv1:` 密文落 settings；**登出与改密都轮换密钥**，使全部旧 token 立即失效；改密在事务内同步轮换。
- Cookie 带 HttpOnly + SameSite=Lax；Secure 在 `cookie_secure` 配置、直接 TLS、或入站 `X-Forwarded-Proto: https` 三种情况下置位。
- 强制改密门：首登未改密的会话只放行 change-password / logout / status 三条路径，其余全部 403 并带 `X-NovaVeil-Error` 头；前端在路由树之上整体替换页面，URL 直跳无法绕过。
- 登录限速：15 分钟内失败 5 次，计数持久化在 `login_attempts` 表（多副本共享）、按 IP 互斥、DB 故障 fail-close（拒绝登录而不是放行）；文案统一且用户名不匹配也做同代价 bcrypt 比对，抹平时序与用户枚举。

### /v1 转发鉴权（API Key）

- 仅接受 `Authorization: Bearer` 或 `x-api-key`；Key 以 `nv1:` 密文落库。
- 列表接口只回掩码；明文查看走 POST + NoStore + IP 告警；RequestState 只存尾 4 位，转发链路里 `api_key_raw` 进入即打码即弃。

### 代理与安全头

- `trusted_proxies` 默认空 = 不信任任何 `X-Forwarded-For`；置于反代后须显式配置代理 CIDR / IP，登录限速才能看到真实客户端 IP。
- OriginProtection 兜底管理面写请求 CSRF；响应统一 `no-store`；安全头 / CSP 在 Go 内嵌静态链路与 nginx 链路两处分别配置。

### SSE

三个管理台流：`/log/overview/stream`（请求全链路）、`/group/runtime/stream`（分组路由运行时）、`/model-eval/queue/stream`（评估队列）；断连时 30 秒节流探活 `/user/status`，401 统一登出。

## 设计想法

- **单管理员体系**：User 表没有角色字段，管理面不区分"谁在看"，鉴权边界只区分"登录与否"——个人网关不需要 RBAC，少一张表就少一个越权面。
- **XFF 默认不信任**：直连部署下 `X-Forwarded-For` 可伪造，采信它等于允许绕过登录限速；把"信任代理"做成显式配置而不是默认行为。
- **限速 fail-close**：限速计数依赖 DB，DB 不可用时拒绝登录——可用性让位于防爆破。
- **改密与登出都轮换密钥**：JWT 无法主动吊销，那就让"吊销"变成"换锁"——单管理员场景下这是零成本的全量失效。

## 已知边界

- `X-Forwarded-Proto` 仍任意来源即影响 Cookie Secure（方向 fail-safe：只会把 Cookie 设得更严，不会放宽），是 ROADMAP 自认未修项。
- 反代后未配置 `trusted_proxies` 时，登录 / 管理限速按代理 IP 共桶，单 IP 可把全站锁死——部署在反代后必须配置该 CIDR。
- 管理 API 令牌桶限速已实现但默认关闭，暴露公网时需显式开启。

## 深入阅读

- 部署侧配套：[SECURE_DEPLOYMENT.md](../SECURE_DEPLOYMENT.md)
- 转发鉴权与渠道路由：[relay.md](relay.md)
