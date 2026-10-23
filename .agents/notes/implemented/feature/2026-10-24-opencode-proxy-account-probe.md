# Agent Note: OpenCode 代理账号批量体检（独立页面 + 并发钉号探测 + 结果自动入池）

Status: implemented

## Problem

免费档渠道按出口 IP 轮换：`channel_proxy` 含 `{account}` 占位符，每个账号对应 resin 的一个出口会话。池的现有学习方式是「流量驱动」——转发时挑号、成功/被拒回填，一次只学一个，且冷启动会撞已 ban 的出口。运营侧需要一次性生成 N 个账号、并发探测，把健康的先填进池子，后续转发直接优先复用。

## Decision

### 钉号探测（`internal/relay/context_probe.go`）

`probeOnce` 抽出 `probeOnceWithAccount(ctx, channel, modelName, tokens, account)`：account 为空沿用池内选号（原行为不变），非空时强制用该账号探测。拿到 HTTP 响应时结论（Status/Detail/Latency）随 result 返回而非丢弃——`probeOnce` 包装层保持旧契约（err 时 result=nil），`ProbeContextWindow` 调用方无感知。成功/403/429 的池回填逻辑原样复用：**体检结果与转发共享同一套池状态**。

### 批量体检（`internal/relay/opencode_account_probe.go`）

`ProbeOpencodeAccounts(ctx, channelID, accounts, count, concurrency, timeoutMS)`：渠道须为含 `{account}` 代理的免费档（`proxyNeedsOpencodeAccount` 同口径）；accounts 空时生成随机账号（复用 `opencodeXXXX` 生成器），非空时去重并校验字符集（`^[A-Za-z0-9_-]{1,64}$`，账号要拼进代理 userinfo）；worker 池并发（默认 10，上限 20）各打一次 16 token 极小真实请求（输出预算已被探测路径收敛为 1）。结论三分类：healthy（2xx）/ rejected（403/429）/ error（其余，含代理与网络故障，不入池不冷却）。单次上限 200 个、单账号超时上限 120s。

### API 与页面

`POST /api/v1/channel/proxy-accounts/probe`（channel.go，同步返回）。前端新增「账号体检」页（`/proxy-accounts`）：渠道选择只列启用中且代理含占位符的免费档渠道；数量/并发输入 + 可选自定义账号列表（粘贴即按列表探测，数量输入失效）；结果表逐账号展示结论/HTTP/延迟/响应摘要（密钥已脱敏）。

## Alternatives considered

- **只测代理连通性（GET /models 或 TCP 连通）** — 不打真实对话请求就测不出出口被 ban/限流，而账号健康度恰恰取决于此；否掉。
- **后台常驻体检任务（定时自动补池）** — 现阶段运营是手动触发场景，常驻任务引入调度与额度消耗治理，YAGNI；否掉，重访信号是需要无人值守补池时。
- **体检结果写独立表** — 池表（`opencode_proxy_accounts`）已承载 verified/cooldown 语义，独立表会造成双源；否掉，直接复用回填。

## Consequences

- **收益**：冷启动不再逐个试错；N 个账号一次并发体检后健康号即时可被转发优先复用；被拒号立即进入 24h 冷却，转发路径不会撞它们。
- **代价与已知上限**：每账号消耗一次极小真实免费请求（16 token 输入 + 1 输出）；同步接口在 200 账号 × 慢上游时页面需等待（上限约 200×120s/并发，正常 100×~1.5s/10 并发 ≈ 15s）；探测与上下文探测共用请求日志面板，大批量体检会产生同量级日志条目。
- **重访信号**：需要定时自动补池；或免费档上游对探测类小请求出现区别对待。

## Verification

```sh
go build ./... && go vet ./... && go test ./internal/relay/ -run TestProbeOpencodeAccounts
cd web-next && pnpm typecheck && pnpm lint && pnpm test -- ProxyAccounts
```

- `TestProbeOpencodeAccountsClassifiesAndSeedsPool`：假 CONNECT 代理按账号返回 200/403/502，断言三分类与池状态（verified / 冷却 / 不入池）。
- `TestProbeOpencodeAccountsGenerateAndValidate`：生成模式（去重、`opencode` 前缀格式）、非法字符拒绝、超上限拒绝、非 `{account}` 渠道拒绝。
- 前端四用例：渠道过滤、默认参数与结果表、自定义账号列表、空态。
