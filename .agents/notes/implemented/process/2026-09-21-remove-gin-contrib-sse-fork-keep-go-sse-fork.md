# Agent Note: 移除 gin-contrib/sse 个人 fork 并保留 go-sse fork 补齐

Status: implemented

## 问题

DEV-04 残余项指出 `go.mod` 中有两个指向个人 fork 伪版本的 `replace`：
`gin-contrib/sse => github.com/looplj/sse` 与
`tmaxmax/go-sse => github.com/looplj/go-sse`。前一篇
[2026-09-20-go-mod-personal-fork-pseudoversions-risk](./2026-09-20-go-mod-personal-fork-pseudoversions-risk.md)
当时的决定是“本次不换源”，并要求在逐项核对 fork 差异后各自处理。

## 决定

- **`gin-contrib/sse` 切回官方 `v1.1.2`，删除个人 fork replace。** 差异核对结论：官方 `v1.1.2` 与 `looplj/sse` fork 的唯一实质代码差异是 SSE 数据行写成 `data:<payload>`（官方）还是 `data: <payload>`（fork，冒号后多一个空格）；另一个差异 `reflect.Pointer` vs `reflect.Ptr` 不改变行为。两者都是合法 SSE 编码，浏览器 `EventSource` 兼容。因此仓库不再需要为该 fork 承担供应链风险。
- **`tmaxmax/go-sse => github.com/looplj/go-sse` 继续保留。** 差异核对结论：官方 `v0.11.0` 缺少 `github.com/looplj/axonhub/llm` 的 `httpclient/decoder.go` 使用的 `Stream` / `NewStreamWithConfig` API；直接切回官方版本无法通过编译。`go.mod` 中已就地注释该原因，待上游官方补齐 API 后切回。
- **`github.com/looplj/axonhub/llm` 保留伪版本**：该模块无 tagged versions（`go list -m -versions` 为空），且是仓库实际使用的 LLM 适配层。伪版本随上游 `unstable` 分支跟进，当前 pin `v0.0.0-20261005144853-5c78997798b4`（2026-10-05 HEAD）。该 pin 相对前一 pin `276ee9fcc84f`（2026-09-28，纯前端 logo/登录页改动，未触碰 `llm/transformer`）含 14 个 `llm/transformer` 提交，以 bug 修复为主，多个直接命中 NovaVeil 调用路径（NovaVeil 在 `handler.go`/`upstream.go`/`opencode_free.go` 三处调用 `AggregateStreamChunks`）：#2561 修复聚合器吞掉 refusal/audio/logprobs/reasoning（模型 refusal 被当空 assistant 消息 + `finish_reason:"stop"` 返回）、#2572 修复 citations 泄漏到内部 `transformer_metadata`、#1872 修复 Anthropic 无参 function tool 缺 `input_schema` 致 422、#2582/#2035 responses transformer 修复、#2611 用量解析接受数字字符串、#2539 anthropic/gemini strict 工具设置保留、#2570 流终止/`finish_reason` 判定重构（统一 outcome reducer）、#2332 新增 MiniMax 图片生成（加性新子包）。无硬性 breaking change；主要行为风险是 #2570 流终止判定重构，已由 `internal/relay` 回归测试覆盖。
- 对应的 SSE 线格式断言与前端文档同步为 `data:<payload>`（上游格式）。

## 备选方案

- 保持两个 fork 不动：依赖列表风险继续存在，且 `gin-contrib/sse` fork 的差异已确认只是空格，没有保留理由。
- 为 `tmaxmax/go-sse` 在仓库内写本地 shim 再切回官方：会增加自维护的 SSE 客户端代码，比保留一个经过编译验证的 fork 更糟；未选。
- 将 fork 的 Stream 补齐提交上游后切回：这是正确终态，但需要上游发布窗口，不作为本次阻塞项。

## 后果

- **收益**：消除一个可用官方发行版替代的个人 fork，缩小 DEV-04 的暴露面；`go.mod` 中剩余 fork 均有就地说明。
- **代价与边界**：SSE 线上输出从 `data: xxx` 变为 `data:xxx`。该格式合法；仓库内流式断言与 `web-next/src/lib/sse.ts` 注释已同步。
- **触发条件明确**：`github.com/tmaxmax/go-sse` 官方发布含 `Stream`/`NewStreamWithConfig`（或 `axonhub/llm` 不再需要）时，删除该 replace；`looplj/axonhub/llm` 发布 tagged version 时替换伪版本。

## 验证

- `go build ./...` 通过。
- `go test -count=1 ./...` 通过（切换官方 sse 后同步更新 `internal/relay/canned_test.go` 的 `data:[DONE]` 断言）。
- `cd web-next && pnpm typecheck && pnpm lint && pnpm vitest run` 通过。
- `gosec -quiet -severity=high -exclude=G115 ./...` 通过。
- `govulncheck ./...` 通过。
- `axonhub/llm` 当前 pin `5c78997798b4`（2026-10-05）：`go build ./...`、`go vet ./...`、`go test -count=1 ./...` 全绿；`go.sum` 净 `+2/-2`（axonhub 校验和替换）。go-sse fork replace 无需变化。`golang.org/x/time` 为 direct（新版 llm 直接 import）。