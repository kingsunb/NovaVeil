# Agent Note: 4xx 快速失败的渠道范围演进

Status: implemented

## 问题

[确定性 4xx 标记与请求级终止演进](2026-10-21-request-level-4xx-fast-fail.md) 原先对全部渠道生效: 任意上游 400/422 命中正文标记即终止整个请求, 不再尝试分组内其他成员。这对 OpenCode 兼容渠道成立, 但 NovaVeil 的分组可以混入普通渠道: 普通上游的 `invalid_request_error` 常是渠道或模型级参数差异, 换一个宽成员可能成功; 上下文类标记在窗口不同的渠道之间也可能换成员即可服务。统一终止会让这些渠道失去故障转移机会, 与「尽量不把可内部处理的错误抛给下游」的产品原则冲突。

## 决定

- `OpencodeCompat` 不再决定确定性 4xx 的终止范围。OpenCode 兼容渠道与普通渠道统一走成员级排除: 400/404/422 以及无密钥渠道 401/403 只排除当前成员, 全部可达候选各失败一次后才返回统一 400 `暂无可用渠道`。
- 普通渠道的成员级故障转移理由现在同样适用于 OpenCode 渠道: 上游对请求的接受程度受模型、窗口与渠道参数影响, 首个成员拒绝不能证明后续成员无解。完整决定见 [确定性 4xx 改为逐成员排除](2026-10-22-fastfail-per-member-scope.md)。
- 400 仍保留每成员一次清洗: 确有字段被改写时重试同一成员一次, 确定性标记且无变化时直接排除。422 不做清洗重试。
- 确定性拒绝按既有成员失败语义记账, 达 `MemberMaxAttempts` 后冷却并清理会话粘合; 不再申请 OpenCode 专属的中性健康状态。
- `OpencodeCompat` 继续只用于 OpenCode 协议、会话头、免费档与代理账号等既有渠道能力, 不按 BaseURL 猜测。

## 备选方案

- **维持 OpenCode 专属整请求终止**: 可以保留上游请求级语义并让首个 400 最快返回; 否掉是因为混合分组会被高优先级 OpenCode 成员提前截断, 组内普通成员或其他模型/窗口成员无法补试。
- **只把两个通用标记限定 OpenCode, 上下文类标记保持全局**: 能保住部分快速失败; 否掉是因为上下文类标记在窗口不同的渠道之间同样可能换成员即可服务。
- **全部回退成员级失败, OpenCode 也不再做确定性识别**: 行为最保守; 否掉是会让无清洗价值的 400 在同一成员上无意义重放, 因此保留标记识别, 只取消其整请求终止权力。
- **继续由 `OpencodeCompat` 区分范围**: 该标记是现成事实源, 但请求接受能力本质属于成员而非渠道兼容模式; 按渠道类型分流会再次制造两套终止语义。

## 后果

- **收益**: 所有渠道拓扑使用同一终止规则, 混合分组可以从后续成员成功; 不再存在“OpenCode 成员一票否决”的隐式边界。
- **代价与已知上限**: 全组拒绝时最终错误会晚于首个 4xx 返回, 且每个候选都会消耗一次尝试; 被拒绝成员按既有阈值累计失败, 可能进入冷却。判定与清洗能力不变, 标记文案变化仍会退回普通 400 路径。

## 验证

- `TestDeterministicRequestRejectionContinuesAcrossMembers` 用 OpenCode 成员构造 400/422/流式 400, 断言请求切换到后续普通成员并成功。
- `TestDeterministic400ExhaustsAllMembersOnce` 与 `TestSingleOpencodeDeterministic400SanitizesOnceThenStops` 分别覆盖全组排除与单成员立即终止; `TestDeterministicRequestRejectionFailsOverForOrdinaryChannels` 保留普通渠道对照。
- 交付验证: `GOTOOLCHAIN=go1.27.3 go test ./internal/relay/ -run 'Deterministic|FastFail|4xx|Sanitize' -count=1`、`GOTOOLCHAIN=go1.27.3 go test -race ./internal/relay/ -count=1`、`npx -y tsx scripts/agent-notes/verify-agent-note-format.ts`。
