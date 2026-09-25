# Agent Note: 探测 goroutine 统一 panic 兜底

Status: implemented

## 问题

半开恢复的合成探测在两处独立 goroutine 上执行探测实现（`probeChannelFunc`，调用栈含渠道回调、协议转换与诊断重试）：

- `recoverExpiredItems` 批量探测的 inline goroutine（route.go）：整批切 HALF_OPEN 后并行探测，收集器按候选数收 results；
- `ensureProbeLocked` → `go runAsyncProbe` 的异步非阻塞探测（prober.go）：请求触发与后台定时探测（`ProbeExpiredItems`）共用同一入口。

两处此前都没有 recover，探测栈任意 panic 的后果按场景分三种：

1. panic 直接击穿网关进程——goroutine 顶层无 recover，整个进程崩溃，全部在途请求陪葬。
2. 批量探测 panic 时不写 results：收集器按候选数死等（`for remaining := len(candidates); remaining > 0` 的收账循环），缺一个结果，整条恢复流程悬挂，触发恢复的选路请求同步挂死。
3. `runAsyncProbe` panic 时成员钉死在 HALF_OPEN：半开标记与占用滞留，选路扫描把该成员永久跳过。

`probeChannelFunc` 是可替换的函数变量（测试用它打桩），渠道类型与探测协议栈的每次扩展都在这条栈上引入新代码，panic 面随渠道生态扩大。docs/modules/relay.md 曾把"探测 goroutine 无 recover"列为已知边界，2026-09-25 与提交后占用归还、CI `-race` 同批落地。

## 决定

- `internal/relay/prober.go` 新增 `runProbeSafely(ctx, channel, modelName)`：`defer recover` 把探测实现抛出的 panic 转为 `fmt.Errorf("probe panic: %v")` 返回，并 `log.Errorf` 记录 panic 值与完整栈——它是探测 goroutine 的唯一 recover 防线，"两处调用场景都依赖必有结论"的调用约定写进函数注释。
- 两处调用点统一改走它：route.go 批量探测的 results 写入、prober.go `runAsyncProbe` 的探测调用。panic 按探测失败落账——批量场景必写一条失败 results，收集器按候选数收齐后走整批失败的指数退避；异步场景走既有失败落账 `reopenItemLocked`（等级加一、按倍数退避重新 OPEN）。

## 备选方案

- **只在 goroutine 顶部加 recover 记日志** — 最强论据是实现最小、进程保住了；否掉因为批量探测的收集器仍死等缺失的 results，恢复流程照旧悬挂——panic 必须转成失败结论，不能只是吞掉。
- **给探测加超时兜底防 panic 类故障** — 最强论据是挂死的探测需要一个出口；否掉因为超时防不了 panic 本身，且两处探测均已带 `MemberNonStreamResponseTimeoutSeconds` 超时，缺的正是 recover 这一块。

## 后果

- **收益**：探测栈任意 panic 的爆炸半径收敛为"当次探测以失败落账"——进程照常服务，批量恢复流程按候选数收敛，异步成员走退避冷却等待下一批，栈日志保留 panic 现场。
- **代价与已知上限**：每次探测多一层 defer（量级可忽略）；真 panic 时探测的结论是"失败"而非"无结论"，因实现 bug 崩掉的成员会多退避一轮（等级加一、冷却翻倍）而不是原地等待复测。重访信号：日志出现 `relay probe panicked` 即说明探测实现存在待修 bug，修复前该渠道会持续按指数退避。

## 验证

- 调用面收敛：`runProbeSafely` 的调用点是 route.go 批量探测与 prober.go `runAsyncProbe` 两处，探测实现经 `probeChannelFunc` 全部过它，`internal/relay` 内无其他直接调用。
- `internal/relay/prober_test.go` 覆盖异步探测成功恢复、失败退避落账与并发触发同一成员只探测一次的占用互斥；全量 `go test -race -count=1 ./...` 通过。
