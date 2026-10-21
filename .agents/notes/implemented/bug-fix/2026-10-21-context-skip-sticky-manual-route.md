# Agent Note: 上下文跳过的占用归还与粘合/手动排除

Status: implemented

## 问题

上下文窗口不足的跳过不是成员失败，原先却沿用了失败分支的“只释放祖先跳”写法：`internal/relay/handler.go` 只调用 `releaseRefChainHops(hops[:len(hops)-1])`。直连成员的 `hops` 长度为 1，实际释放的是空切片，叶子的半开候选 `ProbeItemID` 与紧急并发额度都会滞留。`internal/relay/route.go` 看到 `ProbeItemID != 0` 后直接返回空目标，整组因此无法再选路；紧急额度也会持续占用直到重启。其它无结论路径均整链释放，证据是 `handler.go` 的 RPM 取消、Key 轮换、人工中止、400 清洗、提前 EOF、并发满载等分支都调用 `releaseRefChainHops(hops)`。

会话粘合读取只校验过期、成员存在与冷却，未检查本请求的 `exclude`。上下文跳过某成员后，下一轮仍会从粘合拿到同一成员，直到轮次耗尽；引用链的 `pickRefChainHop` 也有同一旁路。手动模式选路只认 `ActiveItemID`，同样忽略 `exclude`，而终止分母仍按全部可达叶子计数。组内存在多个成员但只激活一个时，这个不适配的当前成员会被反复处理，无法按实际候选数及时终止。

## 决定

- 上下文跳过改为 `releaseRefChainHops(hops)` 整链无结论释放，直连叶子与引用链祖先一起归还探测候选和紧急额度。
- 新增 `pickSessionStickyExcluding`。粘合指向 `exclude` 时按失效处理：删除该条粘合并返回零值，调用方回退常规选路；顶层转发与 `pickRefChainHop` 共用这一入口。
- 手动模式选路尊重 `exclude`，当前成员被排除时本轮返回空目标。
- 新增 `routableCandidateCount`：故障转移沿用 `routableLeafCount`；手动模式从当前激活成员出发，直连叶子计为 1，引用成员按目标分组实际可达叶子展开（沿链处理手动分组）。`contextSkipped`、`busyRejected`、`nonRetryable` 三类终止判定统一使用该分母。

相关既有边界见 [上下文窗口过滤](../feature/2026-10-20-context-fit-routing.md)、[满载终止分母](2026-09-26-channel-busy-terminal-denominator.md) 与 [提交后失败归还探测占用](2026-09-25-post-commit-failure-releases-probe-hold.md)。

## 备选方案

- **只给 `hops` 长度为 1 的直连情况补一次叶子释放** — 改动看似更小；否掉是因为引用链最后一跳同样可能在上下文跳过时持有候选或被下游直接选中，按路径分叉会留下同类遗漏。整链释放与其它无结论路径一致。
- **粘合命中排除成员时直接返回零值但不删除粘合** — 能阻止本轮使用；否掉是因为同一会话后续请求仍会反复命中无效记录，且无法在健康成员成功后续期到正确目标。
- **手动模式继续用 `routableLeafCount` 作分母** — 复用现有函数；否掉是因为它统计全部组内叶子，包括未被激活的成员，分母虚高会让请求空转到轮次上限。

## 后果

- **收益**：上下文跳过不再泄漏探测候选或紧急额度；故障转移的粘合成员被排除后立即失效并回退健康成员；手动模式在唯一当前成员不适配时按真实候选数终止。
- **代价与已知上限**：粘合被排除后会清除，当前请求成功时再按成功成员重建；没有新增配置或跨进程状态。手动模式的候选数现在会解析引用链，因此统计成本与故障转移的叶子统计同阶。

## 验证

- `go test ./internal/relay -run 'TestContextSkipReleasesDirectMemberHold|TestStickyExcludedFallsBackToRegularRouting|TestRefChainStickyHonorsExclude|TestManualPickHonorsExcludeAndCandidateCount|TestManualContextSkipTerminatesAtActiveCandidate'`
- `go vet ./internal/relay`
