# Agent Note: 登录接口请求体上限、绑定失败计数与限速落库

Status: implemented

## 问题

2026-11-10 登录入口排查确认三项缺口，均可被未认证客户端直接利用：

1. 登录接口沿用 `/api` 全局 8MiB 请求体上限。用户名与口令的 JSON 远小于 64KiB，8MiB 让畸形大包在解析前占用不必要的内存与 CPU。
2. `ShouldBindJSON` 失败只返回 400，不调用 `loginLimiter.recordFailure`。攻击者反复发送坏 JSON 或超限包即可绕过 15 分钟 5 次的失败计数，限速对这类请求完全失效。
3. `recordFailure` 与 `reset` 直接使用请求上下文。客户端断开后上下文取消，写库随之中止，失败计数落不了库；成功登录的清零也可能失败，留下陈旧失败行。

## 决定

- `internal/server/middleware/bodylimit.go` 新增 `LoginBodyLimit`，上限常量 `maxLoginBodyBytes` 为 64KiB。它只挂在 `POST /api/v1/user/login` 路由上，先于 `ShouldBindJSON` 生效。
- 全局 `BodyLimit` 保持 8MiB 不变；`/api/v1/setting/import` 继续按既有前缀判断跳过全局上限，导入上限仍为 128MiB。
- 登录绑定失败时先调用 `loginLimiter.recordFailure`，再返回 400 `resp.ErrInvalidJSON`。坏 JSON、超限包与口令错误共用同一失败计数。
- `recordFailure` 与 `reset` 在写库前执行 `context.WithoutCancel`，再叠加独立的 5 秒超时；请求取消不再阻断计数落库。
- `check` 保留请求上下文：数据库读失败时既有逻辑 fail-close 返回拒绝，取消无法换取放行；同时客户端断开可以中止这次只读查询。

## 备选方案

- **把登录上限设为 128KiB**：正常凭据 JSON 不到 1KiB，64KiB 已留足余量；128KiB 只会给攻击者更多空间，没有收益。
- **在全局 `BodyLimit` 里按路径特判登录**：会把登录策略耦合进全局中间件，容易误伤其它 `/api` 路由；路由级中间件把影响面限制在登录入口。
- **超限直接返回 413**：`MaxBytesReader` 的错误统一走绑定失败分支，现有客户端契约是 400 `ErrInvalidJSON`；改成 413 需要额外区分超限与语法错误，属于额外的对外契约变更。
- **`check` 也脱离请求取消**：取消时读库失败已经 fail-close 拒绝，不存在绕过；脱离取消反而让断开请求继续占用连接与 5 秒等待。
- **用进程内计数器替代数据库**：重启即丢、多副本不共享，无法承担跨实例的 15 分钟窗口；沿用 `login_attempts` 表。
- **手动 `io.LimitReader` 后再判长度**：与 `http.MaxBytesReader` 重复造轮子，还要自己处理读取边界与错误映射。

## 后果

- 登录请求体超过 64KiB 即绑定失败，返回 400 并计入一次失败；全局 8MiB 与导入 128MiB 的行为不变。
- 畸形 JSON 与超限包不再绕过限速：同一 IP 15 分钟内累计 5 次后，第 6 次返回 429。
- 客户端取消请求无法抹掉失败计数，也无法阻止成功登录后的清零；每次计数写库最多等待 5 秒。
- 数据库不可用时 `check` 继续 fail-close 拒绝登录，这是既有语义；新增的绑定失败计数同样受该前提约束。
- 已知上限：限速键仍是 `ClientIP`，反代未配置可信代理时所有客户端会塌成单一桶；IP 锁只在单进程内串行化；失败行依赖既有惰性清扫；超限返回 400 而非 413；若未来登录请求需要附加超过 64KiB 的字段（如验证码图片），需要重新评估上限。
- 关联：[口令校验限速与响应上限](./2026-11-01-audit-auth-cookie-headers-response-cap.md)、[登录限速单进程串行化](./2026-09-22-audit-fixes-plaintext-export.md)。

## 验证

- `internal/server/middleware/bodylimit_test.go`：恰好 64KiB 放行；64KiB+1 报 `MaxBytesError`；登录路由上限优先于全局 8MiB；其它 `/api` 路由仍接受 64KiB+1；导入路由仍接受 8MiB+1。
- `internal/server/handlers/user_test.go`：5 次坏 JSON 均 400 且落 5 行，第 6 次 429；超限 JSON 返回 400 并落 1 行；正确凭据登录成功后失败行清零。
- `internal/server/handlers/loginratelimit_test.go`：已取消上下文下两次 `recordFailure` 仍落库并触发拦截，已取消上下文下 `reset` 仍能清零。
- 用户执行：`go test ./internal/server/handlers ./internal/server/middleware -count=1`，`go vet ./...`，`pnpm verify-notes`。
