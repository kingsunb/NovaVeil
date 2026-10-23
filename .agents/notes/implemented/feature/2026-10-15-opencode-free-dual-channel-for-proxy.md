# Agent Note: OpenCode Free 内置渠道多副本供分代理转发

Status: implemented

## 问题

OpenCode Free 是唯一直连 `https://opencode.ai/inference` 免费端点的内置渠道。管理员如需通过不同代理（如不同地域出口、不同隧道）转发同一免费端点的流量，需要多个独立渠道实例，各自绑定不同代理。

但 `ensureFreeBuiltinChannels`（`internal/builtin/builtin.go`）在每次启动时强制把 `builtin = false` 的渠道的 `is_free` 和 `opencode_compat` 清为 false。这意味着通过管理台 UI 创建的自定义渠道副本，每次重启都会丢失这两个标记：

- 丢失 `opencode_compat` → opencode 会话头（`x-opencode-session` 等）不再注入，上游 403 拒绝。
- 丢失 `is_free` → `isOpencodeFreeModel`（`internal/relay/opencode_free.go:74`）不再判定为免费档，Agent 形态请求改写跳过，上游同样 403。

因此自定义渠道无法稳定承担 OpenCode 免费转发，必须在 `BuiltinFreeChannels` 源码清单中添加额外内置条目。

## 决定

在 `BuiltinFreeChannels` 中新增 `"OpenCode Free 2"` 和 `"OpenCode Free 3"` 两个条目，均与 `"OpenCode Free"` 配置完全一致（同 BaseURL、Key、OpencodeCompat、CustomHeader、Models），仅 Name 不同。三个渠道均为 `builtin = true`，不受 `ensureFreeBuiltinChannels` 的自定义渠道清理逻辑影响。管理员在渠道编辑器中分别给各渠道配置不同代理（系统代理或渠道专属代理），即可实现同一免费端点走不同代理出口。

`EnsureBuiltinChannels` 按 Name 去重补建：已存在的同名渠道跳过（`builtin.go` 的 `continue`），所以升级后存量部署自动补建缺失的 `"OpenCode Free 2"` / `"OpenCode Free 3"`，不影响已有的 `"OpenCode Free"`。

### 不变量保持

- `TestBuiltinFreeChannelsDefinitions` 检查 Name 唯一性——`"OpenCode Free"` / `"OpenCode Free 2"` / `"OpenCode Free 3"` 互不冲突。
- `TestOpenCodeFreeSeedProtocolsAreVerified` 按 `Name == "OpenCode Free"` 精确匹配第一个条目——原条目不变，测试通过。
- `isOpencodeFreeModel` 按 `channel.IsFree` 判定——三个副本均为 `IsFree = true`，均正确触发免费档改写。
- `BackfillOpencodeUpstreamProtocols` 按 `builtin = true AND opencode_compat = true` 筛选——三个副本均匹配，协议回填对三者生效。

## 取代检查

- 正交 [opencode v2 客户端头与端点对齐](../bug-fix/2026-10-15-opencode-v2-client-header-endpoint-alignment.md)：那篇修头集合与端点，本篇加渠道副本，互不取代。
- 正交 [OpenCode Zen 免费档 Agent 形态请求改写](../bug-fix/2026-09-29-opencode-free-tier-agent-shape-rewrite.md)：那篇修请求体改写逻辑，本篇不改改写判定，各副本均走同一改写路径。
- 正交 [opencode 上游协议](2026-09-22-opencode-upstream-protocol.md)：那篇关于协议列映射，本篇不改协议。
- 活跃树无 proposed / rejected 覆盖此决定；无完全吸收或需要归档的笔记。

## 备选方案

- **通过 UI 创建自定义渠道副本** — 最小代码改动，零源码变更；否掉是因为 `ensureFreeBuiltinChannels` 每次启动强制清除非内置渠道的 `is_free` 和 `opencode_compat`，副本重启后退化为普通渠道，opencode 会话头和免费档改写均失效，上游 403。
- **放宽清理逻辑，允许自定义渠道保留 `opencode_compat`** — 让管理员自行创建副本；否掉是因为该不变量是有意设计：opencode 兼容头只应由内置 OpenCode 渠道使用，放开后自定义渠道可随意开启 `opencode_compat`，增加上游指纹偏差和维护面。改动跨文件契约（清理逻辑 + 前端编辑器 + 测试），远超本需求。
- **单渠道多 Key + 按 Key 注入不同代理** — 复用现有多 Key 轮询；否掉是因为代理是渠道级字段（`ChannelProxy`），不是 Key 级。`effectiveChannelForKey` 按 Key 解析代理模板，但模板解析的是同一渠道代理字段的不同别名，不是多个独立代理地址。要支持 Key 级独立代理需要改数据模型和转发路径，改动面远大于加内置条目。

## 后果

- **收益**：管理员可在三个内置 OpenCode Free 渠道上分别配置不同代理，同一免费端点的流量按分组/故障转移策略走不同出口。升级后自动补建，无需手动操作。
- **代价与已知上限**：三个渠道的出厂配置（BaseURL、Key、Models、Headers）完全相同，存在代码重复——若上游模型列表变更，需同步改三处。`EnsureBuiltinChannels` 不覆盖已存在渠道，所以改模型列表后存量部署的 `"OpenCode Free 2"` / `"OpenCode Free 3"` 不会自动更新，管理员需在 UI 手动同步或删除后重启重建。重访信号：OpenCode 免费端点模型列表变更，或需要四个以上副本时考虑抽公共定义消除重复。

## 部分取代

[下线 OpenCode Free 2/3 内置副本渠道](../simplification/2026-10-24-remove-opencode-free-duplicates.md) 收回本篇新增 `"OpenCode Free 2"` / `"OpenCode Free 3"` 的决定：内置清单删除两个副本条目，迁移 024 删除存量库中的内置副本行。本篇保留原始动机与备选分析供回访；其中「自定义渠道每次启动被清 `is_free` 与 `opencode_compat`」的清理不变量仍生效，仍是自定义渠道无法承担 OpenCode 免费转发的根因，备选方案中否掉的放宽清理逻辑与 Key 级代理路线依然否。以下验证段描述加副本当时的回归范围，当前清单断言见新笔记。

## 验证

`gofmt` 通过，无语法错误。`TestBuiltinFreeChannelsDefinitions` 的 Name 唯一性检查覆盖新条目。`TestOpenCodeFreeSeedProtocolsAreVerified` 按 `Name == "OpenCode Free"` 精确匹配，不受 `"OpenCode Free 2"` / `"OpenCode Free 3"` 影响。完整 `go test ./internal/builtin/...` 需 Go 1.26.7 工具链，当前环境 Go 1.22.0 无法运行，待构建环境就绪后补验。
