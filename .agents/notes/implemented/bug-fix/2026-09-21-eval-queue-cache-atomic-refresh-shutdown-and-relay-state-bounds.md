# Agent Note: 评估队列并发、缓存原子代、last_used 去抖、停机顺序与 relay 状态有界性

Status: implemented

## 问题

2026-09-20 后续审计的后端可靠性收尾项：

1. **RELI-04**：`ModelEvalQueuePopNext` 的条件 UPDATE 不检查 `RowsAffected`，多实例可重复派发同一任务。
2. **RELI-05**：评估入队用未加锁的 `MAX(position)+N` 生成 position，并发入队会撞 position。
3. **RELI-06**：`cache.RefreshAll` 逐 shard 清空重建，读者可观察到空窗或混合代。
4. **RELI-07**：`APIKeyTouchLastUsed` 每请求一个 goroutine 直写 DB，没有去抖和上限。
5. **RELI-08**：shutdown 每钩子 10s，评估单任务最长 10 分钟，DB 可能先关而 worker 后写。
6. **REL-07**：`cloneRouteState` 未克隆 `emergencyCounts/emergencyBlocks`。
7. **OLD-10/11/12/13**：`clientIPSet` 触顶整体重置；`trimFinishedRequestsLocked` 每次定稿 O(N)；`recoverExpiredItems` 仍用 `context.Background()`；loginratelimit 四处 DB 调用无超时。

## 决定

- **RELI-04**：PopNext 在事务内查完候选后，对 `RowsAffected != len(tasks)` 的 UPDATE 结果按未成功派发处理并返回空；调度器下一轮重试，绝不返回已由他人置 running 的任务。`ModelEvalQueueStop`/`ModelEvalQueueMoveUp` 也使用 `WHERE id = ? AND status = 'queued'` + `RowsAffected == 1` 判定，避免把并行派发的 running/已离队任务误置回 queued。
- **RELI-05**：Enqueue 的 `MAX(position)` 读取、去重与 `Create` 移入同一个 `db.Transaction`，SQLite 写事务串行化；MySQL 在事务内加 `GET_LOCK('novaveil:model_eval_queue:enqueue',10)`，PostgreSQL 在事务内加 `pg_advisory_lock(0x4e5651455145)`，把多实例并发也串行化，position 不再由锁外 MAX 生成。
- **RELI-06**：`internal/utils/cache` 引入 `generation`（一整套只读 shard 集合）+ 原子 `atomic.Pointer` 发布；`RefreshAll`/`Clear` 在 `refreshMu` 写锁下构造新一代再一次替换；`Set`/`Del` 持 `refreshMu` 读锁，避免写入落到刚被替换的退休旧代而悄然丢失。读者始终看某一代的完整视图。
- **RELI-07**：新增 `internal/op/apikey_lastused.go`：单 writer goroutine，`apiKeyTouchPending` 去重 map、1024 有界队列、256 批量合并、2s 去抖；停用后丢弃新触碰，停写路径把已 pending 但与 channel 调度窗口竞态的事件也迁入最后一批 flush。`cmd/start.go` 在 shutdown 时调用 `APIKeyTouchLastUsedFlush`。
- **RELI-08**：`shutdown.RegisterWithTimeout(task.StopAll, 5*time.Minute)` 与 `eval.Default.Stop()` 的 5 分钟有界超时，server 停 ingress 的钩子注册在 LIFO 之后；`APIKeyTouchLastUsedFlush` 先于 DB close；任务与 eval 在 DB close 前停。`internal/utils/shutdown` 用 `hookWG` 追踪每个钩子 goroutine，Shutdown 返回前等全部钩子 goroutine 结束（最多 `outstandingHooksWaitTimeout`，10 分钟）；`RegisterFinalizer` 钩子(如 `db.Close`)在所有普通钩子（含超时钩子的 goroutine）结束后再按注册顺序执行，超时未等完则跳过 finalizer 依赖 OS 退出清理。
- **新增收尾**：`internal/op/error_log.go` 的 writer 在停机 flush 时把队列排干并等待 writer 最多 35s，期间并后续入队走短超时同步直写（`errorLogShuttingDown`），防止错误日志停机上丢；`internal/op/client_stat.go` 在缓存淘汰/触顶逐出时把行移入 orphaned map 而不是直接删除，flush 失败还能在下轮重试，停机 flush 也会带上 orphaned UPSERT；`internal/eval/scheduler.go` 用 `stateMu` 代替 `startMu`+`stopOnce`，`Start` 失败复位、`Stop` 幂等可重入，并带 1 分钟 `reapTicker` 调用 `ModelEvalQueueResetRunning` 回收超出租约(15 分钟)的 running 任务。
- **REL-07**：`cloneRouteState` 深拷贝 `emergencyCounts/emergencyBlocks` 两个 map。
- **OLD-10**：`clientIPSet` 触顶时按最近访问时间淘汰半数最久未见 IP，不再整体重置。
- **OLD-11**：`trimFinishedRequestsLocked` 改为按完成队列出队 O(1) 裁剪，避免每次终态全表扫描。
- **OLD-12**：`pickGroupItem`/`resolveGroupRefChain` 增加 WithContext 变体，真实请求路径把 handler ctx 一直传到 `recoverExpiredItems`，代替 `context.Background()`。
- **OLD-13**：loginratelimit 的四处 DB 调用接入请求 context，并加 5s 超时。

## 备选方案

- **PopNext 用事务内循环重试**——SQLite 的读事务快照会让重试看到的仍是旧快照；选择单次条件更新 + 空返回让下一调度周期重试。
- **Cache 用 RWMutex 包整个 RefreshAll**——实现改动更小，但 RefreshAll 期间会阻塞全部读者；代际替换保留读写无锁，再加 `refreshMu` 解决写丢失缺口。
- **touch-last-used 用定时批量合并队列**——保留，但无界队列会在慢 DB 时无界增长；选择有界队列 + 满时丢弃。
- **停机直接给 eval 10min 无限等待**——最安全但可能拖死部署；选择 5 分钟有界上界，部署可配置。
- **入队跨实例用 Redis/etcd 分布式锁**——引入新外部依赖不划算；选择 MySQL `GET_LOCK`/PostgreSQL advisory lock 这些 DB 原生原语。

## 后果

- **收益**：队列不会重复派发，position 单调唯一；缓存刷新无空窗；last_used 写放大受控；停机顺序让租户 worker 先于 DB 停止；relay 状态无并发快照缺口。
- **代价与已知上限**：last_used 满队列时可能丢失触碰（字段仅管理展示，可接受）；缓存代际替换保留最后一刻并发 Set 丢失的可能；recoverExpiredItems 的探测仍与请求 context 绑定，取消探测器等于取消半开恢复。

## 验证

- `internal/utils/cache/cache_test.go`：RefreshAll 原子替换与 Set 写丢失窗口覆盖。
- `internal/relay/route_state_clone_test.go`、`internal/relay/state_bounded_test.go`：深拷贝与分片/队列有界行为。
- `internal/server/handlers/loginratelimit_test.go`、`internal/op/apikey_lastused_test.go`、`internal/op/model_eval_queue`、`internal/op/error_log_queue`、`internal/op/client_stat` 路径等单测。
- `internal/utils/shutdown/shutdown_test.go`：含超时钩子时的返回行为与 finalizer 顺序。
- 全量 `go build ./...`、`go test ./...`、`go vet ./...` 通过。