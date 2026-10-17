# Agent Note: OpenCode Free Old 内置渠道走 legacy /zen/v1 端点

Status: implemented

## 问题

OpenCode 免费档有两个可用端点：

- `/inference`（v2）：需 5 个核心工具（shell/edit/glob/grep/read）且逐字节校验 schema，任一字段偏差即 403。
- `/zen/v1`（legacy v1.3.7）：只需单个 `read` 工具，不校验 description/parameters schema，显著更鲁棒。

opencode2api 在 2026-10-16（commit `076451f` + `a9470df`）将 legacy `/zen/v1` 设为匿名主通道、`/inference` 设为 fallback，正是利用 legacy 档的宽松校验提高成功率。NovaVeil 此前只有 3 个走 `/inference` 的 OpenCode Free 内置渠道（见 [多副本供分代理转发](2026-10-15-opencode-free-dual-channel-for-proxy.md)），没有走 legacy `/zen/v1` 的渠道，无法利用更鲁棒的主通道。

## 决定

在 `BuiltinFreeChannels`（`internal/builtin/builtin.go`）新增 `"OpenCode Free Old"` 内置渠道，走 legacy `/zen/v1` 端点：

- **BaseURL** `https://opencode.ai/zen`：axonhub 转换器 `NormalizeBaseURL(baseURL, "v1")` 追加 `/v1`，最终请求落点 `https://opencode.ai/zen/v1/chat/completions`，即 legacy 免费档端点。
- **Key** `public`，**OpencodeCompat** `true`：与现有 OpenCode Free 渠道一致，触发 opencode 会话头注入和免费档请求改写。
- **CustomHeader** 与 `/inference` 渠道有三处差异：
  - `User-Agent: opencode/1.18.31 (linux amd64; go1.26.0)` — legacy v1.3.7 格式，非 v2 的 `opencode/latest/2.0.29/cli`。
  - `x-opencode-project: prj_a1b2c3d4e5f6a1b2c3d4e5f6` — legacy `prj_<24hex>` 格式。`injectOpencodeCorrelationHeaders`（`internal/relay/random_headers.go`）仅在头为空时写入默认 40-hex 值，CustomHeader 先于该函数应用，故 `prj_` 值不被覆盖。
  - `x-opencode-client: cli` — 与 `/inference` 渠道一致。
- **Models** 与现有 OpenCode Free 渠道完全一致（8 个 free 模型，含 2 个 Responses 协议）。

`OpenCodeTier("https://opencode.ai/zen")` 返回 `"zen"`（含 `/zen` 但不含 `/zen/go` 或 `/zen/v1`），`isOpencodeFreeModel` 命中 `IsFree = true`，`shapeOpencodeFreeBody` 照常注入 5 工具。legacy 档不校验 schema，5 工具的超集注入可接受——无需为 legacy 渠道单独实现精简改写路径。

管理员在分组中把 `"OpenCode Free Old"` 设为高优先级、`/inference` 渠道设为低优先级，即复用 NovaVeil 现有分组故障转移实现双通道：legacy 主通道失败时自动 fallback 到 `/inference`。

### 不变量保持

- `TestBuiltinFreeChannelsDefinitions` 检查 Name 唯一性——`"OpenCode Free Old"` 与现有 `"OpenCode Free"` / `"OpenCode Free 2"` / `"OpenCode Free 3"` 互不冲突。
- `TestOpenCodeFreeSeedProtocolsAreVerified` 按 `Name == "OpenCode Free"` 精确匹配第一个条目——原条目不变，测试通过。
- `isOpencodeFreeModel` 按 `channel.IsFree` 判定——`"OpenCode Free Old"` 为 `IsFree = true`，正确触发免费档改写。
- `BackfillOpencodeUpstreamProtocols` 按 `builtin = true AND opencode_compat = true` 筛选——新渠道匹配，协议回填生效。

## 取代检查

- 正交 [多副本供分代理转发](2026-10-15-opencode-free-dual-channel-for-proxy.md)：那篇加 `/inference` 同配置副本供分代理，本篇加不同端点（legacy `/zen/v1`）的不同配置渠道，互不取代。
- 正交 [opencode v2 客户端头与端点对齐](../bug-fix/2026-10-15-opencode-v2-client-header-endpoint-alignment.md)：那篇对齐 v2 `/inference` 头集合，本篇用 v1.x legacy 头格式走不同端点，互不取代。
- 正交 [OpenCode Zen 免费档 Agent 形态请求改写](../bug-fix/2026-09-29-opencode-free-tier-agent-shape-rewrite.md)：那篇修请求体改写逻辑，本篇不改改写判定，新渠道走同一改写路径。
- 活跃树无 proposed / rejected 覆盖此决定；无完全吸收或需要归档的笔记。

## 备选方案

- **在 relay 层实现 per-request 双通道 failover（legacy → /inference）** — 在 `upstream.go` 的 `sendPassthrough` 内捕获 legacy 渠道的失败响应并自动重试 `/inference`；否掉是因为 NovaVeil 已有成熟的分组级故障转移机制（见 [docs/DEVELOPMENT_routing.md](../../../docs/DEVELOPMENT_routing.md)），在 relay 层再叠一层跨渠道重试会引入双重重试语义、计数/日志/并发槽位的复杂交互。用分组故障转移实现双通道零代码改动、零新路径，管理员还可灵活调整优先级和代理绑定。
- **为 legacy 渠道实现精简请求改写（只注入 read 工具）** — 新增 `shapeOpencodeLegacyFreeBody` 只确保 `read` 工具存在、不注入 shell/edit/glob/grep；否掉是因为 legacy 档不校验 schema，现有 `shapeOpencodeFreeBody` 的 5 工具注入是合法超集，上游可接受。精简改写需改 `upstream.go` 三处调用点 + 新增改写函数 + 新测试，改动面远大于加一个渠道条目，且无功能收益。
- **通过 UI 创建自定义渠道走 legacy 端点** — 零源码变更；否掉与 [多副本笔记](2026-10-15-opencode-free-dual-channel-for-proxy.md) 同理：`ensureFreeBuiltinChannels` 每次启动强制清除非内置渠道的 `is_free` 和 `opencode_compat`，自定义渠道重启后退化为普通渠道，opencode 会话头和免费档改写均失效，上游 403。

## 后果

- **收益**：管理员可将 legacy `/zen/v1` 设为 OpenCode 免费档主通道，利用其宽松校验提高成功率；`/inference` 渠道作为 fallback 兜底。升级后 `EnsureBuiltinChannels` 按 Name 自动补建，无需手动操作。
- **代价与已知上限**：`"OpenCode Free Old"` 的 User-Agent 和 `x-opencode-project` 是静态值（`opencode/1.18.31 (linux amd64; go1.26.0)` 和固定 `prj_` 值），不随运行时 OS/Go 版本动态生成——非 linux/amd64 部署需管理员在 UI 手动改 User-Agent。`x-opencode-project` 用固定值而非每请求随机，与 opencode2api 的 `legacyProjectID()` 行为略有差异，但 legacy 档不校验项目 ID 唯一性，固定值可接受。重访信号：legacy `/zen/v1` 端点开始校验 User-Agent 或项目 ID 格式，或需要动态生成这些头时，考虑在 `injectOpencodeCorrelationHeaders` 中按 `isLegacyZenUpstream` 分支注入。

## 验证

`go build ./...`、`go vet ./...`、`go test ./internal/builtin/ ./internal/relay/` 均通过。`TestBuiltinFreeChannelsDefinitions` 的 Name 唯一性检查覆盖新条目。`OpenCodeTier("https://opencode.ai/zen")` 返回 `"zen"`，`isOpencodeFreeModel` 对 `IsFree = true` 的 `"OpenCode Free Old"` 渠道返回 `true`，免费档改写路径生效。
