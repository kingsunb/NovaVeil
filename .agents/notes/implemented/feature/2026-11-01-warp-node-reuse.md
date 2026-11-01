# Agent Note: WARP 出口成败回报节点库

Status: implemented

## 问题

WARP 侧的轮换没有记忆：一次 429 就把隧道退役、重新注册，而新注册的 `/128` 大概率落回同一个 `/64`，配额作用域没变。今天退役的段明天还要靠再注册去碰运气，注册次数与实际可用段数被绑死，池子在「看起来一直在换、实际没扩容」之间来回抖。

判断「这个出口真实可用」的唯一可信信号不在 WARP 侧：代理层是纯字节转发，OpenCode 走 CONNECT + TLS，隧道里的 429/403 它看不见（同 [WARP 出口槽位账号化](2026-10-30-warp-slot-account-and-429-quorum.md) 的归因结论）。信号在 NovaVeil 手上，所以要由 NovaVeil 回报。

## 决定

NovaVeil 在槽位分支上向 WARP 控制 API 追加两条**只记账、不影响本次请求**的异步回报：

| 端点 | 触发点 | 语义 |
|---|---|---|
| `POST /nodes/success?egress=<完整出口IP>` | `warpSlotLease.MarkSuccess`，即上游 2xx | 该出口真实成功过，`Success++`、`LastOKDay=今天` |
| `POST /nodes/fail?egress=<完整出口IP>&reason=429` | `warpSlotLease.MarkRateLimited`，即单槽 429 | 该出口今天失败过，当天不再被选号 |

WARP 侧对应新增节点库（`WARP_NODE_DB`，JSON 落盘、按配额段去重、按 `WARP_NODE_TZ` 日界），本仓只依赖上面两条契约，不读它的文件。

### 契约与不变量

- 成功回报按出口去重：同一 `egress` 10 分钟内最多一次（`warpNodeSuccessWindow`），记录上限 256 条并随窗口清理。`Success` 是「这个出口成功过的采样次数」，只用于第二天排序，不是请求数。
- 429 回报不去重：429 本身不频繁，每次都记。
- 两条都由 `warpPostControlAsync` 发出：非阻塞、3 秒超时、失败静默、带 recover。
- 上报路径不在 `warpSlotPool.mu` 内发网络请求：先解锁，再调用。
- `egress`、`pool`、`controlBase` 任一为空即跳过，测试里手写的 pool 不会真的发请求。
- 回报不改变本进程的选槽、suspect、quorum 与 `RotateAll` 判定：本轮只增加事实记录，不改变轮换策略。
- 旧版 WARP 镜像没有这两个端点，返回 404 被静默吞掉，行为退化为改动前。

## 备选方案

- **WARP 侧按连接存活时长推断成功** — 不选：长连接不代表上游 2xx，403 与 5xx 也会被记成成功，反而污染第二天的排序。
- **WARP 侧解析 CONNECT 内的状态码** — 不选：TLS 密文不可见，这是代理层架构决定的，不是补一个调用点能解决的。
- **每个成功请求都回报一次** — 不选：每请求一次控制 API，噪声和开销都不值；10 分钟窗口对「排序用的成败计数」足够。
- **把成功计数落 NovaVeil 自己的库** — 不选：节点凭据在 WARP 侧，跨进程重启后 NovaVeil 无法把计数和节点对上；拥有注册的一方持有台账才不会漂移。
- **单槽 429 同时 `/penalize` 换出口** — 暂时不选：现有语义是本地 suspect 60 秒 + 两个不同 `/64` 才全池换桶，单槽即时换会把一次误判放大成轮换。先只记账，等 `/status` 的 `Prefixes` 与 `Rotation` 攒一天数据再决定是否改成单槽即时轮换。

## 后果

- **收益**：WARP 节点库拿到真实成败，第二天（美东 0 点后）优先复用昨天成功过的段；当天 429 的段当天不再被选中。轮换不再是「必然重新注册」，撞段候选的注册也能留到以后用。
- **代价**：NovaVeil 多两条出站调用（异步、成功去重 10 分钟）；WARP 侧多一个含私钥与设备 Token 的落盘文件，权限 `0600`、依赖挂载卷的属主。
- **计数口径**：`Success` 是采样不是总量，不能当成功率用；真实失败率要看 `/nodes` 的 `fail` 与 `last_fail_day`。
- **单向依赖**：本仓不改 WARP 就发不出有效回报（404 静默），节点库会退化成「只有当天失败记录」。升级顺序是先 WARP 后 NovaVeil，回滚顺序相反。
- **验证边界**：本轮只做静态修改，未执行 `go build`、`go test`、`go vet` 或启动容器。

## 验证

```sh
# WARP 仓
cd D:/AI/github/kingsun/NovaVeil_WARP-main
go test ./...
go vet ./...

# NovaVeil
cd D:/AI/github/kingsun/NovaVeil
go test ./internal/relay -run 'Warp'
go vet ./internal/relay

# 门禁
pnpm verify-notes
```

容器联调：`curl http://127.0.0.1:1081/nodes` 应返回 `enabled=true` 与脱敏节点列表；发一个成功请求后 `Success` 在 10 分钟窗口内只加一次；`POST /nodes/fail?egress=...&reason=429` 后该段 `available` 减少、`failed_today` 加一。
