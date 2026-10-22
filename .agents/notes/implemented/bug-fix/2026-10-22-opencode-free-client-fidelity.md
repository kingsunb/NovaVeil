# Agent Note: OpenCode 免费档客户端保真（UA 2.1.3 与 v2 Chat 字节差异）

Status: implemented

## 问题

OpenCode 免费档按客户端指纹放行。上游最新客户端是 v2 `2.1.3` 与 v1 `1.19.4`，NovaVeil 仍声称 v1 `1.19.3`；v2 的 UA 又来自内置渠道的静态 `CustomHeader`，存量数据库不会随种子更新。请求正文还有三处与 v2 客户端不同：Chat 缺少默认 `max_completion_tokens: 32000`、生成工具没有显式 `strict: false`、JSON 把 `<`/`>`/`&` 转义成 Unicode。任一差异都可能让上游把请求判为非官方客户端。

## 决定

- v1 档 `/zen` 的 UA 版本改为 `1.19.4`，仍按出站协议动态拼装 `opencode/<ver> ai-sdk/provider-utils/<ver> runtime/bun/<ver>`，不改成静态 CustomHeader。
- v2 档 `/inference` 在 `applyOpencodeFreeLaneHeaders` 中无条件覆盖 `User-Agent: opencode/latest/2.1.3/cli`，位于设置 `Accept` 之后；因此存量库不需要迁移。内置三个 cli 种子更新为同一值，desktop 种子更新为 `opencode/latest/2.1.3/desktop`。
- v2 Chat 免费档正文在 `max_tokens` 与 `max_completion_tokens` 都不存在时补 `max_completion_tokens: 32000`；显式值（含 `0`）一律保留。整形发生在 `applyChannelConfig` 之后，渠道 `model_limits` / `param_override` 优先。
- v2 Chat 生成的工具 `function` 增加 `strict: false`；匹配器要求客户端工具显式 `strict=false`，否则替换为规范定义。v1 的 legacy `read` 占位不增加该字段。
- 生成工具的 JSON 使用 `json.Encoder` 且 `SetEscapeHTML(false)`，去掉末尾换行；不使用字符串替换反解 `\u003c` / `\u003e` / `\u0026`。
- 已有版本与头讨论见 [opencode2api 兼容跟进](../architecture/2026-10-19-opencode2api-compat-followup.md)、[免费档头集合对齐](2026-10-20-opencode-free-lane-header-parity.md)、[v2 免费档整形](../feature/2026-10-21-opencode-v2-free-tier-shaping.md)。
- family 路由、追踪头与项目号回退的后续修正在 [OpenCode 免费档新客户端指纹对齐](2026-10-22-opencode-free-fingerprint-parity.md) 中接管。

## 备选方案

- **新增数据库迁移，批量改写存量渠道的静态 UA** — 能一次修正数据库，但动态覆盖已在出站前生效，迁移只会增加不可逆的状态变更；`020.go` 保留为历史记录。否掉。
- **跟进 `/inference` 按 family 分组路由** — 2.1.x 客户端会请求 `/inference/openai/...` 或 `/inference/anthropic/...`，保真度更高；旧扁平路由仍可服务，本次目标不是修复可用性，且改动会扩大路由面。否掉。
- **整包 decode/encode 以 canonicalize 全局键序** — 能完全复刻客户端的键序；现有 `sjson` 点改保留未触及字段的原始字节，整包重写会改变大量无关字段并放大风险。否掉。
- **按 `runtime.GOOS` 改写 shell 工具描述** — 看起来能对齐 Windows 客户端；网关服务端平台不等于工具实际执行端平台，会发出错误描述。否掉。

## 后果

- **收益**：两档 UA 与 v2 Chat 的三处已知字节差异对齐官方客户端，且存量渠道无需迁移即可获得新 UA；工具定义匹配不再把缺失 `strict` 的旧定义误判为一致。
- **代价与已知上限**：免费额度已耗尽，不能真实调用上游验证放行结果；v1 仍使用 legacy `read` 占位且不校验工具 schema。family 与追踪头已由新笔记接管，全局键序与 shell 平台改写仍是已知非修复差异。若上游开始校验这些差异，或要求 UA 再升版，需要重访本决定。

## 验证

- `go test ./internal/relay/... ./internal/builtin/... ./internal/model/...` 覆盖 v2 UA 覆盖、v1 UA 版本、补/不补 `max_completion_tokens`、strict 匹配与工具 JSON 不转义 HTML。
- `go build ./... && go vet ./...` 通过静态检查。
- `npx -y tsx scripts/agent-notes/verify-agent-note-tree.ts` 与 `npx -y tsx scripts/agent-notes/verify-agent-note-format.ts` 校验笔记树与格式。
