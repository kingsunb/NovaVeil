# Agent Note: /zen 免费档关闭，免费档统一到 /inference 并显式区分客户端形态

Status: implemented

## Problem

NovaVeil 把 OpenCode 免费档分成两条 lane：v1 档靠 `BaseURL 含 /zen` 判定（`OpencodeV1Lane`），指向 `https://opencode.ai/zen`；v2 档指向 `https://opencode.ai/inference`。这个划分来自 opencode2api 2026-10-16 的双通道实现，当时 `/zen` 确实可用（2026-10-24 还有 `/zen` 同出口 429→200 的实测闭环）。

2026-10-26 实测推翻了该前提：

1. **官方 1.19.4 客户端默认 baseURL 就是 `/inference`**。用 MITM（本地 CA + TLS 终结）抓包，官方 1.x 的请求行是 `POST /inference/openai/v1/chat/completions`，**不是 `/zen/v1/chat/completions`**。
2. **`/zen` 对免费档已关闭**。把官方 1.19.4 的自定义 provider baseURL 指向 `https://opencode.ai/zen/v1`，同一批 resin 出口一律 `429 FreeUsageLimitError`；同客户端默认配置打 `/inference` 则 200。
3. **官方 1.x 与 2.x 在 `/inference` 上都放行**，差别只有 UA（1.19.4 + provider-utils 4.0.23 + bun 1.4.4 / 2.1.4 cli）与是否带 `x-opencode-request`。

后果：账号体检与真实转发都挂在 `OpenCode Free Old`（`/zen`）上，恒打一个已关闭的端点，所以「体检健康率 3/2100」「转发全 429」里混入了大量与 IP 额度无关的假阴性——形态实验全部打在死端点，怎么改都拿不到信号。

同时暴露一个表达能力缺陷：v1/v2 形态原先由 BaseURL 字符串推断，一旦两条形态都要打 `/inference`，就无法再区分。

## Decision

1. **`model.Channel` 新增 `OpencodeLane`**（`v1` / `v2` / 空）。新增 `model.OpencodeLaneOf(channel)`：显式字段优先，未设置时回退旧口径（`BaseURL 含 /zen` 即 v1）。`OpencodeV1Lane(baseURL)` 保留供回退使用。
2. **两条内置免费档渠道都指向 `https://opencode.ai/inference`**：`OpenCode Free` = `v2`，`OpenCode Free Old` = `v1`。形态差异（UA / `x-opencode-request`）仍由 `applyOpencodeFreeLaneHeaders` 按 `isOpencodeV1Lane` 动态设置，只是判定改读 `OpencodeLaneOf`。
3. **`/inference` 的协议族段对两条 lane 都生效**：`opencodeV2FamilyBaseURL` 去掉 v1 排除条件，v1 形态同样出站 `/inference/openai/...`。
4. **体检路径补协议族段**（`ocProbeRawRequest`）：BaseURL 以 `/inference` 结尾时 Path 由 `/v1/chat/completions` 改为 `/openai/v1/chat/completions`（anthropic 协议用 `/anthropic`），与官方实抓同形。
5. **迁移 026**：把存量内置 `OpenCode Free` / `OpenCode Free Old` 的 `base_url` 统一为 `/inference` 并写入 `opencode_lane`；`builtin = false` 的自定义渠道不动，重复执行幂等。

## Alternatives considered

- **继续用 BaseURL 字符串区分形态**（如 `https://opencode.ai/inference#v1` 或保留假 `/zen` 仅作标记）— 零迁移成本；否掉：URL 会被真实发出去或需要额外剥离逻辑，隐性契约比显式字段更难维护，且 `/zen` 语义已失效。
- **只保留 v2 形态，删除 v1** — 改动最小；否掉：官方 1.x 客户端形态在 `/inference` 上实测 200，保留它有实际价值（不同客户端版本可能有不同的上游待遇），且用户明确要求两种都留。
- **删除 `OpenCode Free Old` 渠道并级联清理账号池** — 彻底断掉死端点；否掉：该渠道现在改指 `/inference` 即成为有效的 1.x 形态渠道，删除会连带丢弃存量代理账号池（2100 条），代价远大于收益。
- **只改体检不改转发** — 能让探测恢复真实；否掉：转发仍会打 `/zen`，问题只解决一半。

## Consequences

- **收益**：免费档只剩一个真实端点（`/inference`），体检与转发都不会再打关闭的 `/zen`；形态从「URL 推断」变为「显式字段」，新增形态不需要再造假 URL；体检路径与官方实抓同形（含 `/openai` 族段）。
- **代价与已知上限**：`channels` 表新增一列（AutoMigrate 加列 + 迁移 026 回填）；`OpencodeLane` 为空时仍走旧推断，自定义 `/zen` 渠道会继续被当作 v1 形态——它们同样打不开 `/zen`，属预期。语义上「Old」不再指端点，而是指客户端世代，命名有历史包袱。
- **未验证**：`/inference` 上 v1 与 v2 形态的实际放行率差异（当前 resin 池额度枯竭，无法在有额度的出口上做对照）；`x-opencode-request` 在 v2 网关是否仍触发 403（笔记记录与本次官方 1.x 带该头 200 的现象冲突，需重验）。

## Verification

```sh
go build ./... && go vet ./internal/model/ ./internal/relay/ ./internal/builtin/ ./internal/db/migrate/
go test ./internal/db/migrate/ -run TestAlignOpencodeFreeLaneChannels -v
go test ./internal/relay/ ./internal/model/ ./internal/builtin/
```

- `TestAlignOpencodeFreeLaneChannels`：两条内置渠道端点与形态被正确回填，自定义渠道不受影响，重复执行幂等。
- 线上复核口径：`sqlite3 data.db "select name,base_url,opencode_lane from channels where builtin=1 and base_url like '%opencode%'"` 应输出两条 `/inference`，lane 分别为 v2 与 v1。

## 相关

- 取代 [opencode2api 兼容全面跟进](../architecture/2026-10-19-opencode2api-compat-followup.md) 中「v1 档 = /zen 动态头」的端点前提（头集合与 UA 结论仍有效）。
- 与 [OpenCode 免费档头集合对齐](../bug-fix/2026-10-20-opencode-free-lane-header-parity.md)、[v2 免费档整形](../feature/2026-10-21-opencode-v2-free-tier-shaping.md) 正交：那两篇管头与正文，本篇管端点与形态来源。
- v2 档长期 429 的调查结论见 [v2 档风控调查](./2026-10-25-opencode-v2-lane-investigation.md)；本篇纠正了其中「v1 为唯一主通道」的运营判断——`/zen` 关闭后 v1 通道本身已无效。
