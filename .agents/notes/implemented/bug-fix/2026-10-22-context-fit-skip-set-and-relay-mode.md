# Agent Note: 上下文过滤跳过集合与同步窗口刷新

Status: implemented

## 问题

分组开启上下文过滤后，689K token 的请求在四个成员间无法推进：前两个成员窗口 200K，后两个 1M。面板看到两个 200K 成员反复轮转，1M 成员始终不承载；同一条 chat 请求还显示为「转换」。

根因有三处：

- `op.ChannelUpdate` 写库后刷新渠道模型缓存时只复制 `Source` 与 `UpstreamProtocol`，同步目录新写入的 `ContextWindow` 仍是旧值。路由因此看不到刚同步的 1M/200K 窗口，重启前都按未知窗口处理。
- `contextFitsWindow` 把窗口 0 当作放行，`effectiveContextWindow` 又只读人工窗口。未知窗口成员会被直接送往上游，再靠 400 排除。
- 跳过成员只写入 `contextSkipped`，实际选路仍只有一个 `exclude`。跳过 A 后选 B，跳过 B 后 `exclude` 指向 B，A 又会被选中，两个 200K 成员形成不触达 1M 的死循环；粘合也可能继续指向被跳过成员。

「转换」不是 `OpencodeCompat` 判定导致的：`supportsNativeFormat` 对 chat→chat 返回 true。问题是请求在 `startRound` 前就被窗口跳过时 `RelayMode` 仍为空，前端按非 `passthrough` 显示为「转换」；分发准备失败路径也没有携带 `Passthrough`。

## 决定

- 窗口来源改为人工值优先、同步目录值兜底；两者都没有时视为未知。上下文过滤开启时，未知窗口成员跳过而不是静默放行。选择跳过而不是放到最后，是为了保证「窗口未知」不会把 689K 请求送给实际只有 200K 的模型；代价是未同步且未手填的成员在过滤期间不可用。
- `contextSkipped` 作为请求级跳过集合传入普通选路、粘合、引用链与紧急兜底。成员一旦因窗口不足被跳过，本请求内不再选中；这比 60 秒计时更严格，也避免并发请求之间共享惩罚。跳过时同步清除该成员的会话粘合。
- `ContextWindow` 的缓存刷新改为整行覆盖当前数据库模型，保证目录同步后的窗口、输出上限与能力字段立即对路由可见。
- 候选选定后立即用 `supportsNativeFormat` 写入 `relay_mode`；正常轮次与分发准备失败轮次共用同一 `Passthrough` 值。`OpencodeCompat` 只影响请求头与模型协议选择，不参与透传标签判定。
- 全部候选都装不下时仍返回 `errContextWindowTooSmall`，并保留请求估算与候选集最大窗口；未知窗口的全未知候选集最大窗口为 0。它与上游返回 400 后才判定的 `errContextWindowExceeded` 区分：前者是筛选阶段本地拒绝，后者是已发起上游后的兜底错误。

## 备选方案

- **未知窗口放到最后再尝试** — 能保留未知成员的可用性；否掉是因为请求已经明确超过 200K，未知成员可能正是 200K，最终仍会把注定失败的请求送上去。
- **继续只信人工窗口** — 改动最小；否掉是同步目录已经提供窗口，用户开启过滤后仍要求逐模型手填，且本次故障正发生在同步值已存在但缓存未刷新的场景。
- **只修单 `exclude`，不引入跳过集合** — 可以让两个成员不再互跳；否掉是引用链、粘合和紧急兜底仍可能绕过排除，且无法表达「本请求内不得再次选中」。
- **用 60 秒计时器控制重复选中** — 贴近报障描述；否掉是单个请求通常远短于 60 秒，请求级集合更简单且没有时钟依赖，后续新请求仍可正常重试该成员。
- **把 `OpencodeCompat` 当作转换条件** — 实现直觉；否掉是它只注入兼容头，不改变报文协议，chat→chat 仍应透传。

## 后果

- **收益**：窗口不足成员在筛选阶段被跳过，200K 成员不再产生上游请求，1M 成员能被正常触达；同步目录窗口立即生效；粘合随跳过后迁移；chat→chat 在成功、失败与取消记录中稳定显示透传。
- **代价与已知上限**：过滤开启时，窗口未知的成员会被排除，管理员需要同步目录或手填窗口才能恢复；请求级跳过集合只覆盖单次请求，不做跨请求缓存；估算密度与边界误差仍存在，上游 400 兜底保留。

## 验证

- `GOTOOLCHAIN=go1.26.7 go test -count=1 ./internal/relay -run 'TestEstimateModelTokensScale|TestContextFitsWindow|TestRelayModeForProtocolMatrix|TestContextFitSkipsSmallWindowsAndReachesLargeWindow|TestContextFitAllSmallWindowsRejectedBeforeDispatch|TestContextFitSkipsUnknownWindowAndUsesSyncedWindow'`
- 四成员用例断言：689K 请求只命中首个 1M 成员，两个 200K 成员上游计数均为 0，粘合迁移到 1M 成员，记录为 `passthrough`。
- 全量候选窗口不足用例断言：返回 `errContextWindowTooSmall` 且所有上游计数为 0；未知窗口用例断言未知成员被跳过、同步 1M 成员承载。

相关历史决策见 [分组级上下文窗口过滤](../feature/2026-10-20-context-fit-routing.md) 与 [上下文窗口手工定义与统一实测密度校准](../feature/2026-10-21-context-window-manual-limits-and-uniform-density.md)。
