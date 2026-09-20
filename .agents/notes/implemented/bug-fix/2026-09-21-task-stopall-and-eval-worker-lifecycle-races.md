# Agent Note: Task StopAll 与评估 worker 生命周期竞态审计修复

Status: implemented

## 问题

2026-09-20 后续审计确认任务与评估模块存在三个并发/生命周期缺陷：

1. **RELI-01**：`Task.StopAll` 先 `runningWG.Wait()`，等待中 `goGuardedCall` 仍可以执行 `runningWG.Add(1)` 后再 `Wait` 已通过——`Add` 与 `Wait` 无锁，关闭窗口即竞态，运行中任务逃逸，数据库可能连续写。
2. **RELI-02（评估）**：`eval.scheduler.runWorker` 的 defer 栈低层 CPUFunc 在 panic 时执行 `running.Add(-1)` 与 `s.Notify()`，高层的 recover 在低层 defer 之前，恢复状态没有细节，事件被泄漏到模型中、队列卡死在运行状态。
3. **RELI-03**：`runWorker` 中 `Notify` 的执行位置在计数更新与清理逻辑之间不固定，事件顺序随运行容量偏移而变化，影响下次调度的顺序判断。

## 决定

- **RELI-01**：`Task` 增加 `runningWGMu sync.Mutex` 与 `runningStopped bool`。`goGuardedCall` 在锁内 `Add(1)` 并启动 goroutine；`StopAll` 在锁内置 `runningStopped = true` 后再 `Wait`；关闭后 `goGuardedCall` 拒绝新增。`resetLifecycle` 复位 `runningStopped = false`。
- **RELI-02**：`runWorker` 的 defer 调整为 LIFO 顺序：外层 `workerWG.Done()` 先注册；内层 `running.Add(-1)` 与 `s.Notify()` 注册；最内层 recover 捕获 panic 并写 `ModelEvalQueueMarkDone` 后恢复状态。panic 恢复路径确保队列项回到可重试状态。
- **RELI-03**：通知严格在 `running.Add(-1)` 之后执行，且 `Notify` 发送在持有 scheduler 的锁外，事件顺序固定为“减少计数 → 通知”，使容量偏移不再改变事件图。

## 备选方案

- **RELI-01 用 `WaitGroup.Go` 风格扩 constructor**：代价相同，但 Go 版本跨层不易落地，选择轻量互斥锁。
- **RELI-02 用 recover 中心转发闭包含补偿**：小函数体更短，但 defer 中间态难读；选择单一闭包并按依赖顺序放置。
- **RELI-03 事件改异步队列**：代价过高，先保持现有 channel 但锁定发送点在计数之后。

## 后果

- **收益**：关闭窗口消失，运行中任务不再逃逸；worker panic 会把队列项置回可重试而不是永久运行状态；事件顺序对容量不敏感。
- **代价与已知上限**：`runningStopped` 位仅在 `StopAll` 到 `resetLifecycle` 间有效，复用同一 Task 的长生命周期需按现有生命周期 API 调用。重访信号：未来若引入用户态暂停/恢复，需要为 `runningStopped` 增加 SQLite 持久化。

## 验证

- `internal/task/task_test.go`：关闭后不再新增且 exp 值与 Wait 一致。
- 受影响包 `go test ./internal/task/ ./internal/eval/...` 通过。