# 下游错误响应契约

本文说明 `/v1/*` 转发接口失败时，网关返回给下游客户端的响应格式、状态码与提示文案。事实以代码为准：请求级写回见 `internal/relay/handler.go` 的 `rejectRequest`，上游轮次失败见 `internal/relay/protocol.go` 与 `internal/relay/upstream.go`。

## 统一响应格式

- 错误体由客户端协议转换器 `inbound.TransformError` 生成：Chat 为 `{"error":{"message":"...","type":"invalid_request_error"}}`，Anthropic 与 Responses 按各自协议结构返回。
- 非流式请求直接返回 JSON 错误体；流式请求只有在**首个字节写出之前**失败才返回 JSON，流已提交后的失败不再返回错误体。
- `Retry-After` 只用于 503。

## 状态码规则

| 场景 | 状态码 | Retry-After |
|---|---|---|
| 默认请求级错误与上游最终失败 | 400 | 无 |
| 请求体超过上游限制（上游 413） | 413 | 无 |
| 管理端一键停止、渠道并发已满 | 503 | 5 秒 |
| WARP 槽位全部不可用 | 503 | 槽位剩余冷却秒数（至少 1 秒） |

**上游的 429 / 5xx 不会原样透传给下游。** 除上表 503 场景外，最终失败统一按 400 返回，上游状态只出现在错误文本里（例如 `...: 429 ...`）。WARP 的 503 同样是网关生成的「槽位忙」信号，不是上游状态码透传。

## 网关生成的提示

| 下游提示 | 触发条件 |
|---|---|
| `服务器过载，请稍候` | 管理端停止全部请求（`errAllRequestsStopped`） |
| `渠道并发已满，请稍候重试` | 分组渠道并发占满（`errAllChannelsBusy`） |
| `WARP 槽位全部不可用，请稍候重试` | WARP 池无空闲槽位（`warpSlotsBusyError`） |
| `暂无可用渠道` | 分组内没有可用成员 |
| `请求上下文超出模型上限，请减少输入或提高模型窗口` | 上游判定上下文长度超限 |
| `请求体超过上游限制（413），请减小上下文后重试` | 上游返回 413 |
| `请求尝试轮次超限` | 超过分组 `MemberMaxAttempts` |
| `请求总时长超限` | 超过分组请求总时长 |
| `等待成员流式首个事件超时(N 秒)` | 流式首事件超时 |
| `等待成员非流式完整响应超时(N 秒)` | 非流式完整响应超时 |
| `脱敏失败, 拒绝放行明文: <原因>` | 脱敏执行失败，拒绝放行明文 |
| `WARP 代理地址无效` | WARP 渠道代理地址无效 |
| `WARP 代理模板必须把 {account} 放在用户名` | WARP 代理模板配置错误 |
| `title generation request rejected by group policy` | 标题生成请求被分组策略拒绝 |
| `model not found` / `model not supported by this api key` | 模型不存在，或 Key 无权使用 |

## 上游轮次失败

所有渠道都失败后，最后一条错误作为 `message` 返回，通常对应 400。

| 下游提示 | 触发条件 |
|---|---|
| `upstream reported zero output tokens: output tokens reported as 0` | 上游上报输出 0，且终止原因不在合法空输出白名单 |
| `upstream finished with reasoning only and no answer content: finish_reason stop with reasoning_content only` | 只有推理内容、没有正文就停止 |
| `upstream stream closed before the terminal event` | 已产生内容，但在协议终止事件之前断流 |
| `upstream stream ended before any content or terminal event` | 未产生任何内容或终止事件就断流 |
| `upstream reported abnormal finish reason: finish_reason "..."` | 异常终止原因，如 `length`、`content_filter` |
| `upstream stream idle timeout exceeded` | 流式空闲超时 |
| `upstream stream window budget exceeded` / `upstream stream budget exceeded` | 超出事件数或字节预算 |
| `upstream stream ended without events` | 有效性窗口内没有任何事件 |
| `upstream response is empty` / `upstream 200 response has neither choices nor usage` | 上游 200 但响应体为空或缺少必要字段 |
| `<上游错误文本>: <响应体片段>` | 上游 4xx / 5xx，片段上限 8 KiB（`errBodySnippetLimit`） |
| Go 原始网络错误文本（`dial tcp ...`、`context deadline exceeded`、`unexpected EOF` 等） | 连接失败、超时、断流 |

## 流式已提交之后

流已经向前写出后发生的失败**不会补发 error 事件**，而是直接结束 SSE，缺少 `[DONE]`、`message_stop` 等终止事件。这样 Codex、opencode 等 Agent CLI 会按「流未完成」自动重连重试；服务端仍按失败定稿、记录错误日志并累计成员连击。

## 观测

失败请求会写入错误日志：`ErrClass`（错误分类）、`ErrBrief` 与 `ErrDetail`（提示文本）、请求体与脱敏命中明细。同类错误只保留最近 3 条，避免同一问题刷屏挤掉其他类型。