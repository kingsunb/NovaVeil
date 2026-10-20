# Agent Note: OpenCode 免费档头集合对齐官方客户端（Accept / msg 编码 / v1 关联头）

Status: implemented

## 问题

OpenCode 免费档的传输层（Bun ClientHello 与 HTTP/1.1 头序）已在 2026-10-19 对齐，但请求头集合仍有三处与官方客户端的偏差，且都是每个请求固定带上的，不是偶发：

1. **`x-opencode-request` 的 id 编码方向反了**。`GenerateMessageID` 复用会话 ID 的 descending 编码（时间部分取反），而 opencode 的消息 ID 走 `Identifier.ascending()`，只有会话 ID 才是 descending。取反后解出的时间戳落在未来，同一会话内的消息序也颠倒。opencode2api 为此单列了 `AscendingID`，并在 `isOfficialSessionID` 的注释里点明"ascending 编码的 `ses_` 不是官方值"，即两种 ID 的编码方向必须分开，不能共用一套。
2. **`Accept` 被流式透传强制成 `text/event-stream`**。`sendPassthroughStream` 在 `BuildHttpRequest` 之后覆盖 Accept——这对普通渠道是必要的（客户端 Accept 属库自管头不会透传，不显式声明就拿不到 SSE），但 OpenCode 免费档因此发不出官方值：v1 模型请求是 `*/*`，v2 CLI 是 `application/json, text/event-stream`。
3. **v1 档多发两个关联头**。`injectOpencodeCorrelationHeaders` 不区分档位，把 `x-session-affinity` 与 `X-Session-Id` 一并镜像给了 v1；官方 1.x 客户端在该 provider 上只发 `x-opencode-session` 与 `x-opencode-session-id`。

不做会怎样：三处都在每次上游请求上出现，上游一旦按头集合或 id 编码做校验，免费档整档被拒；即便不拒，按指纹限流时这三处也是最容易被挑出的差异。

另有四处结构性差异，同样会让上游把请求判为非官方客户端：

4. **父子会话头缺失**。官方 v1 客户端在子代理会话里发 `x-opencode-parent-session-id` 与 `x-parent-session-id`，v2 CLI 发 `x-parent-session-id`；本仓一个都不发。
5. **项目 ID 恒定**。本仓对所有请求返回同一个固定 SHA-1，等于告诉上游"所有流量来自同一个工作目录"；官方 v1 发 git 根提交哈希（40 hex）或 `global`，v2 发由项目信号哈希出的裸 40-hex。
6. **v1 档工具集合与官方不符**。本仓对两档都注入五个核心工具，而官方 1.x 客户端在该档只保证有一个 `read`；多出的 shell/edit/glob/grep 是官方请求里不存在的形状。
7. **`anthropic-beta` 缺失**（仍未做，见下文后果）。

## 决定

本篇在 [opencode2api 兼容跟进](../architecture/2026-10-19-opencode2api-compat-followup.md) 已落地的 Bun 传输与 v1 动态头之上，补头集合层面的对齐。

改动全部门控在 OpenCode 免费档上——`isOpencodeFreeLane(channel)`：`channel.OpencodeCompat && model.OpenCodeTier(BaseURL) == "zen"`（覆盖 `/zen` 与 `/inference`，排除 `/zen/v1`、`/zen/go` 的 go 档）。非 OpenCode 渠道与免费档之外的 OpencodeCompat 渠道行为不变。

### msg_ 改用 ascending 编码（`internal/utils/opencodeid`）

`generateOpencodePrefixedID(prefix, descending bool)` 增加编码方向参数：`GenerateSessionID` 传 `true`（保持取反），`GenerateMessageID` 传 `false`（升序）。时间部分统一换算成 `uint64` 再移位取字节，不再依赖 `int64` 算术右移的符号扩展。

### Accept 按档位给官方值（`internal/relay/opencode_free.go`、`upstream.go`）

- `opencodeFreeLaneAccept(channel)` 返回该档官方 Accept：v1 `*/*`，v2 `application/json, text/event-stream`；非免费档返回空串。
- `applyOpencodeFreeLaneHeaders` 对两档都设 Accept；v2 其余头仍由渠道静态 `CustomHeader` 决定，v1 继续覆盖协议相关的 User-Agent 并发送 `x-opencode-request`。
- `sendPassthroughStream` 增加 `accept` 参数：非空时用它，空时维持 `text/event-stream`。`sendPassthrough` 的两条流式调用（streaming 与 collapsed）都传 `opencodeFreeLaneAccept(channel)` 的结果。

### v1 不再镜像 affinity 头（`internal/relay/random_headers.go`）

`injectOpencodeCorrelationHeaders` 在 `isOpencodeV1Lane` 为真时只写 `x-opencode-session-id`；`x-session-affinity` 与 `X-Session-Id` 留给 v2 档与其余 OpencodeCompat 渠道。客户端自己带来的头不主动删除，只停止由本仓注入。

### 父子会话头（`internal/relay/random_headers.go`、`internal/utils/opencodeid`）

父会话信号取入站 `x-parent-session-id` 头，其次取正文 `metadata.parent_session_id`。v1 档两端都发：`x-opencode-parent-session-id` 与 `x-parent-session-id` 均写 `SessionIDFromSignal(parent)` 规范化后的官方会话号（已是官方 descending 形态的原样保留）；v2 档只写 `x-parent-session-id`，值不重编码。

`opencodeid.SessionIDFromSignal` 由信号 `sha256("ses\0"+signal)` 派生：前 5 字节压成 48 位后取反作为时间部分（满足官方 descending 形态，第 10 位落在 `f`），后 10 字节定宽 Base62 作随机段。

### 项目 ID 按档位派生（`opencodeid.ProjectIDForLane`）

信号取入站 `x-opencode-project` 头，其次正文 `metadata.project_id`，都缺时用 `directory:novaveil:default-project`。v1 档下 `global` 与合法 40-hex（git 根提交哈希形态）原样返回；其余一律 `sha256("opencode-project\0"+signal)` 取前 20 字节成裸 40-hex。v2 档无条件走哈希。原固定 SHA-1 常量随之移除。

### v1 档工具集合收敛为单个 read（`internal/relay/opencode_free.go`）

`shapeOpencodeFreeBody` 增加 `v1Lane` 参数，工具策略按档位分叉：

- v1 档走 `ensureV1ReadTool`：客户端已声明名为 `read` 的工具则完全不动；否则追加一个描述为 DEPRECATED（明示模型不得调用）的 `read` 占位定义。客户端自带的 read 相关别名（`ReadFile`、`read_file` 等）保持原名不重命名。
- v2 档保持原有行为：确保五个核心工具存在，并用 embed 的真实定义替换同名工具。

两处调用点（`sendPassthrough` 与 `conversionMiddleware`）都传 `isOpencodeV1Lane(channel)`。

## 备选方案

- **让 `sendPassthroughStream` 尊重 `request.Headers` 里已有的 Accept** — 少一个参数，改动更小；否掉是因为"客户端 Accept 不透传、必须显式声明 SSE"是普通渠道的既有契约，改为信任调用方会让非流式客户端传来的 `application/json` 打穿到上游，本应 SSE 的响应被当 JSON 处理。
- **v1 的 Accept 也设成 `application/json, text/event-stream`** — 与 v2 统一、少一个分支；否掉是因为 1.x 客户端在该 provider 上由 AI SDK 以 fetch 默认值发包（抓包为 `*/*`），统一成 v2 的值只是把一处不一致换成另一处。
- **`x-opencode-request` 继续复用会话的计数器与取反逻辑** — 实现最省；否掉是因为消息 ID 与会话 ID 在官方客户端是两个不同的编码方向，共用一套等于让消息 ID 永久错下去。
- **直接删掉 v1 的 `x-session-affinity` / `X-Session-Id`（而非分档）** — 更彻底；否掉是因为这三个头在 v2 档是官方行为，删掉会破坏 v2 指纹，且影响非免费档的 OpencodeCompat 渠道。
- **v1 read 工具按参考实现做 alias**（把客户端 read 相关工具原地改名为 `read`，同步重写 `tool_choice` 与历史 `tool_calls`，并在响应里还原原工具名）— 与参考实现完全一致；否掉不是因为做不到：`handler.go` 的流转发循环已有帧级改写插入点（脱敏还原 `restoreStreamEvent`、模型名回显 `echoStreamResponseModel`），`window` 预读帧同样经过该处。真正的原因是代价：别名要穿过「请求改写 → 上游响应 → 流转发」三层才能还原，还需同时覆盖 Chat 原生帧与转换为 Anthropic/Responses 后的帧，等于为「少一个占位工具」引入一个贯穿参数和多协议改写路径；一旦漏还原，客户端会收到自己没声明过的工具名，工具回环直接断掉，比多一个 DEPRECATED 占位严重。当前实现只补占位、不改名，客户端工具语义完全不变。
- **把参考实现的 identity 体系整体搬过来**（多路会话信号派生 + 调用方 scope 隔离 + 项目/父会话统一建模）— 能力最全；否掉是因为本仓只需要把客户端带来的父会话与项目信号按档位渲染，引入 scope 派生会改变现有会话粘合语义，超出本次范围（YAGNI）。

## 后果

- **收益**：v1 的 `x-opencode-request` 时间戳方向与官方一致，同会话消息序不再颠倒；两档 Accept 都是官方值；v1 头集合不再多出官方不发的两个头；子会话带上官方形态的父会话头；项目号不再全局恒定；v1 请求的工具集合与官方一致（单个 `read`）。
- **代价与已知上限**：`sendPassthroughStream` 多一个参数，调用方必须显式传档位 Accept（传空即旧行为）；`opencodeid` 的 ID 生成多一个布尔分支；项目号与父会话号改为按入站信号派生，同一部署下不同项目会得到不同上游项目号（这正是目的，但会改变既有"全部署同一项目"的观测结果）。仍未跟进：`anthropic-beta`（v1 按 body 动态推导 / v2 固定串，当前内置模型无 anthropic 协议）、`#pool` 轮换代理池。v1 工具集合走"补占位不改名"，与参考实现的 alias 方案存在已知差异。
- 重访信号：上游开始按 `anthropic-beta` 校验，或客户端反馈注入的 deprecated `read` 占位被模型调用。
- 重访信号：上游开始按 `anthropic-beta` 校验，或客户端反馈注入的 deprecated `read` 占位被模型调用。

## 验证

- `GenerateMessageID()` 返回 `msg_` + 12 位小写 hex + 14 位字母数字，时间部分为升序（不经取反）；`GenerateSessionID()` 仍为取反。
- `opencodeFreeLaneAccept`：`/zen` → `*/*`，`/inference` → `application/json, text/event-stream`，`/zen/v1`、`/zen/go` → 空串。
- `grep -n "text/event-stream" internal/relay/upstream.go` 只剩默认分支；`sendPassthroughStream` 的所有调用点都传了非默认值来源。
- `injectOpencodeCorrelationHeaders` 中 `opencodeAffinityHeader` / `opencodeUpstreamSessID` 的写入都在 `!isOpencodeV1Lane(channel)` 分支内。
- `SessionIDFromSignal(s)` 输出满足 `^ses_[0-9a-f]{9}f[c-f][0-9a-f][0-9A-Za-z]{14}$`，同一输入两次调用同值。
- `ProjectIDForLane("", v1)` 与 `ProjectIDForLane("", false)` 均为 40 位小写 hex；`ProjectIDForLane("<40 hex>", true)` 原样返回；`ProjectIDForLane("global", true)` 返回 `global`。
- `shapeOpencodeFreeBody(body, true)`：正文已含 `function.name == "read"` 时返回原字节，否则追加一个同名占位；`shapeOpencodeFreeBody(body, false)` 仍补齐五个核心工具。
