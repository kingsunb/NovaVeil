# Agent Note: 免费渠道模型评估确定性失败累计自动删除

Status: implemented

## 问题

内置免费渠道(OpenCode Free、UncloseAI、AI Horde、Pollinations 等)的模型由 `internal/builtin/builtin.go` 的
`BuiltinFreeChannels` 维护, 但这些第三方免费上游经常静默下线模型, 或把原本免费的模型改为收费。模型一旦
失效, 每次「一键评估」都会在上游返回持久性错误——401(凭据失效/已转收费)、403(权限拒绝)、404(模型不存在)——而
这类失败重试不会变好: 下一条评估任务对同一模型再测一遍, 结果一样。网关此前不区分「模型永久失效」与「暂时打
不通」, 失效免费模型会一直留在渠道模型列表里, 在自动分组的选择/替换里被反复选中、反复失败, 只能靠管理员逐台
手工删除。需要给免费渠道模型一套自动清理机制, 且失败类型与次数都收敛到「必然无效」才动手, 不能误删因限流或
网络抖动而暂时失败的、仍健康的免费模型。

## 决定

在评估失败路径开一个仅对 `is_free=true` 渠道生效的自动清理钩子, 挂在 `internal/eval/scheduler.go`
`executeTask` 的失败分支:

- **确定性失败的判定** `eval.isFatalEvalStatus(err)`: 用 `relay.UpstreamStatusCode` 从(可能已包装的)错误里
  还原上游 HTTP 状态码, 只有 401/403/404 算「确定性失败」。429/限流、超时(`context.DeadlineExceeded`)、网络
  错误、5xx 一律不计——它们可能是瞬时或可恢复的。这与转发层把 400/404/422 及keyless 401/403 视为确定性快速
  失败(见 [2026-09-23 无密钥渠道确定性快速失败](../bug-fix/2026-09-23-keyless-401-403-deterministic-fast-fail.md))
  是**两套独立白名单**: 本处面向评估的免费渠道清理, 只认 401/403/404, 不涉转发语义。
- **计数落点** `model.ChannelModel.EvalFatalFailures` 新列(`eval_fatal_failures`, not null default 0),
  加到 `ChannelModel` 行上而非 `ModelEvalStats`。计数随模型删除一起消失, 重建同名模型从零开始, 语义干净。
- **累加动作** `op.ChannelModelRecordFatalEvalFailure(ctx, channelModelID)`: 事务内读该行计数 +1 落库;
  达到 `ChannelModelFatalEvalLimit = 3` 时, 复用既有级联清理 `clearActiveItemsByChannelModels` /
  `deleteItemsByChannelModels` / `deleteEvalRanksByChannelModels`, 再物理删除 `ChannelModel` 行; 事务
  提交后 `channelModelCache.Del` + `groupRefreshCache` 同步进程内缓存。删除后在该条评估历史的 `Error`
  文案末尾追加「该免费模型累计确定性失败达到阈值，已自动删除」; `executeTask` 用 `modelDeleted` 标记
  跳过该模型的 `ModelEvalRankUpsert` 与历史裁剪, 避免刚删完 `eval_ranks` 又写回一条 `channel_model_id`
  悬空的孤儿 rank, 评估历史本身照常落库作为最后一次失败的审计留痕。
- **偏向失败、不重置**: 成功**不**清零计数——一次成功可能只是请求高峰外的侥幸通过, 不代表模型恢复持续可用;
  只有确定性失败才 +1, 累计到 3 才删, 用「多次独立观测」压低误删率。

判定责任拆分: `is_free` 与 `isFatalEvalStatus` 由 eval 层判断并调用; `op` 层函数只做「计数+达阈删除」, 不
感知渠道是否免费, 便于单测与复用。非免费渠道(管理员自己的渠道)即使 401/403/404 也**不**自动删, 交由管理员管理。

## 备选方案

- **连续失败而非累计计数** — 最强论据是一个健康模型在偶发一次 401 后恢复, 连续计数能宽容个别抖动; 但用户明确
  要求累计: 免费的确定性失败(转收费/下线)几乎不回退, 偶发成功更可能是误报, 累计更能反映「长期失效」。
- **把所有评估失败(含 429/超时/网络/5xx)都计入** — 最强论据是一条规则覆盖所有异常; 否掉因为免费渠道限流
  (429)与队列拥挤高频出现, 把这些瞬时失败计入会在限流期连删三台仍健康的模型, 与目标相悖。429/网络/超时/5xx
  正是此机制刻意排除的信号。
- **计数挂在 `ModelEvalStats` 复用既有累计字段** — 最强论据是不新增 schema; 否掉因为它按
  `(channel_id, model_name)` 聚合、删模型后仍残留, 重建同名模型会继承旧失败计数, 且该表语义是「历史吞吐统计」
  而非「待删脏模型标记」, 混用会让统计与清理互相污染。
- **删除时同时清理 `ModelEvalStats` 残留** — 最强论据是避免重建同名模型时把旧统计当成「已测」; 否掉因为删除
  后前端 filteredTargets 已随模型消失、不再引用该统计, 保留历史统计利于审计, 且重建同名免费模型属罕见路径，
  届时 admin 可自行清统计。

## 后果

- **收益**: 失效免费模型在累计 3 次确定性失败后自动消失, 自动分组选择/替换不再把它当可用成员反复选到; 管理员
  免去逐台手工删除; 删除走与手工编辑相同的级联清理 + 缓存刷新, 不留分组悬空引用。
- **代价与已知上限**:
  - 删除是**持久**的: `ensureFreeBuiltinChannels` 只按名称补建**缺失渠道**、不重建被删模型, 因此被自动删掉的
    免费模型重启不会自动回来; 仅在 `BuiltinFreeChannels` 代码变化触发渠道重建、或管理员手动 auto-sync/重新
    添加时才恢复。删除即「人工+代码」共同管理该列表的边界。
  - 误删上限低但非零: 一个模型连收 3 次 401/403/404 才删。若上游对某合法模型稳定误返 403(而非 429), 它会被删;
    但这类稳定 4xx 本身就是故障信号, 删除仍比无限报错更优。
  - 计数不持久化到任何管理 UI(仅落库), 管理员目前看不到「已失败几次」; 若需观察中间态, 需另加展示。
  - 非免费渠道不受影响; 评估的 401/403 若属于 keyed 多 Key 渠道, 仍走转发层的 Key 轮换语义, 本机制只作用于
    eval 观测到的最终失败。

## 验证

- `internal/eval/fatal_test.go` `TestIsFatalEvalStatus`: 表驱动测 401/403/404 为真, 429/400/500/502/网络/
  超时/无状态码/nil 为假, 钉死「仅确定性白名单」。
- `internal/op/channel_model_fatal_test.go` `TestChannelModelRecordFatalEvalFailure`: 前两次只计数
  (`eval_fatal_failures` 依次 1、2)不删, 第三次 `deleted=true` 且 DB 中该行消失, 第四次幂等不报错;
  `ZeroID`/`MissingID` 亦幂等。`go test ./internal/eval/... ./internal/op/` 通过。
