# Agent Note: 全组上下文超限时返回明确错误提示

Status: implemented

## 问题

[确定性 4xx 改为逐成员排除](2026-10-22-fastfail-per-member-scope.md) 落地后，请求会逐个排除确定性拒绝的成员，直到可达候选耗尽。终止时所有原因都被压成 `errNoAvailableChannels`，客户端只看到 `400 暂无可用渠道`。当全部候选都因上下文超限失败时，这个文案没有告诉调用方真正可行动的方向：减少输入或换用更大窗口的模型；同时上游原始正文可能包含渠道信息与 request id，不能直接透传。

## 决定

- `Forward` 在一次请求内维护局部布尔标记 `contextOverflowSeen`，不跨请求共享。确定性 4xx 到达时用既有 `looksLikeContextOverflow` 与 `contextOverflowHints` 判断是否属于上下文超限，不新增第二套探测关键词。
- 标记口径是“本次请求出现过”，不要求所有失败成员同一原因。混合分组中一个成员上下文超限、另一个成员普通确定性 400 时，最终仍返回上下文超限提示。
- 候选耗尽分支仍按 `len(nonRetryable) >= routableCandidateCount` 终止；标记为真时用新哨兵 `errContextWindowExceeded` 调用 `rejectRequest`，否则继续使用 `errNoAvailableChannels`。
- `errContextWindowExceeded` 的固定文案是 `请求上下文超出模型上限，请减少输入或提高模型窗口`。`rejectRequest` 默认状态码为 400，因此无需新增状态映射；上游原始错误正文、渠道信息与 request id 均不进入客户端响应。
- 清理重试、401/403 换号、Key 轮换、冷却/半开、其它确定性 4xx 的失败记账与排除语义保持不变；OpenCode 兼容渠道与普通分组走同一终态文案。

相关历史与范围见 [确定性 4xx 标记与请求级终止演进](2026-10-21-request-level-4xx-fast-fail.md) 与 [4xx 快速失败的渠道范围演进](2026-10-21-fastfail-scope-opencode-only.md)。

## 备选方案

- **透传上游 4xx 原始正文**：调用方能直接看到完整限制信息，但会泄漏渠道实现、request id 等内部信息，且不同上游文案不可控；本次只给固定中文提示。
- **只有全部成员都因上下文超限才提示**：能避免混合原因下的归因偏差，但用户已拍板“出现过即提示”，否则只要一个成员因字段错误失败，就会丢掉真正可行动的上下文原因。
- **为请求路径新增独立超限标记函数**：可完全贴合本分支，但会与 `context_probe.go` 的容量探测漂移出两套关键词；因此直接复用 `looksLikeContextOverflow`。
- **把所有确定性 4xx 都映射成各自的专用提示**：信息更细，但会扩大下游错误契约并增加逐状态维护成本；除上下文超限外的确定性 4xx 继续统一返回 `暂无可用渠道`。

## 后果

- **收益**：全组上下文超限时，客户端收到 400 与固定的可行动提示，不再误以为只是渠道暂时不可用；原始上游正文仍不暴露。
- **代价与已知上限**：混合原因会按“出现过”优先报上下文超限，可能掩盖同批中的其它确定性 4xx；只有候选耗尽分支应用该提示，轮次/时长耗尽等路由层终止仍返回 `暂无可用渠道`。

## 验证

- `TestAllMembersContextOverflowReturnsOverflowMessage` 覆盖两个成员均上下文超限，断言各请求一次、最终 400、含固定提示且不含 `暂无可用渠道` 与上游正文。
- `TestMixedContextOverflowReturnsOverflowMessage` 覆盖上下文超限与普通确定性 400 混合；`TestNonOverflowDeterministic4xxKeepsNoAvailableChannels` 防止普通 4xx 误报超限。
- `TestSingleOpencodeContextOverflowSanitizesAtMostOnce` 覆盖单成员 OpenCode 分组的清洗重试最多一次并立即返回超限提示。
- 交付验证：`gofmt -l internal/relay/handler.go internal/relay/context_overflow_message_test.go internal/relay/deterministic_fastfail_test.go`、`GOTOOLCHAIN=go1.27.3 go test ./internal/relay/ -run 'Deterministic|FastFail|4xx|Sanitize|Overflow' -count=1`、`GOTOOLCHAIN=go1.27.3 go test -race ./internal/relay/ -count=1`、`npx -y tsx scripts/agent-notes/verify-agent-note-format.ts`。
