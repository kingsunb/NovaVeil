# Agent Note: OpenCode 代理账号 429 计票冷却与会话级去重

Status: implemented

## 问题

代理账号池此前 403 与 429 单次即冷却账号，一个出口被限流后会直接闲置到 UTC 零点；同时选号不带会话维度，同一会话换号重试可能重复打同一个账号。WARP 槽位已经有「三个不同 /64 才全槽冷却」，但同样没有会话级去重。

## 决定

- 429 改计票制：`ocProxyPool.rateLimited` 按会话计票，10 分钟窗口内三个不同会话报 429 才写冷却，单会话只计票不冷却；无会话标识时回退单次冷却，避免被限流账号被无限复用。
- 403 保持单次冷却；网络错误只走 `markOpencodeProxyRealFailure` 计数，不冷却也不计票。
- 两个池都加会话去重：`pickOpencodeProxyAccount` 与 `pickWarpSlot` 增加 session 入参，同会话排除已用账号/槽位；代理池耗尽后铸造新账号，WARP 槽位全部用过后清空该会话记忆允许复用，记忆 TTL 24 小时。
- 会话标识派生从发起上游前移到选号之前（`handler.go`），否则选号时还没有会话可用。

## 后果

- 收益：单会话偶发 429 不再把出口闲置一整天，同会话换号必定换新账号/新槽位。
- 代价：429 冷却从单次变成三会话 quorum，极端持续限流下账号冷却更晚；会话记忆仅存内存，重启后去重与计票重新开始。
- 作用面：仅 OpenCode 免费档代理账号池与 WARP 槽位池；403、Key 冷却、成员健康度语义不变。

## 验证

```sh
go test ./internal/relay/ -run 'TestOcProxyPoolPickSessionDedup|TestOcProxyRateLimitedQuorum|TestWarpSlotPoolPickSessionDedup' -v
go test ./internal/relay/ -run 'TestOpencodeProxy429|TestOpencodeProxy403|TestWarpSlot' -v
go build ./... && go vet ./...
```
