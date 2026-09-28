# Agent Note: OpenCode Zen 免费档 Agent 形态请求改写

Status: implemented

## 问题

内置 OpenCode Free 渠道（`https://opencode.ai/zen`，`x-opencode-client` 头 + 公开占位 Key）的六个出厂模型都带 `-free` 后缀。Zen 免费档只接受「Agent 形态」的流式请求：`stream: true` 且请求体携带 `bash`/`edit`/`glob`/`grep`/`read` 五个核心工具名，否则一律 `403 FreeTierError`。网关的诊断测试发非流式、无工具的 Chat 正文，因此全部密钥测试失败；任何非流式或不带核心工具的客户端请求同样被拒，免费渠道实际不可用。

## 决定

在连线上游前，把落在 Zen 免费档的请求规范成 Agent 形态（只借鉴 opencode2api 的行为，不复用其实现，见 [ROADMAP 第 2 节](../../../../docs/ROADMAP.md) 的「只借鉴行为、不复制源码」）：

- 判定 `isOpencodeFreeModel`：`model.OpenCodeTier(baseURL) == "zen"` 且（`channel.IsFree` 或模型名含 `free` 子串，不区分大小写，对齐 opencode2api 的 `isFreeModel` 兜底）。`/zen/go` Go 档由 `OpenCodeTier` 先判排除，不做改写；非 OpenCode BaseURL 同样落空，指向 `/zen` 的自定义渠道若模型名含 `free` 子串也会改写。
- 改写 `shapeOpencodeFreeBody`：强制 `stream: true`；补齐 `stream_options.include_usage: true`；注入缺失的核心工具——只按 `function.name` 判定已有工具，缺失的以最小 Chat 工具定义追加，既有工具的原始字节原样保留。用 sjson/gjson 点状改写，不整包 Marshal。
- 折叠 `collapseOpencodeFreeStream`：客户端原本非流式、改写把 `stream` 置真时，上游返回 SSE，网关用客户端入站转换器的 `AggregateStreamChunks` 聚合成 JSON 再返回，终态校验复用 `validateRoundUsage`/`validateRoundAnswer` 与 Chat `finish_reason` 白名单。
- 覆盖两条路径：透传 Chat 在 `sendPassthrough`（诊断测试即走这里）；转换（Anthropic/Responses → Chat）在 `sendConverted` 里把客户端正文置流式以便 pipeline 走流式、并在 `conversionMiddleware.OnOutboundRawRequest` 里对 Chat 出站做同一改写。
- 指纹与关联头（随本次一并落地）：内置免费渠道 `x-opencode-client` 用 `cli`（对齐 opencode2api，弃 `desktop`）；`OpencodeCompat` 渠道补 `x-session-affinity`（镜像会话号）/`x-opencode-request`（每请求 `req_` 随机值）/`x-opencode-project`（固定 `prj_`）三个关联头。这些头不参与免费档 403 判定（只查会话号 + stream + 工具名），仅为上游亲缘与请求关联，故改写仍是主修复、头是补充。

## 取代检查

- 部分翻转 [OpenCode 模型行记录上游原生协议](../feature/2026-09-22-opencode-upstream-protocol.md) 里「不移植匿名请求改写」的取舍：本次只落地「免费档请求体改写」这一子集，订阅 OAuth、Responses WebSocket、CLI 指纹、双 Key 池仍不移植，互链不取代、不归档。
- 正交 [测试与后台探测经专用参数注入合法 opencode 会话号](2026-09-29-opencode-diag-probe-session-placeholder.md)：那篇修 400 `MissingSessionID`，本篇修 403 `FreeTierError`，同为免费模型测试失败的两处独立根因，互不取代。
- 活跃树无 proposed / rejected 目录；无完全吸收或需要归档的笔记。

## 备选方案

- **只改 `x-opencode-client` 头为 `cli`** — 改动最小；否掉作为唯一修复是因为头无法满足「工具名 + stream」的硬校验，工具名缺失时免费档仍 403，无法独立修复。本次已把 `cli` 与关联头一并落地，但只作为 body 改写之外的补充，不替代它。
- **把免费渠道改成官方 `/zen/go`** — 直接避开免费档；否掉是因为失去免费额度，且内置免费渠道与官方渠道本就是两条独立渠道，不应并线。
- **整体复制 opencode2api 的匿名改写与 Key 池** — 行为最完整；否掉是因为引入另一套 Key 池/数据库与源码，违反「只借鉴行为、不复制源码」的既有路规。

## 后果

- **收益**：免费档模型的诊断测试与真实请求不再 403；非流式客户端对免费模型透明可用，流式客户端补上缺失的核心工具。
- **代价与已知上限**：非流式免费请求多一次「流式上游 + 网关聚合」往返，响应体在网关内完整缓冲后才下发；改写只在 Chat 出站上成立，若未来 pipeline 变更导致非流式无法折叠，会退回 403。重访信号：OpenCode 免费档放宽 Agent 形态校验，或不再要求工具名。

## 验证

`internal/relay/opencode_free.go` 提供 `isOpencodeFreeModel`/`shapeOpencodeFreeBody`/`collapseOpencodeFreeStream`；单测 `internal/relay/opencode_free_test.go` 覆盖判定（含名字中部/前缀含 `free` 的子串用例）、Agent 形态强制、幂等、工具合并与非数组 tools 不改写。关联头注入在 `internal/relay/random_headers.go`，单测覆盖 affinity 镜像、`req_`/`prj_` 格式与禁用零差异。`go build ./...`、`go vet ./...`、`go test ./internal/relay/...` 通过。