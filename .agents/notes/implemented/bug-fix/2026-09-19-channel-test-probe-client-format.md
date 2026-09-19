# Agent Note: 渠道测试探针改用代表客户端协议对齐真实转发路径

Status: implemented

## 问题

渠道管理页的「单模型测试」与「逐密钥测试」探针此前以 `channelNativeFormat(channel.Type)` 决定 `format`——即按渠道上游协议构造请求。真实转发（`handler.go` 的 `Forward`）的 `format` 来自客户端请求协议（`/v1/chat/completions` → `openai_chat`）。当客户端协议 ≠ 渠道原生格式时（典型场景：客户端发 `openai_chat`、渠道是 anthropic/openai_responses），探针与真实转发在三处分歧：

1. **透传/转换判定**：`buildOutbound(channel, format)` 的 `passthrough = supportsNativeFormat(channel, format)`。探针 format=渠道原生 → 几乎总为 true → 走 `sendPassthrough`；真实转发 format=openai_chat → 走 `sendConverted`。校验口径与响应处理路径不同。
2. **完全透传 `raw.Path`**：`buildPassthroughRequest` 完全透传时用 `raw.Path`。真实转发的 `raw.Path` 来自客户端请求（如 `/v1/chat/completions`）；探针的 `raw` 由 `newTestRequest` 构造、`Path` 为空 → 回退 `upstreamPath(format)`，路径不一致。
3. **响应提取**：探针走透传时响应是渠道原生格式；修复后走转换时响应转回 `openai_chat` 格式，`extractMessageContent` 提取 `choices.0.message.content`。format 分歧时提取路径与响应格式不匹配，`content` 可能为空。

后果是：面板测试对 anthropic/openai_responses 等渠道可能把有效回复误判失败、或回复文本提取为空，且日志流的 `client_format` 与真实请求不一致，无法横向对比。

## 决定

引入包级常量 `testProbeClientFormat = llm.APIFormatOpenAIChatCompletion`（`internal/relay/test.go`），作为单模型/逐密钥测试探针 `format` 的单一事实源。`sendChannelTestRequest` 与 `sendKeyTestRequest` 的 `format` 改用该常量，并在 `newTestRequest` 构造的 `raw` 上增设 `raw.Path = "/v1/chat/completions"`（代表客户端入站路径）。

- **路径对齐**：`buildOutbound(channel, testProbeClientFormat)` 的 passthrough 判定与真实转发（客户端发 openai_chat）逐渠道一致——openai 渠道与完全透传渠道为 true，anthropic/openai_responses/gemini/volcengine 为 false。
- **完全透传 `raw.Path`**：`buildPassthroughRequest` 在完全透传渠道下 `strings.TrimPrefix(raw.Path, "/v1")` 得到 `/chat/completions`，与真实转发一致；`##` 标记的原始地址渠道直接用 base，不受影响。
- **日志流对齐**：`clientFormatLabel(testProbeClientFormat)` = `openai_chat`，`relayMode` 随 passthrough 判定自然取 `passthrough`/`converted`，与真实转发同源。
- **`channelNativeFormat` 保留**：剩余唯一调用方为 `testGroupMember`（分组测试，spec 非目标），不删除、不改签名。
- **`newTestRequest` 签名不变**：`Path` 设置职责归调用方，不在 `newTestRequest` 内部硬编码。

## 备选方案

- **让前端测试按钮可选协议** — 最强论据是管理员可显式指定待测客户端协议，覆盖更多场景；否掉因为前端测试按钮无协议选择项，管理端自测等价于客户端以 `openai_chat` 入站（最通用入口协议），引入协议选择会增加前端暴露面与交互复杂度，超出本次修复范围。
- **改 `newTestRequest` 签名注入 `Path`** — 最强论据是把 Path 与报文体构造收拢到一处；否掉因为 `Path` 是调用方语境（代表客户端入站路径），`newTestRequest` 只负责按 format 构造报文体，职责分离更清晰；且 `newTestRequest` 还被 `testGroupMember` 调用，改签名会迫使其也传 Path，越界分组测试非目标。
- **同时改 `testGroupMember`** — 最强论据是分组测试探针有同样的路径分歧；否掉因为分组测试是独立诉求（spec 非目标 8），本次修复聚焦面板单模型/逐密钥测试，分组测试另案处理，避免一个 PR 越界扩大改动面。

## 后果

- **收益**：单模型/逐密钥测试探针的上游 URL 路径、透传/转换判定、响应校验口径、日志流 `client_format`/`relay_mode` 与真实转发（客户端发 openai_chat）逐渠道一致；anthropic/openai_responses 等渠道的面板测试不再因路径分歧误判失败或提取空回复。
- **代价与已知上限**：探针固定以 `openai_chat` 作为代表客户端协议，若未来出现「客户端以 anthropic/openai_responses 入站、且该入站协议下的路径/校验行为与 openai_chat 入站有实质差异」的真实场景，探针无法覆盖该差异。重访信号：出现「某渠道面板测试通过但客户端以非 openai_chat 入站时真实转发失败」的真实案例时，可评估把探针 format 做成按待测入站协议可选。

## 验证

`internal/relay/test_path_test.go` 新增路径一致性单测：`TestSendChannelTestRequestPathConsistency` 表驱动覆盖 openai/anthropic/openai_responses/gemini/volcengine 五个渠道类型，用 `httptest.Server` 捕获 `r.URL.Path` 断言与真实转发路径一致；`TestBuildOutboundPassthroughConsistency` 断言 passthrough 判定与 design 路径一致性对齐决策表逐行一致；`TestSendChannelTestRequestPassthroughPath`/`TestSendChannelTestRequestRawURLPath` 覆盖完全透传与 `##` 标记两条边界；`TestSendChannelTestRequestClientFormat` 断言日志流 `ClientFormat=="openai_chat"` 且 `RelayMode` 与路径一致；`TestExtractMessageContentProtocolCompatibility` 覆盖三种协议响应提取。`internal/relay/keytest_test.go` 新增 `TestSendKeyTestRequestPathConsistency` 断言逐密钥探针路径与单模型探针一致。存量测试均使用 openai 渠道、不按路径分支，无需修正。
