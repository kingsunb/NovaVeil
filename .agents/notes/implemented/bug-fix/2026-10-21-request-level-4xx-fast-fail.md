# Agent Note: 请求级确定性 4xx 单次快速失败

Status: implemented

## 问题

NovaVeil 原先把 400/404/422 当作成员级确定性失败: 首个成员失败后仍会按优先级尝试其他成员、Key 与引用链, 直到所有可达叶子都失败一次才终止。超长上下文、请求体结构错误这类拒绝在换渠道后结果不变, 却会把首个真实的 400 埋到后续成员的 401/429 或超时之后, 同时白白消耗请求轮次、冷却等待与上游配额。

opencode2api 最终版在请求级识别确定性拒绝: 只要上游返回 400/422 且正文命中请求级标记, 整个请求立即结束。NovaVeil 需要移植这一语义, 同时保留既有的普通 400 清洗重试, 并维持 401/403/429 的凭据轮换语义。

## 决定

- 判定状态码只接受 400 与 422; 正文标记以 opencode2api `0bb23c7` 的 `isDeterministicClientResponse` 为准: `invalid_request_error`、`invalid_request`、`context_length_exceeded`、`maximum context length`、`string_above_max_length`、`prompt is too long`。413 未包含, 因为上游最终代码没有把它纳入该判定。
- `internal/relay/handler.go` 在普通 400 清洗分支之前增加请求级判定。错误优先从 `*httpclient.Error.Body` 取上游正文, 没有结构化正文时回退到错误文本; 判定不区分透传与协议转换路径, 但只在 OpenCode 兼容渠道(`OpencodeCompat`)生效, 渠道范围收紧的决定与理由见 [4xx 请求级快速失败限定 OpenCode 兼容渠道](2026-10-21-fastfail-scope-opencode-only.md)。
- 400 且请求体存在可清洗字段时, 仍先执行一次 `sanitizeRequestBody` 并重试; 请求体没有变化时直接快速失败, 避免无意义重放。若清洗后的重试仍命中标记, 或首个 400/422 已无法清洗, 立即终止。
- 终止路径沿用既有错误契约: `markFailed` + 错误日志 + `rejectRequest(..., errNoAvailableChannels)`。客户端收到统一 400 `暂无可用渠道`, 不透传上游原始状态与正文; 这是产品契约决策, 不由渠道决定。
- 请求级拒绝不毒化成员健康度: 不调用 `recordRouteFailureIfReal`, 不进入冷却, 不清会话粘合, 只通过 `releaseRefChainHops` 归还探测候选与紧急并发占用。opencode2api 最终版对 Key 路径调用 `MarkSuccess`, 对匿名路径调用 `NoteNeutral`; NovaVeil 统一采用中性语义, 保留既有路由健康状态不动, 这是与上游 Key 路径的差异。
- 401、403、429 不进入请求级判定。多 Key 渠道的认证拒绝与限流拒绝仍按原路径轮换 Key; 无密钥渠道的 401/403 仍按原成员级确定性失败路径终止。

相关历史边界见 [无密钥渠道 401/403 快速失败](2026-09-23-keyless-401-403-deterministic-fast-fail.md) 与 [终止判定分母](2026-09-26-channel-busy-terminal-denominator.md)。

## 备选方案

- **直接复用现有 400/404/422 全成员失败路径** — 最强论据是改动最小、无需新增正文分类; 否掉因为它仍会跨成员、Key 与引用链重放同一个超长请求, 正是本任务要消除的问题。
- **把上游 4xx 状态与正文原样透传** — 最强论据是调用方能直接看到上下文超限原因; 否掉因为 NovaVeil 现有下游契约是所有成员不可用统一返回 400 `暂无可用渠道`, 改状态与正文会扩大对外契约面并可能泄漏渠道响应。
- **把 413 一并纳入快速失败** — 最强论据是 opencode2api README 提到 400/413/422; 否掉因为最终代码的判定函数只接受 400/422, 413 也可能来自本地请求体预算而非上游请求级拒绝, 按代码事实不扩张。
- **所有 400 都在清洗后无条件重试一次再终止** — 最强论据是最大限度保留既有清洗兜底; 否掉因为超长上下文等不可清洗拒绝会被多打一次上游, 与“首个请求级拒绝即结束”的目标直接冲突。当前仅对确有字段被改写的请求保留重试。

## 后果

- 收益: 上下文超限或请求结构错误不再按成员/Key/引用链数量放大上游尝试; 客户端稳定收到确定性 400, 不再被后续 401 等无关错误覆盖。范围仅限 OpenCode 兼容渠道, 其他渠道按成员级故障转移, 见 [4xx 请求级快速失败限定 OpenCode 兼容渠道](2026-10-21-fastfail-scope-opencode-only.md)。
- 代价与已知上限: 判定依赖上游正文标记, 上游若换文案会退回普通成员级失败路径, 不会误判为快速失败; `invalid_request_error` 等通用标记仍可能命中无法修复的参数错误, 但请求级语义要求此时终止。上游 Key 路径会清除 Key 健康惩罚, NovaVeil 采用中性记账, 只保证不再新增失败与冷却。

## 验证

- `internal/relay/deterministic_fastfail_test.go` 覆盖: 400/422 与流式 400 标记命中后首个成员只调用一次、第二个成员零调用且成员阈值设为 1 时仍不冷却; 跨协议转换路径同样生效; 普通 400 清洗后成功; 可清洗的确定性 400 清洗重试一次后仍终止; 401/403/429 仍轮换到第二把 Key; 标记表明确排除 413、401、403、429 与 5xx。
- 交付验证命令: `gofmt -l internal/relay/handler.go internal/relay/deterministic_fastfail_test.go`、`go build ./...`、`go test ./internal/relay`、`go vet ./internal/relay`。
