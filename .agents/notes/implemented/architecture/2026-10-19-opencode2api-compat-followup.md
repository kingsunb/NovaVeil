# Agent Note: opencode2api 兼容全面跟进（UA/工具定义/v1 头/Bun 传输）

Status: implemented

## 问题

opencode2api 在 2026-10-17 前后关闭了一批 OpenCode 免费档兼容缺口，NovaVeil 落后于其中若干项。上游对免费档请求做三类严格校验，任一不匹配即 403 FreeTierError 或按指纹限流：

1. **逐字节比对工具定义**（2026-10-14 起）：v2 `/inference` 档对每个识别的工具名比对 description 与 parameters，占位 stub 或过期定义被拒。NovaVeil 的 `shell` 工具描述写的是 "Windows using powershell"，而官方 CLI（Linux 运行时）用 "Linux using bash"。
2. **User-Agent 版本/格式**：v2 档期望 `opencode/latest/2.0.32/cli`，NovaVeil 仍是 `2.0.29`；v1 档（`/zen`）期望协议相关的 `opencode/1.19.3 ai-sdk/provider-utils/<ver> runtime/bun/1.3.14`，NovaVeil 用的是过期的 `opencode/1.18.31 (linux amd64; go1.26.0)`，且不随出站协议变化。
3. **TLS 指纹**：OpenCode 1.x/2.x CLI 均跑在 Bun 上，其 BoringSSL ClientHello（JA4 t13d1713h1、http/1.1 ALPN）与 Go crypto/tls 的 ClientHello 不同。上游可按 JA3/JA4 识别非官方客户端并限流/拒绝。net/http 无法伪造该指纹。

此外 opencode2api 在 v1 档发送 `x-opencode-request`（递增 `msg_` id）并用裸 40-hex 项目 ID，而 NovaVeil 此前明确不发送 `x-opencode-request`（见 `opencodeid.go` 旧注释），v1 渠道用旧 `prj_<24hex>` 项目头。v2 档则不能发 `x-opencode-request`（网关 403）。

## 决定

按 opencode2api 逐项跟进，所有改动经 `isOpencodeFreeModel` / `OpenCodeTier` / `channel.OpencodeCompat` 门控，仅影响 OpenCode 免费档，不触及其他渠道与正常路由/failover/Key 轮换。

### v2 UA 与工具定义

- `internal/builtin/builtin.go`：4 个 OpenCode 内置渠道的 User-Agent 版本 `2.0.29`→`2.0.32`（3 个 `/inference` cli + 1 个 `/zen/v1` desktop）。
- `internal/relay/opencode_tools.json`：整体替换为 opencode2api 的 12 工具定义（edit/execute/glob/grep/question/read/shell/skill/subagent/webfetch/websearch/write）。`shell` 描述随之由 "Windows using powershell" 改为 "Linux using bash"，与其余 4 个核心工具（edit/glob/grep/read）逐字节对齐官方 CLI。注入逻辑（`ensureOpencodeFreeTools`）仍只对 5 个核心工具做同名替换/补齐，新增 7 个工具定义供 `knownOpencodeTool` 在客户端声明同名工具时替换为真实定义。

### v1 档动态头（`internal/relay/opencode_free.go`）

新增 `applyOpencodeFreeLaneHeaders(channel, request, modelName)`，在 `upstream.go` 两处 `isOpencodeFreeModel` 块内、`applyChannelConfig` 之后调用。仅对 v1 档（`isOpencodeV1Lane`：`OpenCodeTier=="zen"` 且 BaseURL 含 `/zen`）改写头，v2 档（`/inference`）不动：

- **User-Agent**：按出站协议（`opencodeModelProtocol` 查 `channel.Models[].UpstreamProtocol`，默认 chat）选 provider-utils 版本（chat=4.0.23 / responses=4.0.40 / anthropic=4.0.46），拼成 `opencode/1.19.3 ai-sdk/provider-utils/<ver> runtime/bun/1.3.14`。
- **x-opencode-request**：`opencodeid.GenerateMessageID()`（`msg_` + 12hex + 14alnum 递增 id）。v2 档不发此头（网关 403）。
- **x-opencode-project**：移除 `OpenCode Free Old` 的静态 `prj_<24hex>` CustomHeader，改由既有 `injectOpencodeCorrelationHeaders` 注入裸 40-hex（`opencodeid.ProjectID()`）。

`opencodeid.go` 抽出 `generateOpencodePrefixedID(prefix)` 共用毫秒计数器，`GenerateSessionID`（`ses_`）与新增 `GenerateMessageID`（`msg_`）同形且同毫秒递增。

### Bun 伪装传输（`internal/client/bun.go` + `bun_dial.go`）

从 opencode2api 移植 BunTransport（`http.RoundTripper`）：经 uTLS 用 Bun 1.4.3 的 BoringSSL ClientHello 握手（`bunClientHello`，JA4 t13d1713h1、http/1.1 唯一 ALPN、X25519MLKEM768 首组 key share），仅说 HTTP/1.1，按 Bun fetch 头序布局请求（`bunRequestHead`），按 host 池化空闲连接并监视对端关闭，解码 gzip/deflate/br/zstd。新增依赖 `refraction-networking/utls v1.8.2`、`andybalholm/brotli v1.1.1`（zstd 复用既有 `klauspost/compress`）。

`client.GetBunClient(proxyURL)` 按代理地址缓存复用连接池的 `http.Client`（`CheckRedirect` 与常规渠道一致）。`client.GetOpencodeBunClient(channel)` 按与 `ChannelHttpClient` 同口径解析代理（直连/系统代理/渠道代理）。`helper.ChannelHttpClient` 在入口对 `channel.OpencodeCompat && OpenCodeTier(channel.BaseURL)=="zen"` 的渠道返回 Bun 客户端，其余渠道（含 `/zen/v1`、`/zen/go` 的 go 付费档）走原常规客户端，互不影响。

## 取代检查

- 部分取代 [OpenCode Free Old 内置渠道](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)：那篇的 v1 静态 UA/`prj_` 项目头事实已就地同步为动态注入，本篇承载动态化与 `x-opencode-request` 策略翻转的理由。两篇互链，那篇仍活跃。
- 正交 [opencode v2 客户端头与端点对齐](../bug-fix/2026-10-15-opencode-v2-client-header-endpoint-alignment.md)：那篇对齐 v2 头集合，本篇在其上 bump 版本号并加 v1 动态头与 Bun 传输，互不取代。
- 正交 [OpenCode Zen 免费档 Agent 形态请求改写](../bug-fix/2026-09-29-opencode-free-tier-agent-shape-rewrite.md)：那篇是请求体改写，本篇是请求头与传输层，互不取代。
- 活跃树无 proposed / rejected 覆盖 Bun 传输或 v1 动态头；无完全吸收或需归档的笔记。

## 备选方案

- **v1 用静态 UA（统一 chat 变体）** — 零动态逻辑；否掉是因为 v1 模型含 responses 协议（muse-spark），静态 chat UA 的 provider-utils 版本与协议不符，上游可能按 UA 与实际协议不一致拒绝。
- **v2 也发 `x-opencode-request`** — 对称简单；否掉是因为 v2 网关对该头返回 403 FreeTierError（opencode2api `setV2Headers` 明确不发）。
- **Bun 传输覆盖所有 OpenCode 渠道（含 `/zen/v1` 付费档）** — 统一传输；否掉是因为付费 go 档无需指纹伪装，且 opencode2api `inferenceClient` 对非免费流量返回常规客户端。扩大覆盖面徒增风险无收益。
- **不做 Bun 传输（仅修 UA/工具定义）** — 改动面小；否掉是因为 TLS 指纹是最大的剩余缺口，上游按 JA3/JA4 识别 Go 客户端即可限流/拒绝，UA 修对也绕不过传输层指纹。

## 后果

- **收益**：v2 UA 与 `shell` 工具定义逐字节匹配当前 CLI（消除过期定义 403）；v1 档头集合对齐 opencode2api（协议正确的 UA、裸 40-hex 项目 ID、`x-opencode-request`）；OpenCode 免费档出站 TLS 指纹匹配 Bun 运行时，规避 JA3/JA4 识别。
- **代价与已知上限**：新增 `utls`、`brotli` 两个依赖（供应链面增大）；BunTransport 约 900 行自维护传输代码（维护负担随上游 ClientHello 演进）；TLS 指纹伪装是军备竞赛，上游更换 ClientHello 期望时需同步 `bunClientHello`。所有改动门控在 OpenCode 免费档，非 OpenCode 渠道与路由/failover/Key 轮换/脱敏不受影响。
- **已知未跟进项**（opencode2api 有但本轮未做，按影响排序）：v1 档单 `read` 工具精简（当前注入 5 工具超集，legacy 档不校验故可接受）；v1 anthropic-beta 按请求体特征动态推导；SystemOne 第四协议；免费模型按零成本元数据动态识别（当前用名字含 "free" 兜底）；v1→v2 车道顺序（属分组优先级配置，非代码）。重访信号：上游开始拒绝上述任一时再跟进。

## 验证

`go build ./...`、`go vet ./internal/relay/ ./internal/client/ ./internal/helper/ ./internal/utils/opencodeid/ ./internal/builtin/`、`go test ./internal/client/` 均通过。`helper.ChannelHttpClient` 仅对 `OpencodeCompat && OpenCodeTier=="zen"` 返回 Bun 客户端；`applyOpencodeFreeLaneHeaders` 经 `isOpencodeV1Lane` 仅对 `/zen` 改写头；`opencode_tools.json` 含 12 键且 `shell` 描述含 "Linux using bash"；`opencodeid.GenerateMessageID()` 返回 `msg_` 前缀的 12hex+14alnum。
