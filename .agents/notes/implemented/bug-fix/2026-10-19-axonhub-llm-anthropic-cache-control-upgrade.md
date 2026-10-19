# Agent Note: 升级 axonhub/llm 拾取 anthropic cache_control 转换改进

Status: implemented

## 问题

NovaVeil 依赖 `github.com/looplj/axonhub/llm`（协议转换库，个人 fork 伪版本，见 [go.mod 个人 fork 伪版本风险](../process/2026-09-20-go-mod-personal-fork-pseudoversions-risk.md)）做 OpenAI/Anthropic/Responses 等协议互转。原钉 `v0.0.0-20261016035026-a946fca495c5`。上游 2026-10-18（`8d3c3865dc64`）在 `transformer/anthropic` 出站转换器修了两处 cache_control 缺口：

1. **跨协议转换请求无缓存**：非 Anthropic 协议（如 OpenAI chat）转 Anthropic 时，统一请求里没有 cache-control 字段可承载，转换后 `anthropicReq.CacheControl` 为 nil，请求不带任何缓存断点，无法利用 Anthropic prompt caching。
2. **Vertex/Bedrock 顶层 cache_control 不被支持**：托管 Anthropic 平台（Vertex、Bedrock）不接受请求顶层的 `cache_control`，只接受内容块上的。原转换器直接把顶层 `cache_control` 透传，托管平台拒绝。

## 决定

将 `github.com/looplj/axonhub/llm` 升至 `v0.0.0-20261018172154-8d3c3865dc64`（`go get` + `go mod tidy`）。改动仅落在 `transformer/anthropic/` 三个文件（`ensure_cache_control.go`、`outbound.go`、`cache_control_test.go`），均为上游内部实现，导出 API 面不变，NovaVeil 无需改调用方。

上游引入两段行为：

- **跨协议转换自动启用 ephemeral 缓存**：`outbound.go` 在 `anthropicReq.CacheControl == nil` 且无显式断点且 `shouldEnableAutomaticCacheControl(llmReq)`（`req.APIFormat != "" && != APIFormatAnthropicMessage`）时，置 `CacheControl = &CacheControl{Type: "ephemeral"}`。纯 Anthropic 透传（`APIFormatAnthropicMessage`）不受影响——只有跨协议转换请求才自动 opt-in。
- **Vertex/Bedrock 顶层 cache_control 重定位**：对 `PlatformVertex`/`PlatformBedrock`，新增 `moveTopLevelCacheControlToLastBlock` 把顶层 `cache_control` 挪到最后一个可缓存块（优先保持 1h TTL 一致性、尊重 `maxCacheControlBreakpoints` 上限）；无法安全放置时返回 `transformer.ErrInvalidRequest`。`ensure_cache_control.go` 同时把 `collectMessageBlockRefs` 重构为基于 `cacheControlSlot` 的抽象，并让 Raw 内容块也参与 cache_control 引用收集（原实现跳过 Raw）。

## 取代检查

- 承接 [go.mod 个人 fork 伪版本风险](../process/2026-09-20-go-mod-personal-fork-pseudoversions-risk.md) 的「axonhub/llm 定期跟进」要求：那篇是依赖风险总账，本篇是具体版本的内容跟进记录，互链不取代。
- 正交 [opencode2api 兼容全面跟进](../architecture/2026-10-19-opencode2api-compat-followup.md)：那篇是 OpenCode 免费档头/传输，本篇是 anthropic 转换器缓存，互不取代。
- 活跃树无 proposed / rejected 覆盖此版本或 cache_control 转换；无完全吸收或需归档的笔记。

## 备选方案

- **不升级（保持 2026-10-16 版）** — 零风险；否掉是因为跨协议转换请求白白放弃 prompt caching（成本/延迟），且 Vertex/Bedrock 顶层 cache_control 仍是潜在 400。用户明确要求跟进。
- **升级后关闭自动缓存（若计费敏感）** — 在 NovaVeil relay 层对转换后的 anthropic 请求剥掉自动注入的 `CacheControl`；否掉是因为该行为仅对跨协议转换生效、纯 Anthropic 透传不动，且 ephemeral 缓存是 Anthropic 官方推荐的降本手段，默认开启利大于弊。若后续发现特定渠道计费异常，再在渠道级配置开关。

## 后果

- **收益**：跨协议（OpenAI 等）转 Anthropic 的请求自动获得 ephemeral prompt caching，降低重复前缀的 token 计费与延迟；Vertex/Bedrock 渠道的顶层 cache_control 不再被平台拒绝（重定位到内容块）。
- **代价与已知上限**：自动缓存仅对 `APIFormat != APIFormatAnthropicMessage` 的转换请求生效，可能轻微增加首次写入缓存的计费（Anthropic 对 cache write 计费）；Vertex/Bedrock 在顶层 cache_control 无法安全重定位时由原静默透传改为返回 `ErrInvalidRequest`——这是行为收紧，极少数边界请求会从「平台 400」变成「网关 400」，错误位置前移。重访信号：Anthropic 调整 ephemeral 缓存计费模型或 Vertex/Bedrock 放开顶层 cache_control 时重评估。
- **供应链**：仍是 looplj 个人 fork 伪版本，无 tag；本版经源码逐文件 diff 审阅（仅 3 文件、均 anthropic transformer 内部）+ `go build ./...` + `go test ./...` 全绿。

## 验证

`go build ./...`、`go test ./...`（含 `internal/relay` 的 `bug1_regression_test`/`prompt_cache_test` 等协议转换测试）均通过。`go.mod` 钉 `github.com/looplj/axonhub/llm v0.0.0-20261018172154-8d3c3865dc64`。上游 diff 仅 `transformer/anthropic/{ensure_cache_control.go,outbound.go,cache_control_test.go}` 三文件，导出 API 面无签名变更。
