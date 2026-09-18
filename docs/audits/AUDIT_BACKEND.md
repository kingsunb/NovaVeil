# NovaVeil 后端代码质量审计报告

> **归档横幅：** 本文形成于 2026-09 上旬的历史快照，已归档、不再维护；其中标注的未修复项多数已在此后修复，当前审计事实以 `docs/audits/AUDIT_ISSUES_2026-09-13.md` 为准。

**审计范围**: `/tmp/NovaVeil/internal/` Go 后端代码
**审计方法**: grep/glob/read_file 系统性搜索 + 核心文件逐行审查
**核心审查文件**: relay/handler.go(1086行), route.go(869行), state.go(1025行), upstream.go(592行), keyselect.go(241行), channellimit.go(206行), sticky.go(142行), prober.go(118行), recovery.go(54行), client/http.go, db/db.go, conf/config.go, op/channel.go, op/cache.go, task/task.go, server/server.go, utils/cache/cache.go

---

## 发现汇总

| 严重程度 | 数量 |
|---------|------|
| 严重(Critical) | 1 |
| 高(High) | 4 |
| 中(Medium) | 7 |
| 低(Low) | 4 |
| 信息(Info) | 3 |
| **合计** | **19** |

---

## 严重(Critical)

### C-1: unsafe.String 零拷贝转换存在 GC 安全隐患

**文件**: `internal/relay/handler.go:84`
**代码**: `requestBodyString := unsafe.String(unsafe.SliceData(raw.Body), len(raw.Body))`
**问题**: 使用 `unsafe.String` 将 `[]byte` 零拷贝转为 `string`。虽然注释说明 `raw.Body` 从不被原地改写（每轮改写由 sjson 生成新切片），但 `raw.Body` 在转发循环中被多次重新赋值（如 handler.go:268 `raw.Body, err = sjson.SetBytes(raw.Body, "model", ...)`）。`unsafe.String` 创建的 string 与底层 byte slice 共享内存，如果后续任何代码路径对原始 `raw.Body` 底层数组进行修改，将导致 string 内容被静默篡改，违反 Go 字符串不可变契约。该 string 被存入 `RequestState.body` 并在锁外被 `finish()` 异步读取用于对话留存和用量修复。
**修复建议**: 使用 `string(raw.Body)` 进行安全拷贝。对于 64MB 上限的请求体，每请求一次拷贝是可接受的开销，相比 unsafe 带来的潜在数据竞争风险更为稳妥。

---

## 高(High)

### H-1: time.After 在 wait 函数中未停止，高频重试场景下累积 timer 泄漏

**文件**: `internal/relay/state.go:414`
**问题**: `time.After` 创建的 timer 在 ctx.Done() 先触发时不会被 Stop，timer 资源直到到期才被 GC 回收。在 `handler.go` 转发循环中，`request.wait()` 在每轮选路失败、成员冷却等待等路径被频繁调用。当客户端频繁取消请求或上游快速失败导致大量重试时，未到期的 timer 会累积在 runtime timer heap 中，增加 GC 压力和内存占用。
**修复建议**: 改用 `time.NewTimer` 并在退出时显式 `defer timer.Stop()`。

### H-2: task.RUN() 使用 select{} 永久阻塞，无法优雅退出

**文件**: `internal/task/task.go:124`
**问题**: `RUN()` 通过 `select{}` 永久阻塞调用方 goroutine。`StopAll()` 只关闭各任务的 `stopCh`，但 `RUN()` 本身没有任何退出机制。调用者永远无法从 `RUN()` 返回，`StopAll()` 必须在另一个 goroutine 中调用。`select{}` 阻塞的 goroutine 无法被 context 取消或信号中断。
**修复建议**: 提供接受 `context.Context` 参数的变体，或将 `select{}` 替换为等待 stop 信号 channel，使 `StopAll` 能同时解除 `RUN` 的阻塞。

### H-3: sanitizeRequestBody 忽略 sjson 错误，请求体可能处于不一致状态

**文件**: `internal/relay/handler.go:1062, 1067, 1084`
**问题**: `sanitizeRequestBody` 在三处忽略 sjson 操作的返回错误（`raw.Body, _ = sjson.SetBytes(...)`）。如果 `raw.Body` 因前序操作已处于异常状态，忽略错误会导致请求体被静默设为空或无效值，上游收到空请求体后返回 400，触发不必要的重试循环。该函数在 handler.go:473 的 400 清洗重试路径中被调用，属于错误恢复路径上的关键操作。
**修复建议**: 检查 sjson 返回错误，失败时保留原始 body 不变继续处理。

### H-4: recoverExpiredItems 并行探测 goroutine 无 WaitGroup 跟踪

**文件**: `internal/relay/route.go:310-338`
**问题**: `recoverExpiredItems` 启动的并行探测 goroutine 没有被 `WaitGroup` 跟踪。虽然 `defer cancelAll()` 会取消所有探测的 context，且 `results` channel 容量等于候选数使得写入不会阻塞，但如果 `probeChannelFunc` 的实现不正确响应 context 取消（如底层 HTTP 请求忽略 ctx），goroutine 将在函数返回后继续运行成为泄漏。
**修复建议**: 添加 `sync.WaitGroup` 跟踪所有探测 goroutine，在函数返回前等待所有 goroutine 完成。

---

## 中(Medium)

### M-1: keyCursors map 无上限增长

**文件**: `internal/relay/keyselect.go:24-25`
**问题**: `keyCursors` 按渠道 ID 保存轮询游标，仅在 `CleanupChannelKeyState`（渠道删除）时清理。频繁增删渠道时废弃游标条目会永久累积。
**修复建议**: 在 `channelRefreshCache` 时顺带清理不存在于新缓存中的渠道 ID。

### M-2: keyCooldowns 过期条目仅在读取时惰性清理

**文件**: `internal/relay/keyselect.go:28-29`
**问题**: 过期条目仅在 `channelKeyCooling` 读取时被惰性删除。如果某 Key 被标记冷却后不再被任何请求选中，其冷却条目将永久驻留内存，构成缓慢的内存泄漏。
**修复建议**: 添加定期清扫 goroutine 或在 `channelRefreshCache` 时顺带清理。

### M-3: sessionStickies 无主动过期清理

**文件**: `internal/relay/sticky.go:17`
**问题**: 会话粘合条目的过期清理仅在 `bindSessionSticky` 中以 60 秒间隔节流执行。如果某分组在一段时间内没有新的成功请求，过期的粘合条目不会被清理，持续占用内存。
**修复建议**: 在后台定时任务中定期调用全量清理。

### M-4: clientIPSet 触顶时整体重置，丢失统计精度

**文件**: `internal/relay/state.go:988-992`
**问题**: 当客户端 IP 集合达到上限 4096 时，遇到新 IP 会整体清空重建。在代理/CDN 环境下，IP 统计频繁归零，面板展示的"独立客户端数"在 4096 和 0 之间剧烈跳动。
**修复建议**: 考虑使用 HyperLogLog 近似计数器，或使用 LRU 淘汰而非全量重置。

### M-5: trimFinishedRequestsLocked 每次只删除一条最旧记录，O(N) 扫描

**文件**: `internal/relay/state.go:552-567`
**问题**: 每次终态定稿都遍历全部 requests map 来计数已完成请求并找最旧 ID。高并发场景下在全局锁 `mu` 内执行 O(N) 扫描，造成锁持有时间延长。
**修复建议**: 维护独立的 finished 计数器和环形缓冲，避免每次定稿遍历全量 requests map。

### M-6: recoverExpiredItems 使用 context.Background()，探测不受请求超时约束

**文件**: `internal/relay/route.go:310`
**问题**: 半开恢复探测使用 `context.Background()` 创建独立上下文，即使客户端已断开，探测仍会继续执行并消耗上游配额。
**修复建议**: 在全局停止（`IsAllStopped()`）时取消所有进行中的探测。

### M-7: loginratelimit 使用 context.Background() 进行数据库操作

**文件**: `internal/server/handlers/loginratelimit.go:65, 89, 96, 113`
**问题**: 登录限速器的全部数据库操作使用 `context.Background()`，不继承请求 context 或服务生命周期 context。优雅关闭时可能在 `db.Close()` 后尝试写入。
**修复建议**: 将 `server.baseCtx` 或请求 context 传入登录限速器的数据库操作。

---

## 低(Low)

### L-1: ChannelGetCore 返回缓存直接引用，调用方可能意外修改

**文件**: `internal/op/channel.go:383-389`
**问题**: `ChannelGetCore` 返回缓存中的 `model.Channel` 值副本，但 slice/map 底层数组仍共享。如果调用方修改返回的切片字段，会直接修改缓存底层数据。
**修复建议**: 在文档注释中更明确标注"返回值切片字段与缓存共享底层，调用方不得修改"。

### L-2: 错误包装不一致：部分使用 %w，部分使用 %v

**文件**: `internal/op/cache.go:13-16` 等多处
**问题**: 代码库中错误包装混用 `%w`（errors.Is/errors.As 可穿透）和 `%v`（仅文本展开）。`op/cache.go` 的四处刷新缓存错误均使用 `%v`，导致调用方无法用 `errors.Is` 判断根因。
**修复建议**: 统一使用 `%w` 包装错误。

### L-3: statsColumnsOnce 永久缓存列存在性判定

**文件**: `internal/op/channel.go:451-455`
**问题**: `sync.Once` 永久缓存统计列是否存在的结果。未来有新迁移添加或删除统计列时，需重启进程才能生效。
**修复建议**: 在注释中补充维护提醒，当前设计在已知迁移场景下合理。

### L-4: 测试覆盖不均衡

**文件**: 整个 `internal/` 目录
**问题**: 生产代码 102 个文件，测试文件 78 个。relay 包覆盖较好，但 `op/group.go`、`op/backup.go`、`task/sync.go` 等关键管理操作缺少对应测试文件。数据库迁移脚本缺少回滚测试。
**修复建议**: 为缺少测试的关键模块补充单元测试，重点覆盖事务回滚、并发缓存刷新、迁移幂等性。

---

## 信息(Info)

### I-1: 良好的并发安全实践

**正面发现**:
- `relay/handler.go:115-120`: panic 兜底正确归还引用链探测占用，防止整组钉死
- `relay/state.go:902`: `allStopped` 使用 `atomic.Bool` 避免热路径锁开销
- `relay/route.go:823-830`: `cloneRouteState` 正确克隆所有 map 字段，防止 SSE 消费者与锁内写入构成数据竞争
- `relay/upstream.go:42,52-61`: `closeOnce sync.Once` 保证 `Close` 幂等，可安全并发调用
- `relay/channellimit.go:104`: `sync.Once` 包装 release 函数保证幂等归还
- `utils/cache/cache.go`: 分片缓存设计良好，`RefreshAll` 消除空窗口
- `task/task.go:166-173`: `guardedCall` 使用 `atomic.Bool` 防止任务自身重叠

### I-2: 良好的资源管理实践

**正面发现**:
- `client/http.go:63-64`: 代理切换时 `CloseIdleConnections` 清理旧连接池
- `client/http.go:108-116`: 自定义代理客户端缓存有软上限，超限时整体清空
- `relay/upstream.go:48-62`: `upstreamResponse.Close` 使用 `sync.Once` 保证幂等释放
- `db/db.go:50-56`: 连接池按方言区分，SQLite 限制 4 连接避免 WAL 写竞争
- `server/server.go:71-74`: 正确设置 ReadHeaderTimeout/IdleTimeout 防止 Slowloris 和闲置连接泄漏

### I-3: 良好的错误处理与可观测性实践

**正面发现**:
- `relay/handler.go:994-1034`: 错误日志按类限流，前3条保留完整请求体，后续仅存摘要
- `relay/state.go:488-548`: 终态定稿分两阶段执行，锁外处理大报文避免全局锁阻塞
- `relay/route.go:541-566`: `memberFailureCounts` 业务/基础设施失败独立计数，防止交替失败空转
- `relay/handler.go:463-468`: 提前 EOF 免费重试机制，每个成员每请求仅一次
- `op/error_log.go`: 攒批落库 + 按类去重节流，故障风暴下保护数据库
