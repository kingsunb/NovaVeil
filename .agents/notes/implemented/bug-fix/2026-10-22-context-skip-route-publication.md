# Agent Note: 上下文窗口跳过在路由发布前生效

Status: implemented

## 问题

分组开启上下文窗口过滤后，大请求会在 200K 成员 A、B 与 1M 成员 C 之间来回闪烁。`pickGroupItemSkipping` 选中 A 时立即写入并发布 `route.CurrentItemID`，handler 随后才发现 A 的窗口不足并跳过；B 重复同一过程后，面板才看到 C。被跳过成员实际没有派发、没有冷却，但已经作为“当前承载”发布，前端因此显示错误的健康状态。

问题不只影响直连成员：粘合可以再次命中被跳过成员，引用链的内层选路也会发布自己的 `CurrentItemID`，紧急兜底同样可能在窗口不足时被选中。只在下游过滤后清空当前项会留下瞬时错误快照，并可能覆盖其它并发请求的路由状态。

## 决定

- `Forward` 在每轮选路前调用 `scanContextFit`，递归扫描当前分组可达的叶子成员。扫描遵循手动模式只取 `ActiveItemID`、故障转移取全部成员、引用链深度上限与环检测，并复用 `channelLookupFunc`、`effectiveContextWindow` 与 `contextFitsWindow`，不引入第二套窗口判定。
- 扫描把窗口不足的叶子写入现有请求级 `contextSkipped`；全部后代都不适配的引用成员另记入 `blockedRefs`，并入只供本轮选路使用的 `selectionSkipped`。顶层选路、粘合、引用链与紧急兜底都读取同一个合并集合，因此在写 `CurrentItemID` 之前就会跳过这些成员。
- 被窗口跳过的成员立即清除会话粘合；若它正是路由的当前成员，选路入口同时清空 `CurrentItemID`、`AffinityUntil` 与 `affinityArmed` 并发布清零快照，不保留亲和。
- 全部可达叶子都被窗口排除时，`Forward` 在进入等待、冷却清除或紧急兜底之前返回既有的 `errContextWindowTooSmall`，文案继续携带请求估算与候选集最大窗口。
- handler 中原有的选中后窗口检查保留为防御性兜底，正常路径不会触发；它和预扫描共用 `contextFitsWindow`，不增加失败计数或冷却。

相关背景见 [上下文过滤跳过集合与同步窗口刷新](2026-10-22-context-fit-skip-set-and-relay-mode.md) 与 [分组级上下文窗口过滤](../feature/2026-10-20-context-fit-routing.md)。

## 备选方案

- **给 `pickGroupItemSkipping` 增加可承载回调**：能直接在扫描候选时判定直连叶子；否掉是因为引用成员必须先展开目标分组才能知道窗口，回调会被迫递归调用选路或复制引用解析，且粘合、紧急兜底、引用链每个入口都要同步扩参。预扫描一次生成集合，直接复用既有 `skipped` 贯穿语义。
- **先照旧发布，再在 handler 跳过时把 `CurrentItemID` 改回去**：改动局部；否掉是仍会向 SSE 发出错误快照，且回写可能覆盖同一分组内另一个并发请求刚建立的路由，无法满足“从未发布”。
- **只修复直连成员，不改引用链**：测试量最小；否掉是引用组内层仍会在窗口检查前发布，且全内层不适配时外层引用会先被选中，问题只是从顶层转移到嵌套分组。
- **把窗口不足计入失败或冷却**：可复用现有换成员路径；否掉是窗口不匹配不是渠道故障，会把健康成员错误打入冷却，也会污染后续请求的可用集合。

## 后果

- **收益**：窗口不足成员不再进入 `current_item_id` 发布序列，也不再留下粘合或亲和；四个成员场景只发布首个 1M 成员，引用链与全候选不足场景同样不会先派发再拒绝。
- **代价与已知上限**：开启过滤时每轮选路多一次可达叶子扫描和渠道缓存读取，复杂度与 `routableCandidateCount` 同阶；密度估算误差仍存在，handler 兜底继续覆盖扫描与派发之间配置变化造成的极窄竞态。

## 验证

- `TestContextFitDoesNotPublishSkippedMemberAsCurrent`：四成员、两个 200K 与两个 1M、请求约 689K；断言发布流中两个 200K 成员从未成为 `CurrentItemID`，最终为首个 1M 成员，200K 上游零请求且无冷却。
- `TestContextFitAllSmallWindowsRejectedBeforeDispatch`：全部 200K 时返回 400 与 `errContextWindowTooSmall`，发布流无被跳过成员，上游零请求。
- `TestContextFitRefChainDoesNotPublishSkippedMemberAsCurrent` 与 `TestContextFitRefChainAllSkippedDoesNotPublish`：分别覆盖内层混合窗口与全不足；内层 200K 成员不发布、不派发，全不足时外层引用也不发布并直接 400。
- 交付验证：`GOTOOLCHAIN=go1.26.10 go build ./...`、`GOTOOLCHAIN=go1.26.10 go vet ./...`、`GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/relay`、`GOTOOLCHAIN=go1.26.10 go test -count=1 -race ./internal/relay`、`GOTOOLCHAIN=go1.26.10 go test -count=1 -timeout=15m ./...`。
