# 转发引擎（internal/relay）

`relay` 是网关的核心：`/v1/*` 的每一次转发、分组选路、熔断、会话粘合、Key 轮询、协议互转、脱敏挂载、请求体预算与全链路可视化都在这个包里。分层上 relay 不反向依赖 server（HTTP 层），向下只使用 op（业务与缓存）与 model。

## 怎么做的

### 请求生命周期

一次 `/v1/chat/completions` 请求在 `handler.go` 的 `Forward` 中走完：

1. 全局停检查 + 进程级 body 预算准入（`bodylimit.go`：单请求压缩 16 MiB / 解压 64 MiB，进程总预算 128 MiB，超额返回 413，不进熔断不记失败）。
2. 协议自动检测：客户端把 Anthropic 原生格式发到 chat 端点时自动切换入站转换器。
3. 用 gjson 单字段提取 `model`——**模型名即分组名**（`op.GroupGetByName`）。
4. 会话键 `sessionScopeKey`：`X-Session-Id` 缺省回退 `x-opencode-session`；有 API Key 时格式为 `apiKeyID:sessionKey`。
5. 请求脱敏（fail-closed，见 [mask.md](mask.md)）。
6. 重试循环：`maxRequestRounds` 与请求总截止时间双保险，每轮重读分组配置。
7. 每轮选路：会话粘合 → `pickGroupItemWithContext` → 引用链解析（分组成员可以指向另一个分组，逐层解析到叶子成员）。
8. 多 Key `selectChannelKey`（游标轮询 + Key 级冷却）+ 渠道 RPM 滑动窗口等待。
9. `buildOutbound` 按渠道类型与模型协议装配出站转换器，判定 `supportsNativeFormat` 走透传。
10. 失败分类记账（业务/基础设施错误独立计数）→ 熔断/冷却 → `exclude` 换下一成员重试。

### 三态熔断与紧急兜底

路由状态 `RouteState`（Cooldowns / Levels / HalfOpens / ProbeItemID / PostCommitStrikes / emergencyCounts）全部是进程内存，单锁 `routeMu` 保护，临界区内不做 IO：

- CLOSED 成员直接选中；OPEN 成员按 `cooldownMillis` 指数退避，受冷却上限封顶。
- 存在 CLOSED 成员时，对冷却到期的 OPEN 成员做**异步非阻塞探测**，本请求照常服务。
- 全组冷却到期时整批原子切换 HALF_OPEN 并行探测，最先成功者胜出、置 `ProbeItemID` 等待真实业务请求二次确认（`recordRouteSuccess` 才真正恢复 CLOSED）。
- 全部成员不可用时走紧急兜底 `claimEmergencyItem`，以 `emergencyMaxConcurrent=3` 的信号量节流放行最后防线。
- 完整流程图与配置项见 [DEVELOPMENT_routing.md](../DEVELOPMENT_routing.md)。

### 协议互转与透传

- 协议转换核心使用外部库 `github.com/looplj/axonhub/llm`：入站支持 OpenAI Chat / OpenAI Responses / Anthropic Messages，出站另支持 Gemini / Volcengine / Custom。
- **完全透传 = 任意协议原样转发，但不跳过分组路由、failover、Key 轮询**（见 [CHANNEL_PASSTHROUGH.md](../CHANNEL_PASSTHROUGH.md)）。
- OpenCode 按模型协议：`ChannelModel.upstream_protocol` 只允许空 / `chat` / `responses` / `anthropic`，空值按渠道类型转发。协议来源分三层——种子（出厂免费模型）、目录同步（仅 `OpencodeCompat` 渠道读 OpenCode 公开能力目录）、未知（留空按 Chat 渠道转发）；目录失败只跳过协议填充，不抹掉已写的非空值。
- OpenCode 会话头：入站合法 `ses_` 原样上送（优先级 `x-opencode-session` > `X-Session-Id`，都不合法新铸且不进缓存）；`prompt_cache_key` 只在转到 Chat / Responses 且出站还没有该字段时从脱敏后的正文回写，Anthropic 出站不发明该字段。
- 透传响应头按 blocklist 过滤：`Set-Cookie` / `Location` / `WWW-Authenticate` 及逐跳头一律丢弃，防止恶意上游的 `Set-Cookie` 驱逐管理台认证 cookie。

### 全链路可视化与请求状态

`state.go` 的 RequestState 记录每轮 startRound / finishRound（含渠道 / Key / 代理 / 协议标签与每次尝试轨迹），`OpenRouteStream` 以 SSE 向管理台推送路由状态快照与增量；失败正文截到 64 KiB，API Key 只存尾 4 位。

## 设计想法

- **模型名即分组名**：客户端不需要知道后端有几条渠道，分组本身就是对外暴露的模型；跨供应商故障转移对客户端完全透明。
- **非阻塞半开探测**：恢复探测绝不阻塞业务请求；探测胜出只是"候选"，业务二次确认才真正恢复——防止一个抖动渠道在探测成功瞬间被打回 OPEN 的同时又被放进流量。
- **提交后失败静默截断**：流已提交给客户端后失败，不补发协议内错误帧。实测 Codex / opencode 等 Agent CLI 对「200 + 流内错误帧」只做异常展示不重试，而对「SSE 流缺终止事件」会整体自动重试——静默截断恰好命中客户端重试路径；服务端仍按失败记账、落错误日志、累计成员连击。
- **panic 兜底幂等**：转发循环内 recover 后幂等归还探测占用与半开标记（`releaseRefChainProbeHolds` 对无持有的跳为无操作），防止半开泄漏把整组钉死到重启。
- **单实例内存态**：熔断 / 粘合 / Key 冷却 / 可视化流全部在进程内存，不引入共享存储。单实例部署是明确边界，水平扩展在 ROADMAP 的"明确不做"清单里。

## 已知边界与缺陷

- **【未修复·高】提交后失败路径泄漏探测占用**：`handler.go` 中 `firstErr` / `polluted` / `frameFailure` 的定稿路径只调 `recordPostCommitFailure` 后直接 return，不调 `releaseRefChainHops(hops)`（兄弟路径 RPM 等待取消与 400 清洗重试都显式释放并注明"不归还则整组钉死到重启"）。半开恢复候选胜出的请求一旦提交后失败，`ProbeItemID` 永久滞留，此后该分组选路永远返回空——仅删成员 / 删组 / 重启可恢复；同一路径还泄漏紧急兜底并发额度，3 次后紧急兜底失效。该缺陷已在独立测试中动态复现；修复方式是在该 return 前补 `releaseRefChainHops(hops)` 并补回归测试。
- 主循环集中在 `handler.go`（1325 行），选路 / 熔断 / 脱敏 / 协议 / 超时 / panic 兜底交织在一个循环里，是最大的维护热点。
- 请求体峰值内存放大：`raw.Body` 与 RequestState 内的 string 双份常驻，脱敏替换产生第三份，每轮 sjson 整份复制均未计入预算；预算按 2 份计，大 body 高并发下峰值可达预算 1.5 倍。
- 探测 goroutine 无 recover，panic 会击穿整个进程；测试有并发场景但 CI 不跑 `-race`（见 [build-ci.md](build-ci.md)）。

## 深入阅读

- 路由权威文档：[DEVELOPMENT_routing.md](../DEVELOPMENT_routing.md)
- 透传语义：[CHANNEL_PASSTHROUGH.md](../CHANNEL_PASSTHROUGH.md)
- 脱敏：[mask.md](mask.md)
- 决策记录：会话粘合与三态熔断 [2026-09-11-session-affinity-circuit-breaker-routing](../../.agents/notes/implemented/architecture/2026-09-11-session-affinity-circuit-breaker-routing.md)、OpenCode 按模型协议 [2026-09-22-opencode-upstream-protocol](../../.agents/notes/implemented/feature/2026-09-22-opencode-upstream-protocol.md)、OpenCode 会话头 [2026-09-22-opencode-session-and-prompt-cache-key](../../.agents/notes/implemented/feature/2026-09-22-opencode-session-and-prompt-cache-key.md)
