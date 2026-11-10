# Agent Note: 后端一致性加固：渠道缓存、用量写入、请求时长与错误脱敏

Status: implemented

## 问题

- 渠道缓存：`ChannelEnabled`、`ChannelAutoDisable`、`ChannelAutoEnableExpired` 先在缓存里取旧快照，写完库只改一两个字段就整条 `Set`；它们与 `ChannelUpdate` 的读回发布没有互斥，交错执行会把对方刚写入的字段覆盖回旧值，数据库与缓存长期不一致。`ChannelDel` 与并发 `Set` 也可能在删除后把渠道复活。
- 用量写入：每个请求终态都同步 UPSERT `usage_buckets`，SQLite 连接池只有 4 条连接，高并发下容易 `database is locked`；写失败仅 `Warn`，计量被静默丢弃。
- 请求总时长：`MaxRequestSeconds` 默认 0，等于默认不限时；全冷却重试使用固定间隔（基础值乘轮次并封顶），同时失败的请求会在同一时刻再次涌入，形成重试尖峰。
- 错误脱敏：`RequestState.Error` 直接保存 `err.Error()`，经管理端状态、SSE 与失败摘要原样输出；上游错误可能回显 Authorization、API Key 等敏感内容。

## 决定

1. **渠道缓存写串行化**
   - 新增包级 `channelCacheWriteMu`，`ChannelCreate`、`ChannelUpdate`、`ChannelEnabled`、`ChannelAutoDisable`、`ChannelAutoEnableExpired`、`ChannelDel`、`channelRefreshCache` 全部持锁；`ChannelGet`/`ChannelGetCore` 保持无锁。
   - 锁序固定为渠道写锁、数据库、缓存，无反向获取。
   - `ChannelEnabled`、`ChannelAutoDisable`、`ChannelAutoEnableExpired` 在事务内先写库再 `First` 读回整行，解密后发布缓存，不再基于旧快照局部修改。
   - 自动恢复继续使用带 `auto_disabled_until` 条件的 UPDATE 与 `RowsAffected` 判断，人工接管后不复活。
   - `ChannelDel` 纳入同一把锁，避免删除与并发发布交错；解密失败按错误返回或记录并跳过，不用旧快照兜底。
2. **用量有界队列**
   - 新增进程内 `usageBucketWriter`：队列容量 4096，500ms 或 100 条触发 flush；按 `(bucket_at, model_name)` 在内存合并增量，单协程单事务逐行 `OnConflict` 累加。
   - 未启动 writer 时保留同步 UPSERT，兼容测试与嵌入场景；启动后 `RecordUsageBucket`/`RecordErrorBucket` 非阻塞入队。
   - 队列满时丢弃并累加原子计数，首条及每 1000 条打 `Warn`，`UsageBucketDroppedCount` 可观测，不再静默。
   - 写失败保留批次下轮重试并记录错误；`StopUsageBucketWriter` 先排空队列再最终 flush，错误交给 shutdown 钩子。
   - 启动放在 `op.InitCache` 之后；停止钩子注册在 `task.StopAll` 之后、数据库关闭之前。
   - 不按驱动分支，继续用 GORM `OnConflict` 与事务覆盖 sqlite、mysql、postgres；错误计数进入同一聚合，避免额外队列。
3. **请求时长与重试抖动**
   - `DefaultGroupRelayConfig.MaxRequestSeconds` 改为 1 小时；显式 0 仍表示不限时；负值在归一化时回填默认。
   - 存量分组 JSON 缺字段与显式 0 无法区分，继续按不限处理，避免把旧配置突然截断。
   - 抖动集中在 `RequestState.wait`：非零等待乘以 `1 + rand[0, 0.2)`，覆盖成员重试、全冷却与路由复检；0 秒保持立即重试。
   - 采用正向抖动，保证不低于原退避时间，但最坏比配置上限多 20%。
4. **错误原文统一脱敏（已被 2026-11-11 笔记取代）**
   - 在 `op` 导出 `RedactSensitiveText` 薄包装，复用错误日志已审计的 Bearer 与字段名正则。
   - relay 在 `markFailed`、`markCanceled`、`finishRound`、尝试轨迹 `ErrBrief`、测试探针与 Key 测试结果写入 `Error` 时统一脱敏。
   - 选择在写入点脱敏，使状态快照、SSE、失败摘要与错误日志一次覆盖；`ClassifyError` 仍使用原始错误。
   - `op` 不 import relay，relay 已 import op，新增导出不形成循环依赖。
   - 2026-11-11 修正：错误文本改为后台原文、下游脱敏，见 [2026-11-11-error-text-destination.md](2026-11-11-error-text-destination.md)。

## 备选方案

- **渠道缓存版本号或 CAS**：缓存只有整代原子替换，没有比较并交换原语；要支持 CAS 需改接口并处理 `RefreshAll` 竞争，改动面与回归风险更大。写路径只来自管理端和每分钟自动恢复，全局串行足够。
- **每个渠道单独加锁**：需要额外锁表并在删除、刷新时协调，否则仍无法防止删除后复活；全局锁更简单且无锁序歧义。
- **增加 SQLite 连接数或 busy_timeout**：只降低锁冲突概率，不消除每请求写放大与静默丢量；连接越多反而加剧写竞争。
- **多行单条 UPSERT**：三种方言对 excluded/values 语法不同，逐行事务最稳，且批内行数有界。
- **把 0 直接归一化为默认**：会破坏显式 0 等于不限时的既有契约，也无法区分存量缺字段；需要新增指针字段或迁移，超出本次范围。
- **逐个等待点加抖动**：容易漏掉重试路径并产生重复代码；集中到 `wait` 覆盖全部退避。
- **只在 MarshalJSON 脱敏**：会漏掉失败摘要与错误日志；在 relay 复制正则会与 op 规则漂移。

## 测试

- 渠道并发写后数据库与缓存收敛：`TestChannelConcurrentWritesKeepCacheConsistent`。
- 用量队列 Stop 排空、100 条批量 flush、满队列丢弃计数。
- 抖动边界（0 与负值不变、正数落在配置值与 1.2 倍之间）与随机性。
- `markFailed`、`markCanceled`、`finishRound` 后台错误文本保留原文；下游 `downstreamErrorText` 仍按模式脱敏，错误分类不变。
- 默认总时长与负值回退：`finish_hardening_test.go` 更新为 3600 秒与 3700 秒边界。

## 后果

- 渠道写路径串行化：管理端修改、自动停用/恢复、缓存刷新会互相等待；`ChannelAutoEnableExpired` 逐条事务持锁时间随到期数量增长。路由读仍无锁。
- 用量统计最多延迟约 500ms 或 100 条；数据库故障时 pending 可能持续占用内存到队列上限；队列满仍会丢量，但现在有计数与日志。Stop 超时可能留下未落盘批次。
- 新分组默认 1 小时（3600 秒）上限；存量部分配置为 0 的分组仍不限时。需要统一收紧时应通过显式配置或后续迁移，不在本次暗中改变。
- 所有重试等待最多延长 20%，请求总耗时可能略超截止边界附近；这是避免同步重试的代价。
- 下游脱敏是模式匹配，未命中已知 Bearer 或字段名的秘密仍可能出现在下游错误文本中；后台/日志保留原文，错误分类保持原始语义。

## 验证

```sh
cd D:\AI\github\kingsun\NovaVeil
go test ./internal/op ./internal/relay ./internal/model -count=1
```

```sh
cd D:\AI\github\kingsun\NovaVeil
go vet ./internal/op ./internal/relay ./internal/model
```

```sh
cd D:\AI\github\kingsun\NovaVeil
go build ./...
```
