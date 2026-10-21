# Agent Note: 4xx 请求级快速失败限定 OpenCode 兼容渠道

Status: implemented

## 问题

[请求级确定性 4xx 单次快速失败](2026-10-21-request-level-4xx-fast-fail.md) 原先对全部渠道生效: 任意上游 400/422 命中正文标记即终止整个请求, 不再尝试分组内其他成员。这对 OpenCode 兼容渠道成立, 但 NovaVeil 的分组可以混入普通渠道: 普通上游的 `invalid_request_error` 常是渠道或模型级参数差异, 换一个宽成员可能成功; 上下文类标记在窗口不同的渠道之间也可能换成员即可服务。统一终止会让这些渠道失去故障转移机会, 与「尽量不把可内部处理的错误抛给下游」的产品原则冲突。

## 决定

- 请求级 4xx 快速失败只在 OpenCode 兼容渠道生效: `channel.OpencodeCompat` 为 true 时, `internal/relay/handler.go` 的判定分支才执行; 6 个正文标记全部适用这个范围。
- 其他渠道的同一上游响应恢复为成员级确定性失败: 400 仍先清洗重试一次, 422 不做清洗重试; 随后计成员失败、清该成员粘合、切换下一成员, 全部候选失败才返回统一 400 `暂无可用渠道`。
- OpenCode 兼容渠道的内部语义不变: 命中后整请求终止, 不记成员失败、不冷却、不清粘合, 只整链归还探测候选与紧急额度; 400 且请求体确有可清洗字段时仍先清洗重试一次, 仍命中才终止。
- 渠道范围只认 `OpencodeCompat`: 用户与 API 创建的渠道在 `op.ChannelCreate` 时被强制关闭该标记, 因此语义只覆盖内置 OpenCode 渠道, 不按 BaseURL 猜测。

## 备选方案

- **维持全局生效**: 改动最小, 且超长上下文换渠道确实无法成功; 否掉是因为通用标记 `invalid_request_error`/`invalid_request` 在普通渠道常代表可修复的参数差异, 统一终止会把可服务的请求变成 400, 违背「尽量内部处理」原则。
- **只把两个通用标记限定 OpenCode, 上下文类标记保持全局**: 能保住超长上下文的快速失败; 否掉是因为上下文类标记在窗口不同的渠道之间同样可能换成员即可服务, 混组普通渠道仍会被提前截断。
- **全部回退成员级失败, OpenCode 也不再快速失败**: 行为最保守; 否掉是会丢掉 OpenCode 上游最终版的请求级语义, 超长请求仍会被后续成员的 401/429 掩盖。
- **按 BaseURL 或渠道类型识别 OpenCode**: 能覆盖自建 OpenCode 地址; 否掉是因为 `OpencodeCompat` 已是既有单一事实源, 用户自建渠道本就不注入 OpenCode 兼容行为, 按地址猜测会扩大生效范围。

## 后果

- **收益**: 普通渠道与混合分组恢复既有故障转移, 上游参数类 400 不再整请求终止; OpenCode 渠道仍保持与上游最终版一致的请求级语义。
- **代价与已知上限**: 混合分组中 OpenCode 成员命中标记时仍会终止整个请求, 组内普通成员不会补试, 该拓扑下请求级语义优先于跨渠道兜底。`OpencodeCompat` 只由内置渠道持有, 自建 OpenCode 地址的渠道不会获得快速失败。

## 验证

- `internal/relay/deterministic_fastfail_test.go` 的 OpenCode 用例经 `createOpencodeIntegrationChannel` 构造, 断言首个成员命中后第二成员零调用; 新增 `TestDeterministicRequestRejectionScopedToOpencode` 用普通渠道断言 400 清洗重试一次后切换成员、422 直接切换成员并最终成功。
- 用户执行: `gofmt -l internal/relay/handler.go internal/relay/deterministic_fastfail_test.go`、`go build ./...`、`go test ./internal/relay -run 'TestDeterministicRequestRejection|TestSanitizableDeterministic400|TestConvertedDeterministic400|TestOrdinary400|TestCredentialAndRateLimit|TestIsDeterministicClientResponse'`、`go vet ./internal/relay`、`pnpm verify-notes`。
