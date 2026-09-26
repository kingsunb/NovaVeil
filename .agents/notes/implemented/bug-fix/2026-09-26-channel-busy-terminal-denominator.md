# Agent Note: 满载与确定性 4xx 终止判定的分母改数可达非禁用叶子成员

Status: implemented

## 问题

0ff6bd0 引入"全部成员满载按 503+Retry-After 终止"的准入语义：每个成员命中 `errChannelConcurrencyFull`（并发槽位满的本地准入拒绝）一次就记入 `busyRejected`，当记入数达到成员总数时终止请求。但该判定的分母直接取了 `len(group.Items)`，两者口径根本不一致：

- **虚高 → 永不触发**：`group.Items` 会混入引用成员（被引用链解析进别的分组，本身不参与并发准入）、禁用渠道成员（选路阶段即被 `channelDisabledForRouting` 跳过）与冷却成员，它们都不会进入 `busyRejected`。这些成员在组里时，计数永远凑不满 `len(group.Items)`，请求退化成"满载成员每轮白等约 1 秒再重选"的空转，烧到轮次上限后以 400 `errNoAvailableChannels` 收尾——下游把它当非重试错误，与"可重试的 503"意图相反。
- **过小 → 过早 503（引用场景更糟）**：满载分支执行时 `group` 已被解析成叶子分组（`group = hops[len(hops)-1].group`），`len(group.Items)` 只数叶子分组自身成员，漏掉顶层其余尚未尝试的成员。例如顶层 `[ref→sub(单叶满载)、直连健康成员]`，引用目标的叶子满一次后 `len(busyRejected)=1 >= len(sub.Items)=1` 立即 503，健康的直连成员一次都没被尝试就被丢弃——这是比空转更严重的功能回退。

分母脆弱的根因是"全部成员失败"判定的正确口径是**本请求自顶层可达、且真的会参与准入/失败的叶子成员数**，而非某个分组的 `Items` 长度。

同一分母还出现在确定性 4xx 的快失败路径（handler.go `nonRetryable`）：成员命中确定性 400/404/422（或无密钥渠道的 401/403）各记一次、集合按成员去重后与 `len(group.Items)` 比较，满则终止并回 400。该路径与满载路径**同源、同两个失败方向**——禁用/引用成员虚高导致永不终止空转烧轮次，引用链下叶子分组的 `Items` 过小导致引用目标 4xx 一次就提前 400 丢弃顶层健康直连成员。

## 决定

- 新增 `routableLeafCount(top model.Group) int`（route.go）：从顶层分组沿引用链展开，只统计**非引用**且**渠道未禁用**的叶子成员。引用成员递归展开进目标分组；禁用渠道成员跳过；沿链用 `visited` 按分组 ID 去重截断环形/复引用，与转发解析 `resolveGroupRefChainWithContext` 的防环口径一致。
- 满载终止判定改为 `len(busyRejected) >= routableLeafCount(hops[0].group)`（handler.go）：分母从顶层分组（`hops[0].group`）数可达叶子，而非当前叶子分组的 `group.Items`。冷却成员仍计入——冷却到期后会被重试并可能满载，计入可避免过早按 503 终止；禁用成员与引用成员排除，因为它们不可能进入 `busyRejected`。
- 确定性 4xx 快失败路径同样改为 `len(nonRetryable) >= routableLeafCount(hops[0].group)`（handler.go）：与满载共用同一个分母助手与口径，消除同源的两个失败方向。
- 与 `errChannelConcurrencyFull` 的归类修复（`ClassifyError` 把满载哨兵归入 `ErrClassChannelBusy`）同属这批准入语义收口，二者共用"满载是本地准入拒绝而非上游故障"的前提。

## 备选方案

- **只把分母改成"叶子分组内非引用成员数"** — 最强论据是改动最小、不动引用展开；否掉因为它只修引用成员的虚高，禁用/冷却成员仍虚高，且引用场景下叶子分组的 `Items` 仍不含顶层其余成员，过早 503 依旧发生。
- **按顶层成员 ID 记 `busyRejected`、分母取 `len(hops[0].group.Items)`** — 最强论据是计数边界回到顶层、无需沿引用链展开；否掉因为一个引用成员可解析到多个叶子，failover 在引用子树内逐叶推进（`exclude` 沿链透传），按顶层成员去重会在"单引用挂多叶且全满载"时忙集合不再增长而重新空转。
- **不计数，改成"`busyRejected` 非空且下一次 pick 返回空即 503"** — 最强论据是彻底消除分母口径问题、由选路自证不可达；否掉因为 pick 返回空还可能是全冷却、空组、手动模式无目标等合法等待场景，无法可靠区分"全满载"与这些情形，会把这些场景过早打成 503。

## 后果

- **收益**：禁用成员、引用成员、冷却成员不再虚高分母，唯一可用成员满载时立即 503+Retry-After；引用链场景不再因叶子分组 `Items` 过小而丢弃顶层尚健康/未尝试的直连成员。确定性 4xx 快失败同享此收益：禁用成员不挡终止，引用目标 4xx 不再提前 400 丢掉顶层健康直连成员。
- **代价与已知上限**：每次满载/确定性 4xx 命中多一次沿引用链的计数遍历（`groupLookupFunc`/`channelLookupFunc` 均为缓存命中，满载本就低频，开销可忽略）。计数以"当前快照"为口径，与转发解析之间若在单请求内发生分组/渠道配置变更会有一轮快照误差，属既有快照语义内的同源上限，只影响终止与否、不影响是否会错误漏试成员。`refSkips > len(group.Items)`（结构性跳过的退避触发阈值）仍是同类脆口径，但它只影响退避节奏、不影响终态正确性，优先级最低，本次不收口。

## 验证

- `internal/relay/concurrency_admission_test.go` 新增三例：`TestAllChannelsBusyWithDisabledMemberStill503`（组内挂禁用成员时唯一可用成员满载仍 503）、`TestBusyReferenceDoesNotPrematurely503`（引用目标满载时切换到顶层直连成员而非过早 503）、`TestAllChannelsBusyAcrossReferenceChain503`（跨引用链全满载仍 503）。
- `internal/relay/routable_leaf_count_test.go` 钉死分母语义：沿引用链展开数可达叶子、跳过禁用成员、按分组 ID 去重重复/环形引用、空组与全禁用组返回 0。
- `internal/relay/keyless_test.go` 新增三例钉死确定性 4xx 同源收口：`TestDeterministic4xxWithDisabledMemberFailsFast`（禁用成员不计分母立即 400）、`TestDeterministic4xxReferenceDoesNotPrematurelyFail`（引用目标 401 切顶层直连而非提前 400）、`TestDeterministic4xxAcrossReferenceChainFailsFast`（跨引用链全员 401 仍按统一 400 终止）。
- `go build ./...`、`go vet ./...`、`go test ./... -count=1`、`pnpm verify-notes` 全绿；`bash scripts/build.sh --local` 完整构建并本地启动后 `GET /` 返回 200。