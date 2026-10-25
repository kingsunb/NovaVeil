# Agent Note: opencode v2 档(/inference)风控调查与 session ticket 对齐

Status: implemented

## Problem

v1 档修复(系统提示门禁+会话号时间段, 见 [前一篇](./2026-10-24-opencode-free-system-prompt-and-session-gate.md))后, v2 档仍全量 429 FreeUsageLimitError。对照实验把问题钉死为纯客户端差异: 同一台服务器 IP 直连, 官方 opencode 2.1.4 CLI 三账号连续 200, NovaVeil(含官方逐字节体检模板)429——排除出口 IP、额度、请求形态后, 差异只剩 NovaVeil 的 uTLS 出站与真 Bun(BoringSSL)客户端本身。

字节级对照(本地 TLS 终结观察端分别终结官方 CLI 与 NovaVeil BunTransport 的 chat 连接)给出两个事实:

1. **官方 chat 连接的 ClientHello 与 NovaVeil 仅差一个 PSK 扩展**(0x29, 148 字节 body): 官方客户端进程内复用 session ticket, 第二次连接起带 PSK; 官方 bootstrap 连接(拉 models 目录)同样不带 PSK 且能 200, 说明 chat 端点与 bootstrap 端点风控口径不同。此前记录中"首扩展 ID 0x56a/0x602"为解析错位乌龙, 实为扩展区总长字段, 双方扩展序列逐字节一致。
2. **官方系统提示无模板漂移**: 官方 2.1.4 实发 body 比我们模板多的约 380 字节, 全部来自客户端把运行环境 AGENTS.md 注入系统提示与工作目录描述, 属机器本地差异, 上游不可能逐字节校验。

## Decision

落地 session ticket 对齐(`internal/client/bun_dial.go`): uTLS Config 增加进程级 `bunSessionCache`(`utls.NewLRUClientSessionCache(64)`), `bunClientHello` 的扩展列表末尾加 `UtlsPreSharedKeyExtension` 并设 `OmitEmptyPsk`——首连无 session 时 PSK 扩展省略(与官方首连同构), 同进程第二次连接起携带 PSK(与官方 chat 连接同构, 1654 字节)。上游对我们会话正常下发 ticket(models 端点实测拿到), 缓存生效已由本地观察端验证。

## Alternatives considered(已穷尽的 v2 排除清单)

以下维度全部实测对齐或证伪, 后续排查不必重复:

- **TLS ClientHello**: 扩展序列/签名算法(含 Ed25519+P-521)/曲线(X25519MLKEM768 首组)/ALPN(仅 http/1.1)/版本/压缩, 与官方 chat 连接逐字节一致(仅 PSK 一项之差, 已对齐)。
- **HTTP 层**: 请求行 `/inference/openai/v1/chat/completions`、头集合与顺序、Accept/UA/Accept-Encoding, 与官方实抓一致(10-22 抓包+本次 443 终结实抓双源)。
- **body**: 官方逐字节 v2 模板(系统提示+全量工具+键序)。
- **x-opencode-project**: 固定 40-hex 与每请求随机 40-hex 均仍 429, 排除。
- **bootstrap 先行模式**: 先 GET models(200)再 POST chat, 仍 429, 排除。
- **带 PSK 的 chat**: ticket 缓存生效后第二连接起带 PSK, 仍 429, 排除 PSK 为唯一开关。
- **额度/IP**: 官方 CLI 同 IP 200, 排除。

## Consequences

- **收益**: NovaVeil 的 TLS 会话形态与官方客户端在全部可观测维度一致; PSK 恢复让连接握手成本下降(免完整握手); golden 测试锁定首连形态。
- **未攻破的残余差异**: v2 chat 端点仍对 NovaVeil 429, 差异沉在 uTLS 与 BoringSSL 的 TLS1.3 握手实现深处(HelloRetryRequest 处理、记录层分片、Finished 时序等不可静态观测的行为), 或上游另有未识别维度。**重访信号**: 上游政策变化、opencode CLI 版本更新、或出现新的可观测差异线索时, 优先做完整握手的字节级 diff(pcap 级), 而非重复上述已穷尽清单。
**方法论教训(2026-10-25 复盘, 已固化进 AGENTS.md)**: 本轮「已穷尽」结论是在身份被打标状态下得出的——官方模板实验注定发现不了「客户端自定义工具」维度(模板本无客户端工具), 而身份窗让所有形态实验拿不到干净信号; 工具污染可能是触发打标的因, 身份窗是果, 把果当成主要障碍导致形态排查提前收敛。后续口径: 对齐以官方最新客户端实抓为准(opencode2api 仅作参考), 被惩罚状态下的阴性结果不作排除证据。
- **惩罚窗口终局数据(2026-10-25)**: 窗口过期后单发仍 429 且立即新开满窗(Retry-After 59800s ≈ 16.6h), 两个互不相同的新出口 IP 的窗口同步起算——惩罚按「客户端身份」跨 IP 开窗且无过期豁免, 每次被识别的请求都会续开新窗。运营含义: v2 档 429 时换号重试无效(身份同窗且白白烧新出口), 429 响应的 Retry-After(实测 6.7h~16.6h)应作为渠道级冷却依据; 上游拒绝话术统一为 FreeUsageLimitError, 与真实 IP 额度耗尽不可从响应体区分, 只能靠 Retry-After 头(额度耗尽可能不带此头)与真客户端对照分辨。
- **运维口径**: v2 渠道维持禁用, 每隔数日用量化体检放哨; 2026-10-25 已按 opencode2api 当日更新落地 v2 工具改名与天锚两项形态对齐(见 [工具别名笔记](../feature/2026-10-25-opencode-v2-tool-alias-and-day-anchor.md)), 形态层可对齐项已全部到位; v1(/zen)为当前唯一免费档主通道。

## Verification

```sh
go build ./... && go test ./internal/client/
```

- `TestBunClientHello*`: 首连 ClientHello golden(无 PSK 形态, `OmitEmptyPsk` 口径与 dial 一致)。
- 2026-10-25 远程实测: 同进程第二连接起 ClientHello 为 1654 字节含 PSK, 与官方 chat 连接同构; 上游 models 端点对我们正常下发 ticket。
