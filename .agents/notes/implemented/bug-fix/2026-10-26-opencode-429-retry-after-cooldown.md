# Agent Note: OpenCode 免费档 429 冷却改为锚定配额重置点

Status: implemented

## 问题

[2026-10-24 统一冷却笔记](./2026-10-24-opencode-403-429-uniform-cooldown.md)把 403/429 的出口账号冷却定为固定 24 小时，并在「已知上限」里留下重访信号：上游开始返回可信 `Retry-After` 时可再评估分级冷却。

2026-10-26 在一台全新云主机（出口 IPv4 `35.93.162.163`、IPv6 `2600:1f14:34cf:7b26:ae70::3c` 两个不同 IP）上做官方客户端与 BunTransport 的同口径对抓，确认该重访信号已触发，并实测出固定 24 小时的代价：

1. **上游确实返回可信 `Retry-After`**：连续取样 `16295 → 16168 → 16150 → 15477 → 15319 → 13475 → 13412`，单调递减，与请求次数无关（不因请求续期）。
2. **`Retry-After` 锚在 UTC 零点**：取 `now_utc=20:16:28`、`retry_after=13412s`，反推到期时刻与下一个 UTC 午夜的差值 `delta_vs_midnight=0s`。即免费档是**按 UTC 天的每日配额**，`Retry-After` 就是到配额重置的剩余秒数，不是随机惩罚窗。
3. **固定 24 小时会跨过重置点**：20:16 被限 → 按 `Retry-After` 只需冷却 3.7h，实际冷却到次日 20:16，出口白白闲置约 19.7 小时（约 5 倍）。
4. **窗口跨 IP 同步**：IPv4 与 IPv6 两个不同出口 IP 取到完全相同的 `retry-after`，说明配额单元比单 IP 更粗（数据中心 ASN 段），换出口账号不换窗口——冷却时长的准确性因此直接决定出口池的有效利用率。

本次对抓同时确认形态层已无差异：ClientHello 逐字节一致（仅 random/session_id/key_share 公钥/PSK 四个随机字段不同）、握手后记录层同构（`0x14(1) → 0x17(53) → 0x17(单条合并写入)`）、头序与键序一致、工具集与 `opencode_probe_v1/v2.json` 模板一致。

## 决定

冷却时长改由配额重置点决定，不再固定 24 小时：

- 新增 `opencodeRejectCooldown(retryAfter string, now time.Time) time.Duration`（`internal/relay/opencode_proxy_pool.go`）：上游 `Retry-After` 为秒数时按该秒数冷却；缺失或非秒数时回落到「距下一个 UTC 零点」；一律钳到上限 `ocProxyRejectedCooldown`（24h，保留为上限与兜底值，不再是定值）。
- `markOpencodeProxyRejected` 增加 `retryAfter string` 参数，两个调用点各自取值：业务路径 `internal/relay/handler.go` 用新增的 `UpstreamRetryAfter(err)`（从 `*httpclient.Error.Headers` 取，已核实该结构体带 `Headers http.Header`），容量探测 `internal/relay/context_probe.go` 直接读 `response.Header`。
- 探测探针 `opencodeProxyProbe` 增加只读 `headers` 字段，用于让上游响应携带 `Retry-After`；既有用例的 `respond` 签名不变。
- 事实同步：`docs/DEVELOPMENT_routing.md` 代理账号退避条目改为「优先取 `Retry-After`，缺失回落 UTC 零点，上限 24 小时」。

本决定翻转 [2026-10-24 统一冷却笔记](./2026-10-24-opencode-403-429-uniform-cooldown.md) 中「统一冷却 24 小时」的时长口径；403/429 同类处理、换号重试、不冷却 Key 等其余语义不变，换号机制仍见 [2026-10-22 换号笔记](./2026-10-22-opencode-403-account-switch.md)。

## 备选方案

- **维持固定 24 小时** — 实现最简，且长冷却天然保证不会在窗口内空撞；否掉：实测证明 `Retry-After` 可信且锚定 UTC 零点，固定值必然跨过重置点，出口池利用率损失约 5 倍。
- **只取 `Retry-After`，缺失时仍用 24 小时** — 改动更小；否掉：上游并非所有拒绝路径都带头（本次 `/zen` 与部分 4xx 无该头），缺头时回落 24h 会重蹈跨过重置点的覆辙；既然已知配额按 UTC 天重置，缺头时直接算到零点更贴近真实可用时刻。
- **按配额日重建出口池（UTC 零点全体复活）** — 与本次同源洞察，收益是把逐账号滚动冷却换成按日批次；否掉作为本次范围：它要改池的加载与淘汰语义，超出「修冷却时长」这一处病因，留作后续（详见后果）。
- **改用上游返回的 HTTP-date 格式 `Retry-After` 解析** — 兼容性更全；否掉：实测上游恒发秒数，属 YAGNI，非秒数已由 UTC 零点回落覆盖。

## 后果

- **收益**：被 429/403 的出口在配额重置后立即回到池内，不再多闲置约 19.7 小时；上游给出精确秒数时按其冷却，比 UTC 零点推算更准（例如未来若改为非按天配额也能自适应）。
- **代价**：冷却时长不再是常量，排查时须看 `Retry-After` 与 UTC 零点两个来源；测试断言改为按同一函数计算，`TestOpencodeProxy429CoolsAccount24hAndRetriesAnother` 随之更名为 `...UntilUTCMidnightAndRetriesAnother`（旧笔记验证段引用的旧名已失效，以本篇为准）。
- **已知上限**：本次实测窗口**不**因请求续期（与 [2026-10-25 v2 调查笔记](./2026-10-25-opencode-v2-lane-investigation.md)「每次被识别的请求都会续开新窗」的表述不一致，该表述针对的是按身份窗，本条是按天配额）；若上游改为按身份窗且会续期，则「回落到 UTC 零点」会低估冷却——届时以 `Retry-After` 优先（已实现）即可自愈，不需改代码。
- **未解决**：云主机 ASN 段的每日配额本身被限，换 IP/换账号均无效，免费档仍需住宅/家宽出口（`resin` 代理通道为本仓库既有能力）。形态层已无差异，继续调形态不会改变 429 结果。
- **重访信号**：配额改为非 UTC 天粒度、上游停止返回 `Retry-After`、或出现按请求续期的窗口。

## 验证

```sh
go build ./... && go test ./internal/relay/ -run 'OpencodeProxy|OpencodeRejectCooldown' -v
```

- `TestOpencodeRejectCooldown`：边界表驱动——上游秒数、`" 3600 "` 带空白、`259200` 钳到 24h、HTTP 日期/空值/零值/负值回落到 UTC 零点。
- `TestOpencodeProxy429UsesUpstreamRetryAfter`：上游带 `Retry-After: 3600` 时坏账号冷却约 1 小时并落库，Key 不冷却，换号后成功。
- `TestOpencodeProxy429CoolsAccountUntilUTCMidnightAndRetriesAnother` 等既有用例：无 `Retry-After` 时冷却到 UTC 零点。
- 远程实测（2026-10-26，instabox）：`curl` 对 `/inference` 取回 `retry-after` 与「距 UTC 午夜秒数」差值为 0。
