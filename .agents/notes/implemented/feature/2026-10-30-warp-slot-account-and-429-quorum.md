# Agent Note: WARP 出口槽位账号化与 429 阈值判定

Status: implemented

## 问题

OpenCode 免费档的代理账号制度（见 [按出口 IP 轮换代理账号池](2026-10-20-opencode-free-proxy-ip-pool.md)）假设代理服务商按账号分派出口：`{account}` 填进代理 URL 的 userinfo，服务商据此把连接导向一个出口，NovaVeil 才能把 403/429 归因到具体出口并只冷却那一个账号（见 [OpenCode 免费档 429 冷却代理账号 24 小时并换号](../bug-fix/2026-10-22-opencode-429-account-cooldown.md)）。

WARP 场景不满足这个前提。WARP 是本地自建的出口池，出口由 WARP 侧决定，NovaVeil 只拿到一个固定地址 `socks5://NovaVeil_WARP_socks5:1080`。渠道代理不含 `{account}`，`proxyNeedsOpencodeAccount` 返回 false，`proxyAccount` 恒为空，于是走无参 `POST /penalize`。无参语义是淘汰最近窗口内用过的所有出口；一次 429 会把多个正在服务的出口一起端掉，池子从 N 掉到 1，补池期间所有重试挤到仅剩出口上。根因是归因缺失，不是判定阈值：NovaVeil 知道某个请求被拒，却不知道是哪个出口承载的。

WARP 侧的 `exit.observe()` 与 `Pool.Report()` 原本可按状态码精确淘汰，但代理层是纯字节转发，OpenCode 上游走 CONNECT + TLS，看不见密文里的 429/403，这条路径在架构上不可用。另外，旧实现对 403 与 429 一视同仁；403 更可能是请求形态或身份惩罚信号，不能和「/64 配额耗尽」混成一个配额问题，这与仓库 `AGENTS.md` 的「429 与额度类错误不得当配额问题结案」一致。

## 决定

WARP 出口被改成一组编号稳定的槽位：`slot N` 在进程生命周期内不变，槽位绑定一个出口并持有 `Pool` 引用计数。NovaVeil 通过槽位账号选路，把每个在途请求固定到一个出口；429 先归因到槽位，再由不同 `/64` 的票数决定是否全池换桶。

### WARP 侧：槽位、代理入口与轮换

- 槽位号 `0..size-1`，账号统一为 `warp-<slot>`，用户名不含冒号。槽位绑定 `Pool` 的 refcount：新建绑定 `pins++`，解绑 `pins--`，换绑保留 pins；`desiredLocked` 至少等于槽位数，避免默认 4 个槽位只有 1 个出口。
- `GET /slots`（控制端口 1081）返回 `size`、`updated_at` 和 `slots`。每个槽位含 `slot`、`account`、`egress`、`prefix`、`state`、`in_flight`、`cooling_until`、`generation`。`state` 为 `active`（可派发且空闲）、`busy`（有在途）、`cooling`（该出口 /64 冷却中）、`unknown`（暂时没有绑定出口）。出口替换时槽位号不变，`generation` 加一。
- HTTP 代理（8080）从 `Proxy-Authorization: Basic` 的用户名读取槽位，密码忽略；SOCKS5（1080）走 RFC 1929（即：SOCKS5 用户名密码认证）。无 `warp-` 前缀的用户名继续走原来的轮询，保证旧配置可用；`warp-` 前缀但槽位号非法的用户名按未知槽位返回 407。
- 未知槽位返回 `407` 并带 `Proxy-Authenticate: Basic realm="warp-slots"`；`busy`、`cooling`、`unknown` 返回 `503` 并带 `Retry-After`（至少 1 秒）。不允许静默换到别的出口，否则归因再次失效。每个槽位同时只接一个在途连接，由 WARP 侧兜底。
- 403 不触发轮换；只有 429 或网络失败才调用 `/penalize?egress=`。轮换先复用旧注册、换外层源地址做 soft restart（零注册）；新出口的 `/64`（IPv4 为 `/32`）与旧出口相同才升级 full re-key。全池轮换由 `rotateMu` 串行，相邻两次至少间隔 4 秒；单出口 key 每小时最多 2 次，全池 full re-key 每小时最多 60 次；连续失败按 `300 * 2^n` 秒退避，封顶 3600 秒。预算耗尽或退避期间恢复旧出口，不无限注册。
- 槽位出口不参与 `Check` / `scaleDown` 的主动巡检，避免巡检把正在服务的槽位出口淘汰；槽位出口的健康由真实请求失败驱动。`Close` 会释放槽位引用、清空槽位并 retire 活跃出口。

### NovaVeil 侧：选号、租约与 429 判定

- 所有 NovaVeil 改动以 `isWarpProxy(channel)` 为门，resin 渠道走原分支；WARP 状态只存内存，不写 `opencode_proxy_accounts`。
- `warpSlotPool` 以控制地址为键缓存 `/slots`，TTL 1 秒。选号只取 `state == "active"`、`account`/`egress` 非空、`in_flight + 本地 reservation == 0`、未被本地 suspect 的槽位；同状态按轮询推进。全忙返回 `warpSlotsBusyError`，`rejectRequest` 映射为 503 + `Retry-After`（向上取整，至少 1 秒，最多 1 分钟），不生成随机账号。
- 租约通过 `context.AfterFunc(roundCtx, lease.Release)` 按轮次释放，不按整个请求释放；429 换号后旧槽位立即可用，一次请求重试 3 次不会长期占住 4 个槽位。重试预算沿用 `ocProxyMaxRetries = 3`。
- 单个槽位 429 只设该槽位 `suspectUntil = now + 60s`，不立即做 24 小时冷却。60 秒窗口内记录不同 `/64` 前缀的票；去重后达到 3 个不同 `/64` 才把全池冷却 60 秒，异步 `POST /rotate?all` 并失效 `/slots` 缓存。403 不计票，也不惩罚槽位；代理层 407/503 走 `MarkProxyUnavailable`（2 秒 suspect），不记成员真实失败。
- 网络失败（无上游 HTTP 状态码且非取消）才异步 `POST /penalize?egress=`；有 HTTP 状态码的 400/401/500/502 等不再误 penalize 出口。HTTP 标准库 CONNECT 非 200 只保留原因短语，503 文本是 `Service Unavailable`，因此新增 `isWarpSlotUnavailable` 只在无状态码分支识别它；Bun HTTP 代理的 `proxy refused CONNECT: 503 ...` 由 `isWarpProxyRejection` 覆盖；`502 Bad Gateway` 不匹配，按网络失败惩罚。
- WARP 模板必须把 `{account}` 放在用户名。解析后校验 `resolved.User != nil`、`Username() == proxyAccount`、且 `resolved.String()` 不再含占位符；失败返回 `errWarpProxyTemplateInvalid`，不静默退化为轮询。`warpControlBase` 解析失败返回 `errWarpProxyInvalid`，不把可能含密码的原始 URL 带进错误响应。
- `HasAccountPlaceholder` 同时识别字面量 `{account}` 与百分号转义的大小写组合（`%7Baccount%7D`、`%7baccount%7d` 等），避免小写转义漏掉槽位分支。
- 辅助路径不解析 WARP 的 `{account}`：`helper.ResolveChannelProxyTemplate` 对 WARP 置 `ChannelProxy = nil`（直连/系统代理）；`effectiveChannelForKey` 保留槽位模板；`effectiveProbeChannel`、`effectiveTestChannel`、`probeOnceWithAccount`、`prepareAccountProbe`、`ListOpencodeAccountStates`、`AutoProbeOpencodeAccounts` 对 WARP 返回哨兵或跳过；模型同步、辅助抓取、渠道测试、账号体检、上下文探测都不生成未知槽位、不写库。

### 槽位契约

```json
{
  "size": 4,
  "updated_at": "2026-10-30T12:00:00Z",
  "slots": [
    {
      "slot": 0,
      "account": "warp-0",
      "egress": "2a09:bac5:4733:1a14:0:0:299:42",
      "prefix": "2a09:bac5:4733:1a14::/64",
      "state": "active",
      "in_flight": 0,
      "cooling_until": null,
      "generation": 3
    }
  ]
}
```

- `slot` 从 0 到 `size-1`，进程内稳定；出口替换时槽位号不变，`generation` 加一。
- `egress` 是完整公网地址，`prefix` 是它的 `/64`（IPv4 为 `/32`）；NovaVeil 用 `egress` 调 `/penalize?egress=`，用去重后的 `prefix` 做 429 计票。
- 槽位绑定 `Pool` 的租约/refcount，池子缩容或替换出口后 `/slots` 不出现悬空出口；实际槽位数与 `-pool-size` 一致。
- NovaVeil 的 `/slots` 缓存不超过 1 秒；只选 `active`，按 `in_flight` 加本地 reservation 最少、再轮询；无可用槽位直接返回忙。

### 429 阈值与轮换预算

- 单槽 429 只标记 suspect 60 秒，不立即进入 24 小时冷却；60 秒内 3 个不同 `/64` 都返回 429 才判定为上游整体限流，全槽冷却 60 秒并 `POST /rotate?all`。
- 403 不算配额票，不触发轮换；403 的处理仍回到请求形态与身份比对。
- 窗口内不足 3 个槽位或 3 个 `/64` 时，阈值可能不成立；单槽 suspect 60 秒是退路，不会无限等待。
- 社区实现 `trefeon/warp-relay-pool` 不做阈值判定，单出口 429 就轮换那一个出口，靠冷却退避与每小时预算防抖。本实现把两者叠加：阈值判定管「要不要整体换桶」，轮换预算管「单个出口多久换一次」。
- 轮换后不另建探测器，以下一条真实请求验证；失败继续按预算轮换，403 不触发轮换。

### 出口新鲜度

Cloudflare 从有限的 `/64` 组合中分配 WARP 出口前缀；重新注册拿到的是隧道内 `/128`，外部出口前缀由边缘另配，因此「换注册」不等于「换 `/64`」。本实现按「槽位之间 `/64` 互不相同」设计，并在轮换后比较 `/64`，只变主机位不算换段；soft restart 未换段才升级 full re-key。该前提仍需在生产环境实测确认：若 `/64` 池很小或服务端按设备/来源做前缀亲和，槽位会退化为多个账号共用一个桶，阈值判定与并发隔离随之失效。

## 备选方案

- **保留无参 `/penalize` + recent tracker** — 改动最小，但无法归因到具体出口，一次 429 仍会连坐多个出口；只适合作为止血，不能作为槽位方案。
- **一个槽位一个独立端口，账号即端口** — 避免了账号传递，但 `ResolveProxyTemplate` 的占位符只在 userinfo 生效，端口填不进账号；端口数固定等于池大小，池子无法按负载动态伸缩。
- **由 WARP 侧自行统计 429 并判定** — 代理层在 CONNECT 隧道内看不见 TLS 里的状态码，`observe()` / `Report()` 没有调用者；这不是补一个调用点能解决的。
- **直接用出口 IP 当账号标识** — 语义直观，但出口随轮换与淘汰而变，账号台账会持续积攒失效账号，冷却状态也无法跨进程重启保留。
- **WARP 槽位复用 `opencode_proxy_accounts` 表落库** — 槽位号稳定，落库后跨重启能记住限流状态；否掉是因为会污染 resin 账号表，渠道切回 resin 后无法区分哪些行是真正的 resin 账号。
- **WARP 槽位与 resin 账号同池但不区分来源** — 选号逻辑一行不改，最省事；否掉是因为 `ocProxyPools` 按渠道 ID 缓存，渠道从 WARP 切回 resin 时不会重新载入，槽位账号会被当作 resin 账号填进 resin 模板。
- **为 WARP 新写一套独立账号池** — 隔离最彻底；否掉是因为要重写选号、并发计数、冷却判定、容量淘汰一整套逻辑，与「其余按原有的来」相悖，同池加前缀过滤能达到同等隔离效果。
- **单次 429 立即冷却 24 小时** — 与 WARP 场景的归因缺失冲突，实际退化为无差别连坐；阈值判定避免误伤，代价是单出口真正耗尽时仍会继续派发，需要单槽 suspect 与轮换预算兜底。

## 后果

- **收益**：每个在途请求固定到一个出口，429 能归因到槽位；单个槽位 429 不再连坐其他出口；403 与 429 分开处理；resin 账号体系零 schema、零表行变更；辅助路径不再生成未知槽位或写库。
- **容量**：每槽并发 1，默认 `-pool-size=4`，网关只有 4 个 WARP 并发；全忙时返回 503 + `Retry-After`，需要按上游免费档配额与请求延迟计算容量。
- **状态不持久**：NovaVeil 的 suspect/全冷状态、WARP 的段冷却都在内存；进程或 WARP 重启后清零，不能把 WARP 侧冷却当成跨重启的持久保证。WARP 重启后槽位号可能保持不变但出口已换，NovaVeil 内存里的旧 suspect 会短暂压制新出口。
- **轮换权衡**：预算耗尽或退避期间 `restoreExit` 会清 penalty/cooling，把 429 出口放回服务，可能再次 429；这是「不无限注册」的代价。soft restart 在没有 `sourcePrefix` 时复用同源，必然同 `/64`，会升级 full re-key，注册量增加。
- **槽位出口不巡检**：`Check` / `scaleDown` 跳过 slotBound 出口，槽位出口只靠真实请求失败驱动，坏出口可能多接一次请求才被替换。
- **HTTP 代理文本判定**：标准库 CONNECT 非 200 只保留原因短语，503 的 `Service Unavailable` 需要单独识别；`502 Bad Gateway` 仍按网络失败惩罚。Bun sidecar 的 `Proxy-Authorization` 与分池行为未在本轮实抓验证，启用 sidecar 时需以实际请求确认。
- **阈值边界**：并发低于 3 时 3 个不同 `/64` 的阈值不会成立，单槽 suspect 60 秒是退路；这会把「整体限流」退化为「单槽暂时避开」，不会死等。
- **`/64` 前提**：槽位机制假设槽位之间 `/64` 互不相同；该前提未在生产环境实测，若 `/64` 池很小或存在前缀亲和，阈值判定与并发隔离会退化。
- **验证边界**：本轮只做静态阅读与格式检查，未执行 `go build`、`go test`、`go vet`、`pnpm` 或启动服务；编译、测试与容器联调由用户执行。

## 验证

本轮新增或更新的测试：

- WARP 仓：`internal/warp/slots_test.go` 覆盖槽位解析、错误映射、初始绑定、换绑保留旧出口、解绑归还 pins、`desiredLocked` 覆盖槽位数、槽位状态；`internal/warp/rotation_test.go` 覆盖退避、预算、前缀比较；`internal/httpproxy/httpproxy_test.go` 覆盖 Basic 用户名与 407/503 响应；`internal/socks5/socks5_test.go` 覆盖 RFC1929。
- NovaVeil：`internal/relay/warp_slots_test.go` 覆盖轮询选槽、全忙、429 三票全冷、缓存 TTL、槽位表校验、控制地址解析、代理拒绝识别、`Service Unavailable` 识别、WARP 探测哨兵、WARP 模板保留占位符；`internal/model/setting_proxy_pool_test.go` 覆盖大小写百分号转义；`internal/helper/proxytemplate_test.go` 覆盖 WARP 模板置空与 resin 不受影响。

用户可执行的验证命令：

```sh
# WARP 仓（当前工作副本或已回拷的 NovaVeil_WARP-main）
cd D:/AI/github/kingsun/NovaVeil_WARP-main
go test ./...
go vet ./...

# NovaVeil
cd D:/AI/github/kingsun/NovaVeil
go test ./internal/relay ./internal/model ./internal/helper
go test ./internal/relay -run 'Warp|IsWarp'
go vet ./...

# Agent Note 门禁
pnpm verify-notes
```

容器联调时先确认 `GET http://127.0.0.1:1081/slots` 返回 4 个槽位，再用 `curl -x http://warp-0:any@127.0.0.1:8080 https://api.ipify.org` 与 `curl -x socks5h://warp-0:any@127.0.0.1:1080 https://api.ipify.org` 验证槽位选路；未知槽位应返回 407，忙/冷却应返回 503 与 `Retry-After`。
