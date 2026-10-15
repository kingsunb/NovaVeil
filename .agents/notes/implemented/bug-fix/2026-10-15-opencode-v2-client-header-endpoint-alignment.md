# Agent Note: opencode v2 客户端头与端点对齐

Status: implemented

## 问题

NovaVeil 的 opencode 兼容实现基于 2026-09-29 的逆向工程与 opencode2api 观察。opencode 官方 v2 客户端源码（版本 2.0.26，源码时间 2026-10-10）下载后逐项比对，发现五处偏差：

1. **User-Agent**：网关发 `opencode/1.18.33`，官方客户端发 `opencode/latest/<version>/<client>`（`packages/opencode/src/cli.ts:48` 的 `getUserAgent`）。
2. **免费端点**：网关用 `https://opencode.ai/zen`，opencode2api 2026-10-14 观察免费流量已迁至 `https://opencode.ai/inference`。官方客户端源码仍用 `/zen/v1`（免费与付费靠 apiKey 区分），与服务端迁移存在 4 天窗口。
3. **付费端点**：网关用 `https://opencode.ai/zen/go`，官方客户端用 `https://opencode.ai/zen/v1`。
4. **x-opencode-project**：网关发 `prj_` + 24-hex（SHA-256 前 12 字节），官方客户端用 `Hash.fast`（SHA-1）生成裸 40-hex（`packages/util/src/hash.ts`）。
5. **关联头集合**：网关发 `x-opencode-request`（`req_` + 32-hex），官方客户端不发送此头；官方客户端发 `x-opencode-session-id` 和 `X-Session-Id`，网关缺失。

不修正的后果：User-Agent 和 project ID 格式偏差增加上游指纹识别风险；缺失 `x-opencode-session-id` 可能被上游校验拒绝；`x-opencode-request` 是多余头，不帮过校验反而加大偏差。端点偏差导致免费请求打到一个可能已废弃的路径。

## 决定

以 opencode v2 官方客户端源码为权威，对齐以上五项，并同步更新档位检测和存量数据迁移：

### User-Agent

内置渠道 UA 改为 `opencode/latest/2.0.29/cli`（免费）和 `opencode/latest/2.0.29/desktop`（付费），版本号取官方客户端当前版本 2.0.29（源码 2.0.26 + 预期补丁）。`internal/builtin/builtin.go`。

### 端点

- 免费档：`https://opencode.ai/zen` → `https://opencode.ai/inference`（跟随 opencode2api 2026-10-14 观察）。
- 付费档：`https://opencode.ai/zen/go` → `https://opencode.ai/zen/v1`（官方客户端源码）。

### x-opencode-project

`ProjectID()` 从 `prj_` + hex(SHA-256[:12]) 改为裸 hex(SHA-1)，对齐官方 `Hash.fast = SHA-1`。`internal/utils/opencodeid/opencodeid.go`。`GenerateSessionID()` 经逐行比对确认与官方完全一致，不改。

### 关联头

`internal/relay/random_headers.go` 的 `injectOpencodeCorrelationHeaders`：

- 新增 `x-opencode-session-id`（`x-opencode-session-id`）和 `X-Session-Id`，两者均镜像 `x-opencode-session` 值。
- 删除 `x-opencode-request` 注入（官方客户端不发送）。
- 保留 `x-session-affinity` 和 `x-opencode-project`（后者值改为裸 40-hex）。

`internal/helper/fetch.go` 的 `applyOpencodeCompatHeaders` 同步生成 `x-opencode-session-id`，与 `x-opencode-session` 同值。

### OpenCodeTier

`internal/model/opencode_protocol.go` 的 `OpenCodeTier` 更新端点识别：

- `/zen/v1` → `go`（付费档，新端点）
- `/inference` → `zen`（免费档，新端点）
- `/zen/go` → `go`（旧付费端点，保留兼容）
- `/zen` → `zen`（旧免费端点，保留兼容）

`go` 模式先于 `zen` 匹配，因为 `/zen/v1` 含 `/zen` 子串。旧端点保留是为了已落库的自定义渠道（管理员手动配的 BaseURL）不被档位检测破坏。

### Migration 020

`internal/db/migrate/020.go`：`EnsureBuiltinChannels` 只补建缺失渠道、不覆盖已存在的同名渠道（`builtin.go:338` 的 `continue`）。升级代码后存量库里 "OpenCode Free" / "OpenCode" 渠道仍保留旧 BaseURL 和旧 UA。Migration 020 按 `builtin = true AND opencode_compat = true` 筛选渠道，按旧 BaseURL 模式（`/zen/go` → 付费、精确 `https://opencode.ai/zen` → 免费）匹配并就地更新 BaseURL 和 CustomHeader 里的 User-Agent。管理员自定义的 BaseURL（不匹配旧模式）不动；已是新端点的渠道跳过（幂等）。

## 取代检查

- 部分更新 [OpenCode Zen 免费档 Agent 形态请求改写](2026-09-29-opencode-free-tier-agent-shape-rewrite.md) 里的关联头事实（`x-opencode-request` 删除、`x-opencode-project` 改裸 40-hex、加 `x-opencode-session-id`/`X-Session-Id`），body 改写决定不变，互链不取代、不归档。
- 正交 [测试与后台探测经专用参数注入合法 opencode 会话号](2026-09-29-opencode-diag-probe-session-placeholder.md)：那篇修 400 `MissingSessionID`，本篇修头集合与端点偏差，互不取代。
- 正交 [opencode 上游协议](../feature/2026-09-22-opencode-upstream-protocol.md) 和 [opencode session 与 prompt cache key](../feature/2026-09-22-opencode-session-and-prompt-cache-key.md)：那两篇关于协议列和会话号算法，本篇不改这两项。
- 活跃树无 proposed / rejected 目录；无完全吸收或需要归档的笔记。

## 备选方案

- **免费端点跟官方客户端用 `/zen/v1`** — 官方源码权威性最高；否掉是因为 opencode2api 2026-10-14 观察免费流量已迁至 `/inference`，官方客户端源码（2026-10-10）可能尚未跟上服务端迁移。若实测 `/inference` 返回 403，改回 `/zen/v1` 只需改 builtin.go 一行，`OpenCodeTier` 已同时识别两个端点。
- **保留 `x-opencode-request` 头** — 多一个头不会帮我们通过校验；否掉是因为官方客户端源码确认不发送 `x-opencode-request`（`model-request.ts` 头集合里无此项），多发一个头增加指纹偏差而非减少。`GenerateRequestID()` 函数保留定义但不再调用，以备内部诊断需要。
- **保留 `prj_` 前缀的 project ID** — `prj_` 是我们自己加的前缀；否掉是因为官方用 `Hash.fast = SHA-1` 生成裸 hex，前缀与官方不匹配。SHA-256 → SHA-1 的降级在 project ID 场景无安全影响（project ID 是确定性标识符，不是抗碰撞凭据）。
- **不加 migration，只改 builtin.go** — 改动最小；否掉是因为 `EnsureBuiltinChannels` 不覆盖已存在渠道，存量部署升级后 DB 里仍是旧端点和旧 UA，新代码的端点改动不生效，只有全新部署才用新值。这会让「升级了但没生效」成为隐蔽陷阱。
- **在 `EnsureBuiltinChannels` 里就地更新而非走 migration** — 避免新增迁移编号；否掉是因为启动时静默改 DB 数据不如显式 migration 可追溯，且 migration 记录表让管理员能确认刷新已执行。

## 后果

- **收益**：opencode 兼容头集合、端点、project ID 格式与官方 v2 客户端对齐，减少上游指纹偏差和拒绝风险。存量部署升级后 DB 渠道自动刷新到新端点和新 UA，无需管理员手动改渠道编辑器。
- **代价与已知上限**：`/inference` 端点基于 opencode2api 观察（2026-10-14）而非官方客户端源码（2026-10-10 用 `/zen/v1`），若服务端未迁移或已回退，免费请求会 403——回退方案是改 builtin.go 一行 BaseURL 回 `/zen/v1`。Migration 020 改存量 DB 数据（`base_url` 和 `custom_header` 里的 User-Agent），但只动 `builtin = true AND opencode_compat = true` 的渠道，且只匹配旧端点模式，管理员自定义的 BaseURL 不碰。`GenerateRequestID()` 成为死代码（定义但无调用方），保留以备内部诊断。重访信号：opencode 官方客户端版本更新导致 UA 或头集合再次偏移，或 `/inference` 端点被服务端废弃。

## 验证

`go build ./...`、`go vet ./...`、`go test ./internal/relay/... ./internal/helper/... ./internal/model/... ./internal/utils/...` 通过。`internal/relay/opencode_free_test.go` 覆盖 `/inference` → zen 和 `/zen/v1` → go 的档位判定。`internal/relay/random_headers_test.go` 覆盖 `x-opencode-session-id`/`X-Session-Id` 镜像与裸 40-hex project 格式，已删除所有 `x-opencode-request` 断言。`internal/helper/fetch_test.go` 覆盖模型同步路径注入 `x-opencode-session-id` 且与 `x-opencode-session` 同值。`internal/db/migrate/020.go` 的 `opencodeEndpointUpgrade` 按旧端点模式匹配，新端点和自定义端点返回空串跳过（幂等）。
