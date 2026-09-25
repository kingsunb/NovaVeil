# Agent Note: 提交后失败路径归还半开探测占用与紧急兜底额度

Status: implemented

## 问题

流式转发存在一类"已提交给客户端之后才发现失败"的收尾路径：`firstErr`（读错误）、`polluted`（污染流）、`frameFailure`（提前干净关闭、思考-only 自然终止、聚合失败等定稿哨兵）触发时，本轮已无法重试，只能跨请求累计成员健康度后终止。该路径此前只调用 `recordPostCommitFailure` 做连击记账，随后直接 `return`，从不调用 `releaseRefChainHops(hops)` 归还整条引用链的占用。

当本轮选路胜出者是半开恢复候选（`recoverExpiredItems` 把最先探测成功的成员置为 `route.ProbeItemID`，等待本请求做真实业务二次确认）时，这个遗漏把缺陷放大成整组不可用：

- `ProbeItemID` 永久滞留：`pickGroupItemWithContext` 一看到 `route.ProbeItemID != 0` 就返回空（route.go:116-119），`claimHalfOpenLocked` 也拒绝新半开——该分组此后选不出任何成员，仅删成员、删组或重启可恢复。
- 同一路径同样泄漏紧急兜底并发额度：叶子成员若是紧急放行来源（`claimEmergencyLocked` 递增的 `emergencyCounts`），占用滞留则 `emergencyMaxConcurrent`（=3）次耗尽后紧急兜底整体失效。

语义上对等的兄弟路径全部显式整链释放并写有"不归还则整组钉死"注释：管理端终止（stopRequested 分支）、RPM 门禁等待取消、上游 400 清洗重试、提前 EOF 免费重试。该缺陷由 2026-09-25 全面审计的并发域复核发现，[文档模块重组](../process/2026-09-25-docs-module-reorg-remove-dated-audit-snapshots.md) 把它作为未修项补进 ROADMAP 并在模块文档列为高优先级已知边界，2026-09-25 以独立测试动态复现后修复。

## 决定

- 提交后失败块在终态记账（`markCanceled` / `markFailed` + `recordErrorLog`）之后、`return` 之前补上 `releaseRefChainHops(hops)`（handler.go），按层归还整条引用链上每一跳的探测候选占用与紧急并发额度，与兄弟路径的"整链无结论归还"语义对齐。`recordPostCommitFailure` 保持只管连击记账、不碰占用状态——记账与释放的职责分离，该对齐关系写进路径注释。
- 新增回归测试 `internal/relay/probe_hold_release_test.go` 的 `TestPostCommitClientGoneReleasesProbeHold`：复用 `finish_hardening_test.go` 的 `failingWriter`（`failAfter=2`）驱动 clientGone 定稿分支——与提交后连击豁免（`TestForwardClientGoneExemptFromStrikes`）同一收尾语义；预置唯一成员冷却到期加探测桩恒成功的路由态，使选路走整批 HALF_OPEN 并行探测并让胜出者置 `ProbeItemID`；断言请求以失败终态定稿后 `ProbeItemID` 归零、`HalfOpens` 清空、`EmergencyActive` 归零。该测试在未修复代码上实测 FAIL，修复后 PASS。

## 备选方案

- **给 ProbeItemID 加 TTL 兜底（占用滞留一段时间后自动过期）** — 最强论据是选路可自愈，无需每个调用方自律；否掉因为它又引入一个定时器与三态熔断状态机的交互面，治标不治本——契约本就是"持有者负责归还"，兜底只会掩盖违约的调用方。
- **只在 recoverExpiredItems 侧兜底（胜利者长期未被业务确认即自动释放）** — 最强论据是修复点集中在一处；否掉因为它只救 `ProbeItemID` 滞留这一半，同路径的紧急并发额度泄漏同样修不到，且"长期未确认"与"业务确认还在路上"没有可靠的区分口径。
- **扩展 panic 兜底路径的 releaseRefChainProbeHolds 让常规失败路径复用** — 最强论据是现成的幂等释放器直接可用；否掉因为它是 should-never-happen 路径的兜底，route.go 注释明确其刻意不归还紧急并发额度（定论路径重复归还会多扣在飞额度，这是换取"对全部持有状态严格无副作用"的有界取舍），常规失败路径借道等于推翻该函数的无副作用保证。

## 后果

- **收益**：提交后失败归还整链占用，半开恢复候选与紧急并发额度回到可用池，分组选路在成员提交后失败后照常工作；回归测试把该语义钉进门禁，新失败路径漏释放会直接红。
- **代价与已知上限**：每次提交后失败多一次全链 hops 的锁内释放（幂等，与兄弟路径同量级，开销可忽略）；提交后失败路径从此把"连击记账"与"占用释放"两个语义放在同一定稿块——重构选路结构（如引入新的占用状态）时必须同步枚举该块的归还面。

## 验证

- `internal/relay/probe_hold_release_test.go`：`TestPostCommitClientGoneReleasesProbeHold` 断言 clientGone 定稿后 `ProbeItemID==0`、`HalfOpens` 为空、`EmergencyActive==0`。
- 全量 `go test -race -count=1 -timeout=20m ./...` 通过；CI 的 `-race` 门禁见 [CI 启用 -race 并修复三处真实数据竞态](../testing/2026-09-25-enable-race-detector-in-ci.md)。
