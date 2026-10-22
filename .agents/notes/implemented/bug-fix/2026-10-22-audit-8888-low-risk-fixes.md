# Agent Note: 修复线上 8888 体检发现的低风险服务问题

Status: implemented

## 问题

线上 8888 实例的只读体检确认容器、镜像、SQLite 与鉴权边界正常，但发现五处由本服务代码造成的低风险缺口：Compose 只探静态首页，数据库异常时容器仍可能 healthy；启动时两个入口并发触发模型同步，固定产生“模型同步正在进行中”WARN；静态中间件对任意 HTTP 方法返回首页，连 TRACE 也得到 200；启动横幅无视 `NO_COLOR=1` 且含 Unicode 表情符号；独立部署文档把初始密码文件的删除时机写成改密成功。

## 决定

- 新增匿名只读 `GET /healthz`。它复用现有 GORM 句柄，在 2 秒超时内执行 `SELECT 1`；成功返回 `{"status":"ok"}`，失败返回 503 与 `{"status":"unavailable"}`，不返回错误正文、版本、DSN 或配置。生产 Compose 的 healthcheck 改探 `/healthz`，`GET /` 继续服务 SPA。
- 静态中间件先放行 `/healthz`、`/api` 与 `/v1`，其余路径只接受 GET 与 HEAD；其它方法返回 405 和 `Allow: GET, HEAD`。CORS 预检仍由既有全局 CORS 中间件处理。
- `SyncModelsTask` 的并发拒绝改为哨兵错误 `ErrSyncInProgress`。启动异步入口与定时任务入口都在 `errors.Is` 判定后静默跳过；其它真实错误继续 WARN。保留两个启动入口，避免改变启动同步的触发时机。
- 横幅在 `NO_COLOR` 非空或 stdout 非 TTY 时关闭 ANSI；移除火箭表情。颜色判定与写出函数拆开，便于不重定向真实 stdout 的情况下做契约测试。
- 独立部署文档改为“首次成功登录后该引导文件即自动删除”，与登录处理器在签发 JWT 前调用清理函数的实际顺序一致。

## 备选方案

- **删除 `cmd/start.go` 的启动同步入口**：能更少代码地消除这次重复，但只修掉一个碰撞点；定时器与管理员手工同步仍会撞上同一把锁并得到笼统错误。哨兵把“已在进行”编码为可识别状态，语义更准确。
- **healthcheck 继续请求 `/` 并另加业务探针**：改动更小，但容器健康状态仍无法反映数据库可用性，不能解决体检发现的就绪语义缺口。
- **静态中间件仅对 OPTIONS 放行**：可覆盖部分预检，但全局 CORS 已处理合法预检；额外放行会让普通 OPTIONS 误入静态层，扩大方法边界。
- **横幅只在 `NO_COLOR` 非空时禁用颜色**：容器场景够用，但管道、重定向和日志采集同样不应写入 ANSI；因此同时判断 TTY。

## 后果

- **收益**：数据库不可用时 Compose 能通过 503 识别不健康；静态路径不再响应写方法与 TRACE；并发同步不再制造固定 WARN，真实失败仍有日志；非交互输出保持纯文本且无表情；部署文档与首次登录清理行为一致。
- **代价与已知上限**：`/healthz` 只证明数据库可执行最小查询，不证明渠道、Key 或上游可用；它也不进入 SPA 回退，因此必须由后端路由注册。生产 Compose 改动只有在重新 `docker compose up -d` 后才会进入运行中的容器。

## 验证

- `GOTOOLCHAIN=go1.26.7 go test -count=1 ./internal/conf ./internal/task ./internal/server/middleware ./internal/server/handlers` 覆盖健康端点、静态方法契约、同步哨兵与横幅输出。
- 临时进程手工验证 `/healthz` 返回 200、`POST /` 返回 405、`NO_COLOR=1` 的启动横幅不含 `\x1b[`。
- `npx -y tsx scripts/agent-notes/verify-agent-note-tree.ts` 与 `npx -y tsx scripts/agent-notes/verify-agent-note-format.ts` 校验笔记树与格式。
