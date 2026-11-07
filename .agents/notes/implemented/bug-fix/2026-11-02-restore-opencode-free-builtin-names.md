# Agent Note: 恢复 OpenCode Free / OpenCode Free Old 旧名内置渠道

Status: implemented

## Problem

2026-11-01 的 `f4d0a0d` 把两条内置免费档主渠道改名为 `OpenCode Free V2` / `OpenCode Free V1`。`builtin.EnsureBuiltinChannels` 只按 `channels.name` 复用存量行，线上库仍保留旧名 `OpenCode Free` / `OpenCode Free Old`，代理账号池也挂在旧渠道 ID 上；改名后的构建不再命中旧渠道。用户确认线上旧渠道未删除，要求把旧名渠道加回源码，同时保留 V2/V1 与 V1 备用1/2，后续再考虑删除。

## Decision

`BuiltinFreeChannels`（`internal/builtin/builtin.go`）在保留 `OpenCode Free V2`、`OpenCode Free V1`、`OpenCode Free V1 备用1/备用2` 的同时，新增两条旧名兼容条目：

- `OpenCode Free`：`OpencodeLane=v2`，与 `OpenCode Free V2` 同端点、同 Key、同种子模型。
- `OpenCode Free Old`：`OpencodeLane=v1`，与 `OpenCode Free V1` 同端点、同 Key、同种子模型。

启动时 `EnsureBuiltinChannels` 按旧名命中线上存量渠道，代理账号池继续挂原渠道。不新增迁移；线上若已生成 V2/V1 行，按用户口径后续再清理。

## 取代检查

- [OpenCode Free Old 内置渠道走 legacy /zen/v1 端点](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)：该篇的 `/zen` 端点结论已由 [2026-10-26 笔记](./2026-10-26-opencode-free-zen-closed-inference-only.md) 取代，旧名渠道改指 `/inference`；本篇恢复旧名条目并沿用该端点，与该篇部分重叠、互链，不归档。
- [2026-10-26 笔记](./2026-10-26-opencode-free-zen-closed-inference-only.md)：本篇复用其 `OpencodeLane` 与 `/inference` 结论，只补回旧名条目，不改变形态定义；部分重叠，不取代、不归档。
- 活跃树无完全吸收本篇或需要归档的 implemented 笔记。

## Alternatives considered

- **把 V2/V1 直接改名回旧名** — 源码只留两条主渠道，最干净；否掉：用户明确要求现有 V2/V1 与备用渠道先保留，后续再删。
- **新增迁移把线上 V2/V1 自动改名或删除** — 能自动收口，但线上旧渠道仍在，且新行可能已有管理员配置；自动删除风险高，用户明确后续再处理，本次不做。

## Consequences

- **收益**：线上旧渠道与代理账号池继续按名称复用；源码同时保留 V2/V1 的现有配置与命名。
- **代价与已知上限**：新库会同时补建四条主渠道与两条备用渠道，模型与端点重复，AutoSync 会对同一端点重复同步；删除 V2/V1 时需同步移除旧名兼容条目并清理线上 V2/V1 行。

## Verification

```sh
go test ./internal/builtin/ -run 'TestBuiltinFreeChannelsDefinitions|TestBuiltinFreeChannelsKeepLegacyOpencodeNames|TestOpenCodeFreeSeedProtocolsAreVerified' -v
go vet ./internal/builtin/
```

- `TestBuiltinFreeChannelsDefinitions` 校验当前 20 条定义（含 keyed 账号池）且名称唯一。
- `TestBuiltinFreeChannelsKeepLegacyOpencodeNames` 锁定旧名与 V2/V1 的 lane 对应关系，并确认四条主渠道都走 `/inference`。
- `TestOpenCodeFreeSeedProtocolsAreVerified` 按 `OpenCode Free V2` 校验种子模型协议。

## 相关

- [OpenCode Free Old 内置渠道走 legacy /zen/v1 端点](../feature/2026-10-17-opencode-free-legacy-zen-channel.md)
- [/zen 免费档关闭，免费档统一到 /inference 并显式区分客户端形态](./2026-10-26-opencode-free-zen-closed-inference-only.md)
