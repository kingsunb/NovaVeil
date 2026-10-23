# Agent Note: opencode 免费档指纹实抓校准（Content-Length 位置翻转 + 同步 UA 收口 + v1 键序归一）

Status: implemented

## Problem

2026-10-23 在一次性 Linux 机上用真实官方客户端（`@opencode/cli@2.1.4`、`opencode-ai@1.19.4`，均为 npm 最新）与 NovaVeil 对同一本地 sink 对抓，拿到原始头序与请求体，发现三处与实抓证据的偏差：

1. **Content-Length 头位置**：官方客户端把它作为传输层追加头放在最后（Connection → Accept → Host → Accept-Encoding → Content-Length）；NovaVeil 写在公共块内（authorization 之后第二位）。根因是 [2026-10-23 笔记](./2026-10-23-opencode-anthropic-headers-and-content-length-position.md)参照了 opencode2api 的 bun 移植，其 wire 行为与官方客户端不一致。
2. **同步路径 UA 外泄旧版本**：模型同步/探测请求只引用渠道静态 `custom_header`，存量库存着的 UA 停留在旧版（实测发出 `opencode/latest/2.0.32/cli`，播种时点的内置定义值）；v1 档同步更裸——无任何 UA 时 Bun 传输回退 `Bun/1.3.14`。relay 转发路径有动态覆盖，同步路径没有。
3. **v1 档请求体键序未归一**：官方 1.19.4 键序为 `[model, max_tokens, messages, tools, tool_choice, stream, stream_options]`，NovaVeil 出站是转换器构造序 `[model, messages, stream, stream_options, tools]`。v2 已有键序归一，v1 没有。

实抓同时证实两处**无需**修改：`x-opencode-project` 官方在非 git 目录也发 `global`（NovaVeil 回退值合法）；官方 2.1.4 默认主请求不含 todowrite/patch，embed 14 工具表是按需注入的超集，与 [v2 parity 笔记](./2026-10-24-opencode-v2-fingerprint-parity.md)的源码结论一致。

## Decision

1. **Content-Length 位置（`internal/client/bun.go`）**：删除 `bunRequestHead` 公共块循环中的 Content-Length 特判与 `wroteLength` 标志，仅在传输层追加段按 body 非空写入；`bunCommonHeaders` 移除 `"Content-Length"` 并注释原因。输出契约：有体请求的头序必须以 Content-Length 结尾。
2. **同步 UA 收口（`internal/helper/fetch.go` + `internal/model/opencode_protocol.go`）**：免费档版本常量与 UA 构造从 relay 上移到 model（`OpencodeV1UserAgent(protocol)`、`OpencodeV2UserAgent()`、`OpencodeV1Lane(baseURL)`），relay 的 `applyOpencodeFreeLaneHeaders` 改为引用 model；`fetchOpenAIModels` 在 `applyOpencodeCompatHeaders` 之后调用新增的 `applyOpencodeLaneUserAgent`，对 `OpencodeCompat` 且 zen 档渠道按档位动态覆盖静态 UA。v1 同步无模型上下文，按 chat 协议回退，与 relay 缺省口径一致。
3. **v1 键序归一（`internal/relay/opencode_free.go`）**：`normalizeOpencodeFreeChatBody` 泛化为 `normalizeOpencodeChatBody(body, keyOrder)`，新增 `v1ChatKeyOrder`；`shapeOpencodeFreeBody` 的 v1 lane chat 分支在 read 工具处理之后调用。键序归一不置 `changed`（与 v2 现状一致，不触发非流式折叠）。

## Alternatives considered

- **`x-opencode-project` 回退改为合成 40 位 git 哈希** — 让每个会话更像"真实 git 项目"；否掉：官方在非 git 目录真实发 `global`，伪造哈希属于制造假身份，且上游未证明校验 project 取值，收益纯投机。
- **helper 本地复制一份版本常量** — 改动最小；否掉：会出现第三份指纹知识（relay/model/builtin），升版时必漏一处，与"依赖统一"相悖。
- **v1 键序维持现状** — 上游 v1 档目前只校验 read 工具存在性，无实证风险；否掉：v2 的逐字节门禁证明上游会收紧校验，归一成本一次函数调用，防御性收益大于风险。

## Consequences

- **收益**：Bun 传输出站头序与官方 2.1.4 / 1.19.4 实抓逐位一致；同步/探测请求不再外泄陈旧 UA 或裸 Bun UA；v1 正文键序与官方一致。
- **代价**：免费档版本常量归属从 relay 移到 model，升版改 `model/opencode_protocol.go` 与 `builtin.go` 静态 UA 字面量两处；`bunCommonHeaders` 少一项，任何按该列表枚举的代码需自查（当前仅 `bunRequestHead` 消费）。
- **已知上限**：anthropic 协议档未能用官方客户端实抓（2.1.4 拒绝自定义 npm 供应商配置，两次尝试失败即按熔断停手），其头序仍是源码推导结论；v1 官方会发 `max_tokens`/`tool_choice` 而 NovaVeil 透传客户端原值、不代填——客户端驱动的差异保留。
- **重访信号**：opencode 发布 2.1.5+ / 1.20+ 改变头序、键序或 UA 格式；上游对 `/models` 路径的 UA 出现校验迹象。

## Verification

```sh
go build ./... && go test ./internal/client/ ./internal/relay/ ./internal/helper/
```

- `TestBunRequestHeadOfficialClientWire`：以官方 2.1.4 实抓完整头序固定 `bunRequestHead` 输出，Content-Length 必须在最后。
- `TestShapeOpencodeFreeBodyV1ChatKeyOrder`：v1 出站键序等于官方 1.19.4 实抓序。
- `TestFetchOpenAIModelsOverridesOpencodeLaneUserAgent`：v2 档陈旧 UA 被覆盖、v1 档补官方 UA、非 zen 档与未开兼容渠道不动。
- 抓包机复验：instabox 上重建后对抓 relay 与官方客户端，头序 diff 为空（会话值除外）。
