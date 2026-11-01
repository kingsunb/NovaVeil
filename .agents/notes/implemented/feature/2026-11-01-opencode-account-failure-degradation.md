# Agent Note: 代理账号连续失败降级与 24h 窗口冷却

Status: implemented

## 问题

OpenCode 免费档代理账号原先只有 403 单次冷却和 [429 两会话计票](2026-11-01-opencode-429-quorum-two-sessions.md)。超时、断流这类真实转发失败只加台账计数，选号仍按「已验证、最近成功」优先，同一个反复失败的出口会继续被选中。

## 决定

- 真实失败口径与 `real_failure_count` 相同：非客户端取消的转发失败，含 403/429。体检路径的 403/429 只冷却，不计真实失败，也不推进窗口。
- 选号顺序是连续失败少者优先，同级已验证优先，再按最近成功时间新者优先。连续失败只留在内存，任何成功（真实转发或体检）或进入冷却即清零；重启后从零计。
- 24 小时翻滚窗口随失败落库。起点为零或距今超过 24 小时则改写为当前时间且计数置 1，否则加 1；加满 6 次且当前未冷却时，冷却到下一个 UTC 零点（上限 24 小时），并清零连续失败与窗口。已在冷却中的失败只加总计数，不推进窗口，也不改冷却截止。成功不清窗口。
- 窗口的加减只用库内旧值做条件更新，并发写可组合。冷却写入同时把窗口两列清零。进程启动时只把未冷却行的窗口播种进内存。
- WARP 槽位不使用本机制。台账页不加列。

## 备选方案

- **窗口也由调用方写入读到的绝对值**：实现短，但后到的写会覆盖先到的计数，并发下窗口会丢。
- **窗口计满后总是重写冷却截止**：能保证满 6 次一定冷却，但会盖掉 403/429 已经按 `Retry-After` 定好的截止，也会绕过 429 的两会话门槛。
- **连续失败也落库**：重启后降级还在，但选号软惩罚不需要跨进程，多一列持久状态没有对应收益。

## 后果

- 收益：反复失败的出口会让位于零失败账号，含未验证新号；6 次真实失败会在当天余下时间停用该出口，重启后窗口仍在。
- 代价与已知上限：连续失败重启即归零，进程刚拉起时不会延续降级。冷却中的失败不累计进下一窗，到期后从零再计。窗口冷却仍可被体检成功提前清掉，与既有 403/429 复活语义相同。
- 作用面：仅 OpenCode 免费档代理账号池。WARP、Key 冷却与成员健康度不变。

## 验证

```sh
go test ./internal/relay -run 'TestOcProxyCandidatePrefersFewerFailures|TestOcProxyPickPrefersZeroFailure|TestMarkSuccessClearsConsecutiveNotWindow|TestFailureWindowTriggersCooldownOnce|TestFailureWindowResetsAfter24h|TestCooldownClearsFailureState' -count=1
go test ./internal/op -run 'TestSaveOpencodeProxyFailureWindow|TestSaveOpencodeProxyFailureWindowResets|TestSaveOpencodeProxyFailureSkipsCoolingWindow' -count=1
```
