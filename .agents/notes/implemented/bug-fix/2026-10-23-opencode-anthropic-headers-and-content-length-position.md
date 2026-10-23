# Agent Note: opencode Anthropic 协议头补齐与 Content-Length 头位置修正

Status: implemented

## Problem

与上游 opencode2api(57b906b) 对比发现 NovaVeil 的 opencode 免费档存在四处请求头偏差, 均可被上游指纹检测识别:

1. **Content-Length 位置**: bun.go 把 Content-Length 写在请求头末尾, 而上游在 common header block 中按 Bun fetch 的固定头序写入。头序是 TLS 指纹之外的 HTTP 指纹维度, 位置偏差有中风险。
2. **x-opencode-client 缺失**: 上游在 v1/v2 两条 lane 均硬编码 `x-opencode-client: cli`, NovaVeil 依赖 builtin 配置间接提供, 非代码保证。
3. **anthropic-version 缺失**: 上游对 Anthropic 协议设 `anthropic-version: 2023-06-01`, NovaVeil relay 层未设。
4. **anthropic-beta 缺失**: 上游 v2 Anthropic 硬编码 `interleaved-thinking-2025-05-14`; v1 Anthropic 从 body 特征(MCP/context-management/skills/task-budget/speed/fallbacks/thinking/tools)派生 beta 列表。NovaVeil 两者均无。

## Decision

### Content-Length 位置 (internal/client/bun.go)

bunRequestHead 的 common header 循环增加 `wroteLength` 标志: 遍历到 `content-length` 且 body 非空时就地写入并置标志, 末尾仅当 `body != nil && !wroteLength` 时兜底写入。与上游 bun.go:538-578 逻辑一致。

### 协议头注入 (internal/relay/opencode_free.go)

applyOpencodeFreeLaneHeaders 在 Accept 之后统一注入 `x-opencode-client: cli`; 当协议为 Anthropic 时设 `anthropic-version: 2023-06-01`。v2 Anthropic 额外设 `anthropic-beta: interleaved-thinking-2025-05-14`。v1 Anthropic 调用 v1AnthropicBetas(request.Body) 从请求体派生 beta 列表, 非空时设 `anthropic-beta`。

### v1AnthropicBetas 派生 (internal/relay/opencode_free.go)

从上游移植 v1AnthropicBetaRequest 结构体、v1AnthropicToolBetas 映射表、claude3OrOlderPattern 正则、v1AnthropicBetas/isPresentJSON/anthropicSupportsStructuredOutput 三个函数。派生顺序与上游一致: MCP→context-management→skills→task-budget→speed→fallbacks→thinking→tools。

## Alternatives considered

- **在 random_headers.go 注入** — 该文件负责关联头(session/affinity/project), 协议头与 lane 强相关, 放 applyOpencodeFreeLaneHeaders 更内聚。
- **v1 anthropic-beta 只设固定值** — 上游无固定 beta, 全部从 body 派生; 设固定值会发送多余标记, 可能触发上游拒绝。

## Consequences

- **收益**: v1/v2 Anthropic 请求头与上游一致, 消除可被指纹检测的偏差; Content-Length 头序匹配 Bun fetch。
- **代价与已知上限**: v1AnthropicBetas 派生依赖 body 的 JSON 结构, 若上游 AI SDK 新增 beta 派生规则需同步更新。anthropicSupportsStructuredOutput 的模型前缀列表需随 Claude 新版本维护。

## Verification

```sh
go build ./internal/client/ ./internal/relay/
go vet ./internal/client/ ./internal/relay/
```
