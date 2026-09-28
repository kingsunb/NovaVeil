# Agent Note: muse-spark 免费档按 Responses 协议补种并重跑回填

Status: implemented

## 问题

`muse-spark-1.2-contributor-free` / `muse-spark-1.3-contributor-free` 在公开目录 `https://models.opencode.ai/api.json` 的 `opencode` 提供方里带模型级 `provider.npm = "@ai-sdk/openai"`，出站必须走 Responses。但 [upstream-protocol 的出厂种子](../feature/2026-09-22-opencode-upstream-protocol.md) 只核实了六个继承 `@ai-sdk/openai-compatible` 的 chat 模型，没列这两个名字，于是它们 `upstream_protocol` 留空 → 按渠道类型回退成 Chat 出站 → 命中 [免费档 Agent 形态改写](2026-09-29-opencode-free-tier-agent-shape-rewrite.md) 注入 Chat 格式工具（`{"type":"function","function":{"name":…}}`，顶层无 `name`）→ 上游按 Responses 校验拒绝：

```
tools[0] missing required field name (Error from provider (Console))
```

同时，`upstream_protocol` 的空协议回填挂在迁移 017 的 `Up` 里，只跑一次；后续补进种子的模型在存量库上不会再被填，所以「改种子数据」本身修不了已上线的库。

## 决定

- 在 `OpencodeZenSeedProtocols` 补两条 `muse-spark-1.2-contributor-free` / `muse-spark-1.3-contributor-free` → `responses`，并在内置 OpenCode Free 渠道的出厂模型列表同步这两条（`TestOpenCodeFreeSeedProtocolsAreVerified` 强制种子表与出厂列表一一对应）。
- 新增迁移 019，复用 builtin 在 init 时注册的 `opencodeProtocolBackfill` 钩子再跑一轮空协议回填。幂等：只写 `upstream_protocol` 仍为空、且命中种子表的内置 `OpencodeCompat` 模型，不覆盖管理员非空值。
- 免费档改写不动：它只挂在 Chat 出站，muse-spark 一旦补成 `responses` 就不再经过那段注入，原生走 `/v1/responses`；chat 模型仍需要那段改写维持现状。

## 取代检查

- 部分更新 [OpenCode 模型行记录上游原生协议](../feature/2026-09-22-opencode-upstream-protocol.md)：把该篇「六个出厂模型都继承 `openai-compatible`」的事实就地补成「八个，muse-spark 两只模型级 `@ai-sdk/openai` → responses」，并补一句迁移 019 再回填。不翻转决定，不新开。
- 补充 [OpenCode Zen 免费档 Agent 形态请求改写](2026-09-29-opencode-free-tier-agent-shape-rewrite.md)：在「已知上限」补一条「Chat 改写对 Responses 原生模型注入会触发 tools[0] missing name，须靠协议种子避开」。两篇都是同一免费模型家族的不同缺陷，互不取代、不归档。
- 活跃树无 proposed / rejected 目录；无完全吸收或需要归档的笔记。

## 备选方案

- **把免费档改写改成按协议注入（Responses 模型注入顶层 `name` 工具）** — 治标；否掉是因为 muse-spark 本就该走 Responses 原生路径，让它在 Chat 路径里塞 Responses 工具是两层错误的叠加，且改写只在 `m.format == Chat` 时触发，改了也够不到 Responses 出站。
- **在 `modelProtocolProvider` 里对空协议做运行期种子兜底** — 不依赖 DB 迁移、自动愈合未来同步失败；否掉是因为把种子表从「出厂回填数据」升级成「运行期路由权威」扩大了契约面，而目录同步正常时根本不会空，真正缺口是「一次性回填不含后续补种」，一个再回填迁移就补齐。
- **直接删掉免费档的五个核心工具注入** — 去掉整类「工具格式错」的风险；否掉是因为当前仍以「免费档要求 Agent 形态」为前提（见 agent-shape 那篇），在没确证上游放宽前保留改写，只修协议路由这一处。

## 后果

- **收益**：muse-spark 免费模型按 Responses 原生出站，不再命中 Chat 工具注入，`tools[0] missing name` 消失；存量库经迁移 019 自动补上协议，无需手工改库。
- **代价与已知上限**：新增一条「再回填」迁移，种子表每次扩列都要另起迁移才能作用到存量库——这仍是「一次性回填」模型的固有代价。重访信号：Seeding 关系日后若迁到运行期兜底，可撤销 019 这类再回填迁移并回填普通化。

## 验证

`go build ./...`、`go vet ./internal/...`、`go test ./internal/model/... ./internal/builtin/... ./internal/relay/...` 通过；`TestOpenCodeFreeSeedProtocolsAreVerified` 锁定种子表与出厂列表一致性。匿名档实测 `https://opencode.ai/zen/v1/models`（`Bearer public`）返回的 11 个免费模型里仅 muse-spark 两只为非 chat 协议，其余全部 `@ai-sdk/openai-compatible`，佐证只补这两条即可。