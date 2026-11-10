# Agent Note: OpenCode 免费档直连渠道 429 后停用至 UTC 零点

Status: implemented

## 问题

OpenCode 免费档的额度按出口身份在 UTC 日窗口内结算，429 表示该出口当日额度已耗尽或已被上游打标。直连渠道没有代理账号池可轮换：收到 429 后继续留在候选池里，只会被同一个上游反复拒绝，把请求轮次和整体时间预算烧在必然失败的成员上。

现有机制覆盖不到这类渠道。Key 冷却是按分钟的临时退避，单 Key 直连渠道冷却结束后立刻再撞；WARP 渠道的[内存停用到 UTC 零点](2026-11-02-warp-stuck-egress-utc-block.md)只对 `isWarpProxy` 生效，且不写库、不改 `Enabled`，重启即丢。需要一条明确的渠道级规则：直连免费档收到 429 就整体停用，等到跨过 UTC 零点额度重置后再自动恢复。

## 决定

1. **模型字段**：`model.Channel` 增加 `AutoDisabledUntil *time.Time`（列 `auto_disabled_until`，无默认值）。非空表示该渠道由 429 自动停用，值即恢复时刻；`Enabled` 同时为 false。字段随渠道接口返回，前端可据此展示原因，不必改接口契约。
2. **触发条件**：只在转发主循环的真实失败分支判定，要求 `outcome == AttemptFailed`、渠道满足 `isOpencodeFreeLane`（`OpencodeCompat && OpenCodeTier=="zen"`）、`Proxy == false`，且错误状态码为 429。keyed 免费渠道默认不带代理，同样适用；走 `{account}` 代理账号池的渠道不适用，它们仍按账号退避处理。判定放在多 Key 轮换之前：命中即整渠道停用，不冷却单把 Key，避免换 Key 继续打同一出口。
3. **停用动作**：`op.ChannelAutoDisable` 用一次事务写入 `enabled=false` 与下一个 UTC 零点的 `auto_disabled_until`，提交后在渠道写锁内从库读回并发布渠道缓存。转发侧把当前成员放入 `exclude` 后继续故障转移，不等同成员重试；Key 冷却与限流状态不做额外清理，与人工停用口径一致。
4. **自动恢复**：任务 `channel_auto_enable` 每分钟执行一次并在启动时先跑一遍，`op.ChannelAutoEnableExpired` 查出 `enabled=false` 且截止时刻已到的渠道，逐条置回 `enabled=true`、清空标记，并在同一把渠道写锁内从库读回后发布缓存。停机期间到期的渠道由启动那一轮补上。
5. **人工接管**：`ChannelEnabled` 无论启用还是停用都清空标记；`ChannelUpdate` 只在提交的 `enabled` 与原值不同时清空，避免全量编辑器原样保存就把自动停用状态带走。清空后渠道完全由人工状态决定，不再被定时任务触碰。
6. **不改既有 WARP 停用**：`warp_block.go` 的内存停用继续服务 WARP 代理渠道，两条规则按渠道类型互斥；本轮不合并，避免改变已在生产验证的 WARP 语义。

## 备选方案

- **复用 `blockWarpChannelUntilUTC` 的内存停用表**：实现最小，到点惰性清理，也不需要定时任务。但重启会丢失停用状态，面板只看到渠道仍在启用，与「自动禁用和启用」的运维预期不符；且该表语义绑定 WARP，直连渠道复用它会让两条规则混在一起。否掉。
- **只冷却当前 Key（`markChannelKeyCooldown`）**：直连免费渠道通常只有一把 Key，冷却几十秒后仍会命中同一出口；429 是出口/身份级结论，不是单凭据问题。否掉。
- **固定停用 N 小时或按 `Retry-After`**：上游免费额度按 UTC 日重置，固定时长可能跨不过重置点（继续 429）或白等到重置后（浪费可用时间）；`Retry-After` 在部分响应里缺失或被上游异常值污染。否掉。
- **在探测/测试路径也触发停用**：管理端测试与后台体检是诊断行为，不应因一次诊断请求改变生产渠道状态。触发点只放在真实转发。否掉。
- **只在 `channelDisabledForRouting` 加内存判断，不改 `Enabled`**：改动最小，但渠道列表仍显示启用，分组之外看不到停用原因，且重启后重新参与选路。与「渠道禁用」语义不符。否掉。
- **降级为成员级冷却（`RouteState.Cooldowns`）**：冷却按成员记录，同一渠道在多个分组、多个模型上是不同成员，无法表达整渠道停用；而且全组冷却会被 `allMembersInCooldown` 当成可自动清除并立刻重试。否掉。

## 后果

- 收益：直连免费档 429 后立即退出所有分组与手动模式的选路，避免 429 循环；停用状态跨重启保持；UTC 零点后一分钟内自动恢复，无需人工。
- 代价：429 路径增加一次同步数据库写入，超时上限 5 秒；只在真实失败分支执行，不在正常转发热路径上。
- 已知边界：判定只看 HTTP 状态码，不区分「当日额度耗尽」与「请求形态被惩罚」，两者都会停用到 UTC 零点；需要排查形态问题时先看渠道字段与日志，不要在窗口内反复手动启用。
- 若同一时间所有直连免费档渠道都被停用，分组会暂时没有可用成员，请求按既有语义等待重扫而不是立即报错；这是渠道级停用的预期结果。
- 面板本轮只显示渠道已停用，尚不展示「自动停用至某时刻」的文案；字段已随接口返回，后续补前端展示不需要再改后端。

## 验证

```sh
cd D:\AI\github\kingsun\NovaVeil
go test ./internal/op -run 'TestNextUTCZero|TestChannelAutoDisable|TestChannelEnabledClearsAutoDisable' -v -count=1
```

```sh
cd D:\AI\github\kingsun\NovaVeil
pnpm verify-notes
```
