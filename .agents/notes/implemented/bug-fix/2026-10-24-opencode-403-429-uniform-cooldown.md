# Agent Note: OpenCode 免费档 403 与 429 统一冷却 24 小时

Status: implemented

## 问题

此前 403 的出口账号冷却按正文二分类（2026-10-23 e0e176f 引入，当时未立笔记）：正文含 "abusive traffic" 等滥用特征的冷却 15 分钟（`markOpencodeProxyAbuseBlock`），形状门禁 403（FreeTierError）完全不冷却出口；另有通用 403 的 12 小时常量（`ocProxyCooldown`，`markOpencodeProxyForbidden`）在换号钉扎移除后已无调用方。429 则冷却 24 小时。

实际运行中 403 基本都是出口 IP 被上游标记，短冷却或不冷却会让被拒出口留在池里反复撞 403；三档时长（15min/12h/24h）并存也增加心智负担。用户拍板：403 冷却与 429 保持一致，报错即冷却 24 小时，冷却账号记录到本地。

## 决定

- 403 与 429 视为同一类出口拒绝：业务路径（`handler.go`）与容量探测（`context_probe.go`）在本轮使用了代理账号且上游状态码为 403/429 时，统一调用 `markOpencodeProxyRejected`，冷却 `ocProxyRejectedCooldown = 24h`。探测路径仍单发、不换号。
- 冷却账号记录到本地沿用既有机制：`markOpencodeProxyCooldown` 异步 upsert 进 `opencode_proxy_accounts` 表（`cooldown_until`），进程重启后由 `loadOpencodeProxyPool` 恢复；403 现在也走该路径，形状门禁 403 不再漏记。
- 撤销 403 二分类：删除 `isOpencodeNetworkAbuse` / `isOpencodeNetworkAbuseText` / `markOpencodeProxyAbuseBlock` / `markOpencodeProxyForbidden` 与 12h、15min 常量，只留一个拒绝口径。
- 其余语义不变：同成员换号预算 `ocProxyMaxRetries = 3`、多 Key 渠道不冷却 Key、不记成员失败、不设 `exclude`、不清会话粘合；非 OpenCode 渠道的 429 仍按 Key 限额语义冷却 Key。
- 事实同步：`docs/DEVELOPMENT_routing.md` 代理账号退避条目改为统一 24 小时，并修正钉扎移除后遗留的「耗尽后固定复用最后账号」表述。

本决定翻转 [OpenCode 免费档 429 冷却代理账号 24 小时并换号](2026-10-22-opencode-429-account-cooldown.md) 中「403 12h / 429 24h 差异化」的口径；换号机制本身见 [OpenCode 免费档 403 时直接换代理账号重试](2026-10-22-opencode-403-account-switch.md)，池与落库规则见 [按出口 IP 轮换代理账号池](../feature/2026-10-20-opencode-free-proxy-ip-pool.md)，冷却时长以本篇为准。

## 备选方案

- **保留滥用 403 的 15 分钟短冷却**：滥用封禁通常换个出口即解，短冷却可让出口尽快回归；否掉是因为滥用特征串无法稳定区分「滥用封禁」与「出口被标记」，误判一次就把坏出口放回池里继续撞 403，且用户明确要求报错即 24 小时。
- **形状门禁 403 维持不冷却**：整形缺陷导致的 403 冷却出口确实冤枉；但出口被标记与整形回归在网关侧无法可靠区分，不冷却会让被标记出口无限期留在池内，统一冷却是更安全的默认。
- **403 用 12 小时、429 用 24 小时**：旧差异化口径；否掉是因为用户要求两者一致，双时长还要维护两套常量与分支。

## 后果

- **收益**：被拒出口 24 小时内不再参与轮换，坏 IP 不会反复触发 403；冷却单一口径，403 全部落库、跨重启保留。
- **代价与已知上限**：若 403 实为整形门禁误报（网关自身缺陷），出口会被冤枉冷却 24 小时，误报成本高于旧口径；仅被短暂限流的出口也要等满 24 小时。重访信号：上游开始返回可信 `Retry-After`，或 403 正文能稳定区分封禁与形状拒绝时，可再评估分级冷却。

## 验证

- `TestOpencodeProxy403RetriesAnotherAccount`：坏账号 403 后冷却约 24 小时、落库（`assertOpencodeProxyPersistedCooldown`）、换号成功、Key 与成员状态未被污染。
- `TestOpencodeProxy429CoolsAccount24hAndRetriesAnother` 与 `TestOpencodeProxy429MultiKeySwitchesAccountWithoutKeyCooldown`：429 同为 `ocProxyRejectedCooldown` 24 小时并落库。
- `go test -count=1 ./internal/relay/...` 与 `go test -count=1 -race ./internal/relay`
