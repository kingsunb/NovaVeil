# Agent Note: WARP 选槽失败改为请求内重试

Status: implemented

## 问题

WARP 全池轮换期间，`/slots` 快照可能短暂没有任何 `active` 槽位。NovaVeil 转发循环在真正发起上游请求前调用 `pickWarpSlot`，命中 `warpSlotsBusyError` 时旧逻辑直接 `markFailed` 并 `rejectRequest`，把请求终止为 503。分组配置的 `MemberRetryIntervalSeconds` 与 `MaxRequestRounds` 均不生效，因为错误发生在 `startRound` 之前。

## 决定

`handler.go` 对 `pickWarpSlot` 返回的 `warpSlotsBusyError` 单独分流：归还本轮引用链占用后，按 `routeRecheckIntervalSeconds(group.RelayConfig)` 等待并重新进入选路循环。该错误不记成员失败、不写错误日志、不冷却成员，也不排除当前成员。其他选槽错误仍按请求级失败立即终止。

等待节奏沿用无可派发目标时的重检间隔：分组 `MemberRetryIntervalSeconds` 为 0 时回退模型默认值，避免紧循环烧轮次。总上限仍由请求循环顶部的 `MaxRequestRounds` 与 `MaxRequestSeconds` 控制。

## 备选方案

- **把 `warpSlotsBusyError` 映射为 503 后让客户端重试** — 最强论据是实现最简单，且协议层可携带 `Retry-After`。否掉的理由是全池轮换通常在几十秒内恢复，把恢复成本推给客户端会打断长连接调用方，也无法利用分组内其他可用成员。
- **把选槽失败计入成员失败并触发 failover** — 最强论据是语义统一。否掉的理由是 WARP 槽位不足是本地准入瞬态，不是成员健康度劣化；计入失败会在轮换恢复前把健康成员打进冷却。
- **等待 `RetryAfter` 的完整时长** — 最强论据是等待窗口与 WARP 池恢复时间对齐。否掉的理由是全池冷却可达 60 秒，长等待会绕过分组的细粒度重试节奏；仍以分组重检间隔轮询 `/slots`。

## 后果

- **收益**：全池轮换期间请求不再立刻 503；分组重试配置重新覆盖 WARP 选槽阶段；健康成员不因本地槽位不足被冷却。
- **代价与已知上限**：槽位持续不可用时请求会按轮次上限持续等待，`MaxRequestRounds` 太小会提前以路由耗尽终止；等待期间占用一个请求状态但不占用 WARP 槽位。若 WARP 池恢复时间远大于分组重试间隔，会产生多次 `/slots` 请求。

## 验证

- `internal/relay/handler.go` 中 `pickWarpSlot` 错误分支使用 `errors.As` 识别 `warpSlotsBusyError` 后 `request.wait` 并 `continue`。
- 用户可执行验证：

```sh
cd D:/AI/github/kingsun/NovaVeil
go test ./internal/relay -run WarpSlot
go vet ./internal/relay

# 触发全池轮换后观察请求是否在恢复窗口内继续重试
curl -sS -X POST 'http://127.0.0.1:1081/rotate?all=1'
```
