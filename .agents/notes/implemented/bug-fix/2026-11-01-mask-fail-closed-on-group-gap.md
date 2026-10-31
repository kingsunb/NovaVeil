# Agent Note: 脱敏挪入转发主循环, 分组空窗期不再放行明文

Status: implemented

## 问题

脱敏块原本在转发主循环之外执行, 依赖第二次 `op.GroupGetByName` 读分组: 请求入口处(第 105 行)已查过一次分组并拒绝不存在的模型, 但两次查询之间分组可能被删除——此时脱敏被整体静默跳过, 主循环按既有语义等分组重现后, 用未脱敏的请求体照常转发上游。违背该处注释声明的 fail-closed 契约(脱敏失败拒绝放行明文)。窗口是两次查询之间的瞬态, 但真实存在且后果是明文出网。

## 决定

- 脱敏块移入 `internal/relay/handler.go` 转发主循环: 每轮分组读取成功后、本轮任何使用请求体发起上游之前执行; 用显式 `maskDone` 守卫保证每请求只执行一次, 重试轮复用同一结果不重复扫描。不能用 `maskMapping == nil` 判断是否已脱敏——开关关闭时 mapping 合法为 nil。
- 分组暂不可得时(循环内等待重现)不脱敏也不转发, 恢复那一轮先脱敏再放行; 脱敏失败仍按原语义 markFailed + recordErrorLog + rejectRequest, fail-closed 闭合。
- 每轮分组查询改走既有注入点 `groupLookupFunc`(route.go, 生产值即 `op.GroupGetByName`), 与 route/prober 的测试注入先例一致。
- maskMapping、streamRestorer、`request.Masked`、applyMaskResult、truncateMaskMatches 语义不变; panic 兜底 defer 仍在函数层覆盖脱敏代码。

## 备选方案

- **分组查询失败时直接拒绝请求** — 最强论据是 fail-closed 最直接; 否掉因为分组被删属管理操作瞬态, 请求本就按既有语义等待分组重现, 拒绝会把瞬态运维放大成客户端错误, 与循环内等待设计冲突。
- **保留循环外脱敏、查询失败时重试查询直到成功** — 最强论据是改动最小; 否掉因为自旋重试等于把主循环已有的等待逻辑复制一份, 时序竞态修不干净, 两处等待终将漂移。

## 后果

- **收益**: mask 开启时不存在任何明文转发路径——上游发送(sendPassthrough/sendConverted)全部位于脱敏块之后, 脱敏失败即 return。
- **代价**: 脱敏时点从"进入即刻"推迟到"首轮分组可用"(对成功请求无感知差异); 脱敏块处于主循环内, 阅读时需注意 maskDone 守卫。

## 验证

- `internal/relay/mask_group_wait_test.go`: TestMaskWaitsForGroupThenMasksBeforeForward(分组瞬断后恢复, 上游只见到脱敏体且脱敏恰好一次)、TestMaskOnceAcrossRetryRounds(重试轮跨分组空窗仍只脱敏一次)。
- 既有 TestMaskFailureFinalizesBodyAndReleasesBudget(脱敏失败定稿)保持通过。
