# Agent Note: OpenCode 免费档新客户端指纹对齐

Status: implemented

## 问题

真实 OpenCode 客户端抓包显示，NovaVeil 免费档仍有五处可观测差异：v2 路径缺少 `/openai` family 段、v2 `Accept` 不是 `*/*`、v2 缺少 `b3`/`traceparent`、v1 UA 的 Bun 版本落后，以及无项目信号时 `x-opencode-project` 被伪造为随机 40-hex。差异会让上游在不同协议和有无追踪头的请求之间识别出非官方客户端。

阶段一同时复核了旧版客户端：`@opencode/cli@2.0.32` 与 `opencode-ai@1.19.3` 已分别具备新版 v2 的 family 路径、`*/*`、`b3`/`traceparent`，以及新版 v1 的 `x-opencode-project: global`。因此这些不是 2.1.3/1.19.4 新增头，而是 NovaVeil 此前未复刻的存量客户端契约；版本升级只改变了 UA 中的客户端版本与 Bun 版本。

## 决定

- v1 UA 的 Bun 段固定为 `1.4.4`，与 `opencode-ai@1.19.4` 实测值一致。
- v1/v2 免费档的 `Accept` 均为 `*/*`。响应解析不依赖 `Accept`，仍由上游 `Content-Type` 与 SSE 解码器决定。
- v2 免费档的 BaseURL 路径恰好为裸 `/inference` 时，按上游协议插入 family：Chat 与 Responses 使用 `openai`，Anthropic 使用 `anthropic`。同协议透传在 `buildPassthroughRequest` 拼 URL 前插入，跨协议转换在 `buildOutbound` 构造 axonhub transformer 前插入。
- family 只处理裸 `/inference`；已有 `/inference/openai/v1` 等更具体路径、`/zen`、`/zen/v1`、自定义路径和 `##` 完整 URL 保持原样。
- v2 免费档每个出站请求用 `crypto/rand` 生成同源的 32-hex trace、16-hex span 与 16-hex parent。头值分别为 `traceparent: 00-<trace>-<span>-01` 与 `b3: <trace>-<span>-1-<parent>`，入站同名头被覆盖；v1 与付费档不增加这两项。
- `x-opencode-project` 仅原样保留 `global` 或合法 40 位小写十六进制值，其余输入统一回退 `global`。v1/v2 共用该语义。
- 本笔记接管 [OpenCode 免费档客户端保真](2026-10-22-opencode-free-client-fidelity.md) 中暂缓的 family 路由；旧笔记的其余决定继续成立。

## 备选方案

- **只在转换路径改 BaseURL**：改动更小，但同协议透传不经过 transformer，仍会发出旧路径。否掉。
- **在 transformer 内识别 OpenCode 渠道**：需要把渠道语义塞进通用转换层，且无法覆盖透传。否掉。
- **对所有 `/inference` 渠道补 family**：会改变付费或自定义地址的既有路由。否掉。
- **继续从项目信号派生随机 40-hex**：形态像官方值，但无 Git 项目时实测应为 `global`，反而制造确定性差异。否掉。
- **按客户端进程复用 trace ID**：更接近实测客户端，但网关请求之间没有进程边界，且任务要求逐出站请求重生成。否掉。

## 后果

- **收益**：v2 Chat/Responses/Anthropic 的最终 URL、两档 Accept、v1 UA、v2 追踪头与项目号回退均有实测依据和单测锁定。
- **代价与已知上限**：官方客户端同一进程内可能复用 trace ID，NovaVeil 逐请求重生成，仍有行为级差异。若上游开始校验 trace 生命周期或项目目录哈希，需要另开决定。

## 验证

- `go test ./internal/relay/... ./internal/utils/opencodeid/...` 覆盖三个协议的透传/转换最终 URL、路径负例、Accept、追踪头形态与同源、项目号分支和 v1 Bun 版本。
- `go build ./... && go vet ./...` 通过静态检查。
- `npx -y tsx scripts/agent-notes/verify-agent-note-tree.ts` 与 `npx -y tsx scripts/agent-notes/verify-agent-note-format.ts` 校验笔记树与格式。
