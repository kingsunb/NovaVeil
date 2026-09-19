# Agent Note: 诊断、评估与半开探测路径按网络错误重试策略容忍基础设施错误

Status: implemented

## 问题

真实转发主循环对基础设施层错误（`upstream_network`：代理/DNS/TLS/连接重置/连接提前中断）按 `MemberInfraMaxRetries` 独立计数、未达阈值即在等待后重试同一成员；但五条诊断/评估/探测路径对上游都是**单发**调用，网络错误直接定结论：

- 面板单模型测试（`TestChannel` → `sendChannelTestRequest`）；
- 模型评估（`TestChannelKeyFailover` → `sendChannelTestRequest`，评估队列与手动评估共用）；
- 面板逐密钥测试（`TestChannelKeys` → `sendKeyTestRequest`）；
- 分组测试（`TestGroup` → `testGroupMember`）；
- 半开/后台探测（`probeChannel`，结论直接决定成员恢复 CLOSED 还是重新 OPEN 且冷却等级 +1）。

生产样本：非流式透传路径上 axonhub `HttpClient.Do` 返回 `HTTP request failed: Post "https://api.stepfun.com/step_plan/v1/chat/completions": unexpected EOF`（`url.Error` 包装 `io.ErrUnexpectedEOF`，连接在收到 HTTP 响应前中断）。该错误被正确归类为 `upstream_network`，但日志里仅 1 次尝试即终态——管理员在分组路由策略配置的「网络错误重试 3 次」对诊断路径完全不生效，一次瞬时抖动即把渠道/密钥/评估模型判死，或把恢复中的成员重新打入更长冷却。

## 决定

`internal/relay/test.go` 新增 `sendDiagnosticUpstream(ctx, send)` 作为全部诊断/评估/探测上游调用的统一包装，现在时语义：

- **重试判定**：`isRetryableDiagnosticError` = `isInfrastructureError` 且错误不是 `context.Canceled`/`context.DeadlineExceeded`。上下文取消/超时表示调用方已放弃或时间预算耗尽，重试只会把等待拉长 N 倍。
- **重试次数**：`diagInfraRetryLimit()` 取 `MemberInfraMaxRetries` 出厂默认值（3），计数语义与路由层一致（阈值含首次失败，达到即止），即最多发起 3 次上游调用。
- **重试间隔**：`diagInfraRetryInterval` 默认 2s（对齐默认 `MemberRetryIntervalSeconds`），定义为包级变量仅为测试可缩短；等待期间 `ctx.Done()` 立即放弃并返回最后一次错误。
- **覆盖调用点**：`sendChannelTestRequest`、`sendKeyTestRequest`、`testGroupMember`、`recovery.go` 的 `probeChannel`。四处的透传/转换分派逻辑收进闭包传入，分派本身不变。
- **不读具体分组配置**：渠道可属多个分组，评估/单渠道测试没有分组语境，统一用出厂默认值；业务错误（4xx/5xx/限流/响应校验失败）一律不重试。
- **主循环不经过此包装**：`handler.go` 转发循环保持既有路由层重试语义（成员级计数、冷却、故障转移），两者口径独立。

## 备选方案

- **让诊断走完整分组路由循环** — 最强论据是完全复用既有重试/冷却/熔断策略，无平行实现；否掉因为诊断是主动运维动作，有意绕过冷却记账、计费落库与成员选择（评估需逐 Key 探测并绕过冷却、探测指定单个成员），套进路由循环会改变这些核心语义，且路由循环面向流式/非流式真实客户端请求，形体完全不匹配。
- **按成员所属分组的 `MemberInfraMaxRetries` 重试** — 最强论据是严格对齐管理员显式配置的「网络错误重试次数」；否掉因为一个渠道可属多个分组（配置可能互相矛盾），评估与单渠道测试天然无分组语境，硬造归属会引入歧义；出厂默认 3 与当前实际配置一致，且探测/评估是低频动作，默认值偏离个别分组配置的代价可忽略。
- **业务错误也重试** — 否掉：4xx/5xx/限流的重试不可能改变结果，且评估场景烧计费请求正是 [model-eval-key-attempt-limit](2026-09-19-model-eval-key-attempt-limit.md) 要遏制的问题。

## 后果

- **收益**：瞬时网络抖动不再把渠道测试/逐密钥诊断/模型评估直接判死，日志里的诊断条目在基础设施抖动下会呈现多次尝试轨迹；半开/后台探测对抖动上游不再误判「未恢复」而指数加重冷却；诊断容错口径与分组路由对真实流量的口径一致，管理员配置的「网络错误重试」心智模型成立。
- **代价与已知上限**：连续基础设施失败时诊断耗时最多增加 2 次重试间隔（约 4s）加两次上游快速失败耗时；逐密钥测试的 60s 单 Key 预算由全部尝试共享（挂死上游吃满预算时不会重试，超时语义优先）；评估 10 分钟预算下最坏 10 把 Key × 3 次快速失败仍在预算内。重访信号：若出现「诊断重试放大了对已死渠道的请求量」的案例，可评估把 `diagInfraRetryLimit` 暴露为设置项。
- **口径边界**：重试只覆盖非流式诊断调用；探测仍不写日志流条目、逐密钥测试仍不落失败摘要，可观测性行为不变。

## 验证

`internal/relay/diag_retry_test.go`：`TestSendDiagnosticUpstreamRetriesInfraError`（基础设施错误重试至成功）、`TestSendDiagnosticUpstreamStopsAtLimit`（达上限返回最后一次错误）、`TestSendDiagnosticUpstreamDoesNotRetryBusinessError`（429 不重试）、`TestSendDiagnosticUpstreamDoesNotRetryContextError`（上下文超时不重试）、`TestSendDiagnosticUpstreamStopsWhenContextCanceledDuringWait`（等待期间取消立即放弃）。`internal/relay/infra_error_test.go` 补入生产样本分类用例（`url.Error` 包装 `io.ErrUnexpectedEOF` → 基础设施错误）。`pnpm verify-notes` 与 `go vet ./...`、`go test ./...` 通过。

## 关联

- [渠道测试探针改用代表客户端协议](2026-09-19-channel-test-probe-client-format.md)：同一批发送函数（`sendChannelTestRequest`/`sendKeyTestRequest`）的上一次对齐，本篇不动其协议语义。
- [模型评估密钥尝试上限](2026-09-19-model-eval-key-attempt-limit.md)：评估烧计费请求的关切来源，本篇重试仅限基础设施错误与之兼容。
