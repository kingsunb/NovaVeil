# Agent Note: WARP 同出口连续 429 停用渠道至 UTC 零点

Status: implemented

## 问题

免费 WARP 的 IPv4 出口是 Cloudflare NAT 后的共享地址，重新注册与换槽位通常仍落回同一个公网 IP。NovaVeil 收到 429 后按[单次 429 单槽轮换](2026-11-02-warp-single-429-rotation.md)淘汰该槽位，但 WARP 侧没有告诉上游“换了几次还是同一个出口”。只要槽位快照刷新后又出现同一地址，请求就会继续打向这个渠道，形成 429 循环并持续消耗出口配额。

需要一条跨仓契约：WARP 在连续 429 惩罚仍指向同一出口时明确返回“无新出口”，NovaVeil 据此把该 WARP 渠道从选路中停用到下一个 UTC 零点，等免费额度重置后再自动恢复。

## 决定

### WARP 侧：连续同出口计数

- `Pool` 用 `penaltyStrikes map[string]int` 记录每个出口被 429 惩罚的连续次数，键是完整出口地址。
- `PenalizeByEgress(addr, reason)` 仅在找到在岗出口、`addr` 非空且 `reason == "429"` 时递增计数；403、网络失败和巡检淘汰不计数。
- `placeRotated` 在新旧出口地址不同时删除旧地址的计数。真正的出口变化即中断连续段。
- 常量 `sameEgressPenaltyLimit` 为 10。达到后 `PenaltyState` 返回 `hit=true`。
- 达到阈值时记录下一个 UTC 零点；`PenaltyState` 跨过该点后清零计数，避免 UTC 后首次 429 立即再次停用。
- `POST /penalize` 响应增加两个字段，旧客户端可忽略：

```json
{"penalized":1,"strikes":10,"no_new_egress":true}
```

- `no_new_egress` 只在 `reason == "429"` 且 `strikes >= 10` 时为真；无参数淘汰最近出口的旧路径同样带 `reason=429`。

### NovaVeil 侧：渠道级停用

- `warpPenalizeEgress` 与 `warpPenalizeLatest` 把渠道 ID 传给控制请求；异步响应解析 `no_new_egress`。
- 命中后调用 `blockWarpChannelUntilUTC(channelID)`，在进程内存中记下截止时间：下一个 UTC 零点，取当前记录与本次的较晚值。
- `warpChannelBlocked(channelID)` 在读路径惰性清理过期项；到期后自动恢复，不写数据库、不改渠道 `Enabled`。
- `channelDisabledForRouting` 先看渠道是否被管理端禁用，再对 `isWarpProxy` 渠道查停用表。被停用的 WARP 渠道在所有分组、手动模式和自动模式中都无法被选中。
- `allMembersInCooldown` 沿用既有口径，把停用渠道排除在分母外；全组只剩被停用渠道时不会触发自动清冷却。
- 槽位租约与待轮换记录携带 `channelID`，在 `finishPending`、`MarkNetworkFailure` 和旧的非槽位 WARP 路径都能把结果归到具体渠道。

## 备选方案

- **NovaVeil 自己比较 `/slots` 的出口地址来计数**：能避开跨仓契约，但 `/slots` 有 1 秒缓存，轮换期间会看到旧地址；还无法区分“轮换失败”和“轮换成功但地址没变”，计数会误判。WARP 侧掌握真实轮换结果，判定更可靠。
- **命中后直接写渠道 `Enabled=false`**：状态持久、面板可见，但需要数据库写入，且 UTC 零点不会自动恢复，必须再引入定时任务或人工操作；与“临时停用”语义不符。
- **第一次 429 就停用到 UTC 零点**：实现最简单，但 IPv6 或不同段出口经常一次轮换就恢复，过早停用会白白损失可用渠道。
- **复用 `RouteState.Cooldowns`**：冷却按成员 ID 记录并自动到期，不能表达“整个渠道停到 UTC 零点”；而且 `allMembersInCooldown` 会把全组冷却误判为可自动清除。
- **只记录日志不返回信号**：排障可见，但 NovaVeil 仍会继续打同一个出口，429 循环没有终止条件。

## 后果

- 收益：连续 10 次 429 惩罚仍指向同一出口时，渠道立即退出选路，避免继续消耗已耗尽的出口配额；UTC 零点后无需人工操作即可恢复。
- 代价与已知上限：停用表只在内存，NovaVeil 重启会丢失提前停用状态；WARP 的连续计数也在内存，WARP 重启后重新计数。若所有可用渠道都被停用，分组会暂时无成员可路由，这是预期结果。
- 计数口径是“连续 429 惩罚”，每次惩罚都会触发一次轮换；真实换到不同出口时计数清零。轮换预算耗尽、长时间保留旧出口时，计数会继续累加并最终停用渠道。
- 该改动只处理渠道级停用，不改变免费 WARP 的 IPv4 共享出口事实；需要唯一 IPv4 出口仍应使用付费 Zero Trust 专用出口。

## 验证

```sh
cd D:\AI\github\kingsun\NovaVeil
go test ./internal/relay -run 'TestWarpChannelBlock|TestWarpSlot|TestSessionStickyInvalidatedByWarpBlock' -v -count=1
```

```sh
cd D:\AI\github\kingsun\NovaVeil_WARP-main
go test ./internal/warp -run 'TestPenaltyState' -v -count=1
```

```sh
cd D:\AI\github\kingsun\NovaVeil
pnpm verify-notes
```
