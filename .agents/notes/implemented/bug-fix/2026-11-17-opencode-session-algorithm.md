# Agent Note: 免费档会话号对齐官方 2.3.0 生成算法

Status: implemented

> 缓存生命周期与诊断输出边界见 [会话与密度缓存生命周期及 sidecar 诊断边界](2026-11-20-session-density-cache-lifecycle-and-sidecar-diagnostics.md)；本文的会话号编码算法继续有效。

## 问题

免费档所有请求持续返回 429 `FreeUsageLimitError`（表面是「额度耗尽」），而同一台机器上的官方 opencode cli v2.3.0 正常工作。逐项排除后定位到根因：**上游按 `x-opencode-session` 做准入校验，Nova 派生的会话号不满足官方形态**。

定位链（全程用容器内 Bun 1.4.4 直接 fetch 做对照，同 IP 同指纹）：

- 官方 body/头 + 官方旧 session → 200；同 body/头 + Nova 派生 session → 429；全新随机 session → 429。
- 顺带排除了 12 个出站头的形态（逐项一致）、`mcp_` 工具名（不带工具也 429）、project 值（透传官方值仍 429）、请求路径（改裸路径 404，证明原路径正确并回滚）。
- 一个方法论教训：capture_svc 抓包的 `path_client` 是客户端到网关的路径，此前把它当成官方到上游的路径，导致一轮错误的「裸路径」修复。

## 决定

逆向官方二进制的会话号生成函数（压缩名 Txe/YO）：

```js
let r = BigInt(Date.now()) * 0x1000n + BigInt(s);  // 真实毫秒 ×4096 + 同毫秒序号
let a = e ? ~r : r;                                // 按位取反
let c = a 的低 48 位 hex(6 字节大端);               // 12 位 hex, 随时间递减
let d = crypto.getRandomValues(new Uint8Array(b - 12)); // 14 位随机
return c + 随机段(62 字符表);                       // "ses_" 前缀
```

用两个抓包样本反推：`e17fee14effe` → 2026-11-17T05:35:49Z、`e17f6c800ffe` → 05:44:40Z，与 TUI 会话实际启动时刻精确吻合，算法确认。

关键约束：**同毫秒序号必须小于 64**。官方 seq < 256 且位 7..6 为零，使 hex 第 10 位恒为 `f`、第 11 位落在 `[c-f]` —— 这正是 Nova 代码里 `descendingTimePartPattern`（`^[0-9a-f]{9}f[c-f][0-9a-f]$`）的来源。旧派生把序号段填满 12 位哈希，第 10 位变成其他字符，上游直接 429。

`SessionIDFromSignal` 的 v2 分支重写为官方算法：

- 时间段取「信号首次解析时刻」的真实毫秒（进程内 `sessionClockStore` 缓存，缓存超 1 小时刷新），同一信号在进程内会话号稳定；重启后刷新仍保持时间新鲜。
- 序号段取哈希低 6 位（`&0x3f`），满足 `<64` 约束且确定性派生。
- 随机段保持 `base62Fixed(sum[6:16], 14)`（信号哈希派生，形态与官方 62 字符表一致）。
- v1 分支（1.19.5 已停售）同样收敛到 `<64` 约束，模板不变。

## 备选方案

- 保持天锚派生：会话号形态不满足官方校验，免费档持续 429。
- project/头/路径继续排查：已全部实证排除，收益为零。
- 每请求生成全新会话号（完全随机）：会破坏 Nova 的会话粘合（多轮请求会话号漂移），且与官方「会话内稳定」语义不符。
- 借用官方 CLI 的真实会话号：需要部署内跑官方 CLI 并同步其状态，运维成本高。

## 后果

- 免费档请求上游放行，429 消失（部署实测 200，模型正常回复）。
- 会话号的时间段真实反映请求时刻，`descendingTimePartPattern` 形态校验通过；同信号进程内稳定，重启后派生新号（上游视为新会话，Nova 全量重发 messages 无状态推理，安全）。
- 时间缓存由 `sessionClockStore` 持有摘要键，容量与并发约束见 [会话与密度缓存生命周期及 sidecar 诊断边界](2026-11-20-session-density-cache-lifecycle-and-sidecar-diagnostics.md)。
- `NOVEIL_SIDECAR_DEBUG=1` 时 Go 与 Bun 诊断日志只记录头名，Go 端另记录源站与正文长度。

## 验证

由用户执行（AGENTS 禁止 AI 运行 build/test/vet）：

```sh
go test ./internal/utils/opencodeid -v
go test ./internal/relay -run 'TestOcProbe|TestProbeOpencode' -v
go test ./...
```

线上验证：部署 `sha-a878c1b` 后发一条免费档请求，应返回 200 且模型正常回复（实测 `say ok` → `ok`）。
