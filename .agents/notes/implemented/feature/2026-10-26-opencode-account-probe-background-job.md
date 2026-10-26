# Agent Note: 账号体检改为后台作业，避免反向代理按超时切断

Status: implemented

## Problem

OpenCode 账号体检是逐账号发一次真实请求：并发体检几十个账号时耗时可达分钟级。原实现是**同步**的——`POST /api/v1/channel/proxy-accounts/probe` 会等全部账号出结论才返回，前端"开始体检"与"一键测试台账"两个入口都在等这个长请求。

后果有两个：

1. 经反向代理（Cloudflare 等）访问时，超过 100 秒就可能被按超时切断（524），体检进度全部丢失。
2. 请求被切断后，前端拿不到任何结论，用户只能重跑，而重跑会再次消耗出口额度。

## Decision

1. **拆出「准备」与「执行」两段**（`internal/relay/opencode_account_probe.go`）：
   - `prepareAccountProbe` 解算渠道/模型/账号列表并做「生成即落账」，不发起任何上游请求，校验失败立即返回。
   - `runAccountProbe` 按并发执行体检；ctx 结束后不再开始新账号，未体检的账号记为 `cancelled`。
   - `ProbeOpencodeAccounts` 保持同步语义（= 准备 + 执行），24 小时自动体检任务继续使用它。
2. **新增后台作业**（`internal/relay/opencode_account_probe_job.go`）：
   - 作业上下文用 `context.Background()` 派生，**不随 HTTP 请求结束而中断**。
   - 整体时限 = 单账号超时 × 批次数，钳制在 2~30 分钟；时限到点后在途探测被取消，未开始的账号记为已取消。
   - 进程内保留最近 32 个作业，超出后淘汰最旧的。
3. **接口改为「启动 + 轮询」**：
   - `POST /api/v1/channel/proxy-accounts/probe` 立即返回 `{job_id, channel_id, model, state, total}`。
   - 新增 `GET /api/v1/channel/proxy-accounts/probe/:job_id` 返回作业状态；结束后带完整 `results` 与 `summary`。
4. **前端改为轮询**（`web-next/src/pages/ProxyAccounts.tsx`）：按钮触发后每 2 秒拉一次作业状态，"一键测试台账"不再前端分批同步等待，进度实时显示；作业被取消时给出提示。

## Alternatives considered

- **只调大前端超时** — 治不了根：反代超时不可控，且长连接占用连接数。否掉。
- **把体检拆成多个小请求分批（前端分批）** — 原前端"一键测试"已是 200 个一批，仍可能超限；且用户要的是"点了就继续在后台跑"。否掉。
- **复用 24 小时自动体检的开关当手动触发** — 它的周期是 24 小时，无法按需触发。否掉。

## Consequences

- 体检请求**立即返回**，不再受反代超时影响；作业在后台继续跑完。
- 作业超时时，未完成的账号明确记为 `cancelled`（计入失败），不会出现"跑了一半没结论"的静默状态。
- 作业不随页面关闭而停止；进程重启则中断（作业存活于内存，无落盘需求）。
- 体检结论仍逐账号落库，页面台账随时可看，不依赖作业结果。

## Verification

```sh
go test ./internal/relay/ -run TestStartOpencodeAccountProbe -v
go test ./internal/relay/ ./internal/server/...
cd web-next && pnpm typecheck && pnpm vitest run src/pages/ProxyAccounts.test.tsx
```

- `TestStartOpencodeAccountProbeRunsInBackground`：接口返回后作业仍在运行，终态可查、结论可取。
- 线上：点击"开始体检"应立即返回并进入"后台体检中…"，进度随作业推进刷新。

## 相关

- 账号体检的出站形态与免费档整体对齐见 [Bun sidecar 真发](./2026-10-26-opencode-bun-sidecar-tls-behavior.md)。
- 403/429 冷却与 `Retry-After` 口径见 [429 冷却锚定配额重置](./2026-10-26-opencode-429-retry-after-cooldown.md)。
