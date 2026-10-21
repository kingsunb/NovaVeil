# Agent Note: 确定性 4xx 改为逐成员排除

Status: implemented

## 问题

`internal/relay/handler.go` 原先有两套确定性 4xx 语义。普通渠道在 400 清洗、成员失败记账后排除当前成员并继续选路，只有所有可达候选都失败一次才终止；`OpencodeCompat` 渠道命中 400/422 正文标记后却直接拒绝整个请求。混合分组中只要优先级最高的成员是 OpenCode 兼容渠道，后续普通成员即使能服务也拿不到请求，违反了故障转移“尽力在内部消化错误”的目标。

确定性 400/422 的真实语义是“当前成员不接受这个请求”，不是“分组内没有任何成员能接受”。不同渠道、模型和上下文窗口本来就可能有不同限制，因此必须把拒绝范围收回到成员。

## 决定

- 删除 `OpencodeCompat && isDeterministicRequestRejection(err)` 的整请求终止分支。OpenCode 兼容渠道与其他渠道统一走既有 `nonRetryable`、`exclude`、`clearSessionStickyByItem` 与 `continue` 路径，不复制第二份排除逻辑。
- 400 仍保留每成员一次 `sanitizeRequestBody` 机会：确有字段被改写时重试同一成员一次；确定性正文标记但请求体没有变化时不重放，直接进入成员级排除。普通 400 没有命中确定性标记时继续保留原有清洗重试。
- `isDeterministicRequestRejection` 的状态码与正文标记口径不变，仍是 400/422 与既有六个标记；它只负责判断 400 是否存在“清洗也无济于事”的确定性拒绝，不再拥有终止整个请求的权力。
- 某成员一旦进入确定性 4xx 排除集合，本次请求不再重试该成员。终止条件保持不变：`len(nonRetryable) >= routableCandidateCount(hops[0].group)` 时终止；默认以 `errNoAvailableChannels` 返回统一 400，本次请求出现过上下文超限时改用固定提示，见 [全组上下文超限时返回明确错误提示](2026-10-22-context-overflow-message.md)。单成员分组仍在该成员失败后立即终止。
- 成员失败记账、冷却/半开、会话粘合清理、多 Key、403 代理账号换号与 `MemberMaxAttempts` 语义均不改变。OpenCode 成员现在和普通成员一样按真实失败累计路由状态。

原先的请求级终止与渠道范围收紧分别见 [确定性 4xx 标记与请求级终止演进](2026-10-21-request-level-4xx-fast-fail.md) 与 [4xx 快速失败的渠道范围演进](2026-10-21-fastfail-scope-opencode-only.md)；两篇中的整请求终止结论由本笔记取代，标记识别与普通渠道故障转移理由继续有效。

## 备选方案

- **保留 OpenCode 整请求终止，只让普通渠道故障转移**：改动最小，但混合分组仍会被高优先级 OpenCode 成员提前截断，正是本问题要消除的拓扑。
- **为 OpenCode 复制一份逐成员排除逻辑**：可以绕过整请求终止，但会制造第二套状态集合与终止条件，后续修改极易分叉；因此直接复用既有普通渠道分支。
- **把所有确定性 400 都改为不清洗直接排除**：实现最直白，但会丢掉已有的 `stop`、`temperature`、`developer` 角色清洗能力；因此只在确实改写了请求体时重试。
- **所有 400 都无条件清洗重放一次**：会保留现有普通 400 行为，但确定性拒绝且无可清洗字段时仍多打一次上游，也违反混合分组中“该成员只请求一次”的边界；因此只对确有变化的请求重放。
- **首个确定性 4xx 仍立即终止**：能避免任何额外上游尝试，但与分组内不同模型/窗口成员可互补的现实冲突，无法满足跨渠道兜底目标。

## 后果

- **收益**：混合分组中的确定性 400/422 不再被首个 OpenCode 成员一票否决；健康成员可以获得请求。所有成员的排除次数仍以候选数为上限，不会无限空转。
- **代价与已知上限**：全组都确定性拒绝时，客户端会等到每个可达候选各失败一次后才收到统一 400，失败延迟从首个成员扩大到整组；成员级失败记账也会按既有规则累加，可能让被拒绝成员进入冷却。单成员分组与无清洗字段场景仍是立即失败。

## 验证

- `TestDeterministicRequestRejectionContinuesAcrossMembers` 覆盖 OpenCode 成员返回 400/422/流式 400 后普通成员成功，断言一次失败加一次成功轨迹且未达阈值不冷却。
- `TestDeterministic400ExhaustsAllMembersOnce` 覆盖两个 OpenCode 成员均返回确定性 400，断言各只尝试一次并以统一 400 终止；`TestSingleOpencodeDeterministic400SanitizesOnceThenStops` 覆盖单成员清洗重试恰好一次后立即失败。
- `TestConvertedDeterministic400FailsOverToNextMember` 与 `TestDeterministicRequestRejectionFailsOverForOrdinaryChannels` 覆盖协议转换和普通渠道的同一逐成员语义。
- 交付验证：`gofmt -l internal/relay/handler.go internal/relay/deterministic_fastfail_test.go`、`GOTOOLCHAIN=go1.27.3 go build ./...`、`GOTOOLCHAIN=go1.27.3 go test -race ./internal/relay/ -count=1`、`npx -y tsx scripts/agent-notes/verify-agent-note-format.ts`。
