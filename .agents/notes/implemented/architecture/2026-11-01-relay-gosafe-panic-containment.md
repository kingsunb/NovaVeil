# Agent Note: relay 全部 fire-and-forget 协程统一 goSafe 兜底

Status: implemented

## 问题

[探测 goroutine 的 panic 兜底](../bug-fix/2026-09-25-probe-goroutine-panic-recover.md)只覆盖了探测栈, relay 包内仍有 10 处 fire-and-forget 派生点裸跑无 recover: opencode 代理池异步落库(成功/失败/冷却)、WARP 控制 POST、opencode 账号体检作业与单账号体检、半开探测、渠道 Key 测试、分组测试。协程内任意 panic 直接击穿整个网关进程, 全部在途请求陪葬; 这些点位多为落库与通知, 没有探测那样的"必须产出失败结论"的调用方, 适合进程级兜底而非语义转换。

## 决定

- `internal/relay/gosafe.go` 新增 `goSafe(name string, fn func())`: 独立协程运行 fn, `defer recover` 捕获后以 `log.Errorf("relay %s panicked: ...")` 记录 panic 值与 `debug.Stack`, 进程不倒。
- relay 包内全部 fire-and-forget 派生点统一经 `goSafe(<场景名>, ...)` 派生, 场景名进日志便于定位。现行 10 个点位: opencode-proxy-success / opencode-proxy-failure / opencode-proxy-cooldown / warp-control / async-probe / opencode-probe-job / account-probe / group-probe / channel-key-test / group-test。
- 分层不变式: relay 包内新增 `go func()` 派生点必须经 goSafe。`runProbeSafely` 仍是探测 panic 的语义层(panic 转失败结论, 保证批量收集器收齐 results 不悬挂), goSafe 是其外层的最后防线, 两者叠加不互斥。
- wg 型点位在闭包内保留 `defer wg.Done()`, panic 时 Done 照常执行, Wait 不悬挂。

## 备选方案

- **复用 task.safeCall** — 最强论据是不新增代码; 否掉因为它绑定 taskEntry 私有类型且会让 relay 反向依赖 task 包, 分层不合, 语义(转换任务结果)也不匹配纯兜底。
- **逐点位手写 defer recover** — 最强论据是各点位可自定义日志; 否掉因为十处重复样板必然漂移, 新增点位漏写一处就回到原点, 统一入口才能靠 grep 清点。

## 后果

- **收益**: 协程 panic 的爆炸半径从"整个进程"收敛为"当次任务失败 + 一条带栈日志"; 全部派生点可机械清点(`grep -c "goSafe(" internal/relay/*.go`)。
- **代价**: 每个派生点多一层闭包与 defer(量级可忽略); panic 只记日志不重试, 落库类任务该次丢失。重访信号: 日志出现 `relay <name> panicked` 即对应实现存在待修 bug。

## 验证

- `internal/relay/gosafe_test.go`: panic 不外泄且 defer 照常到达、正常路径照常执行。
- 清单核对: `grep -rn "goSafe(" internal/relay/ --include="*.go"` 与上文 10 点位一致, 无裸 `go func()` 派生点残留(测试文件除外)。
