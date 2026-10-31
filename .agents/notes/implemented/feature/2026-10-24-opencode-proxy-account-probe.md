# Agent Note: OpenCode 代理账号批量体检（独立页面 + 并发钉号探测 + 结果自动入池）

Status: implemented

## Problem

免费档渠道按出口 IP 轮换：`channel_proxy` 含 `{account}` 占位符，每个账号对应 resin 的一个出口会话。池的现有学习方式是「流量驱动」——转发时挑号、成功/被拒回填，一次只学一个，且冷启动会撞已 ban 的出口。运营侧需要一次性生成 N 个账号、并发探测，把健康的先填进池子，后续转发直接优先复用。

## Decision

### 钉号探测（`internal/relay/context_probe.go`）

`probeOnce` 抽出 `probeOnceWithAccount(ctx, channel, modelName, tokens, account)`：account 为空沿用池内选号（原行为不变），非空时强制用该账号探测。拿到 HTTP 响应时结论（Status/Detail/Latency）随 result 返回而非丢弃——`probeOnce` 包装层保持旧契约（err 时 result=nil），`ProbeContextWindow` 调用方无感知。成功/403/429 的池回填逻辑原样复用：**体检结果与转发共享同一套池状态**。`channel_proxy` 模板解析失败时探测直接报错（不回显模板原文，避免代理凭据进日志与台账），不带着未替换的 `{account}` 占位符出站。

### 批量体检（`internal/relay/opencode_account_probe.go`）

`ProbeOpencodeAccounts(ctx, channelID, accounts, count, concurrency, timeoutMS)`：渠道须为含 `{account}` 代理的免费档（`proxyNeedsOpencodeAccount` 同口径）；accounts 空时生成随机账号（复用 `opencodeXXXX` 生成器），非空时去重并校验字符集（`^[A-Za-z0-9_-]{1,64}$`，账号要拼进代理 userinfo）；worker 池并发（默认 10、上限 50）各打一次官方客户端形态探测请求（形态见下节）。结论三分类：healthy（2xx）/ rejected（403/429）/ error（其余，含代理与网络故障，不入池不冷却）。单次上限 30000 个、单账号超时上限 120s。

2026-10-25 起探测消息改用问题池轮换（`ocProbeQuestions` 五问，洗车问的距离以 1-1000 随机填充）：全量账号同一文案出站易被上游按消息指纹聚合识别。曾同日加过平滑放行与随机错峰，同日按运营决策回收（体检吞吐优先），前端并发上限与后端对齐为 50/默认 10。

### 体检请求形态与独立会话（2026-10-24 同日迭代）

体检请求从「极小构造消息 + 免费档整形补齐」升级为**官方客户端完整形态原样出站**：用真实客户端各发一条默认测试消息实抓模板——v1 档 opencode 1.19.4（`/zen`）与 v2 档 2.1.4（`/inference`）——embed 为 `internal/relay/opencode_probe_v1.json` / `opencode_probe_v2.json`（系统提示 + 全量工具定义，工具字节与抓包一致）。装配时仅四处动态：模型 ID、展示名（v2 系统提示的 Name 行）、当日日期（格式对齐客户端，日不补零）、用户消息（默认测试消息）；输出上限透传渠道模型配置，缺省 64000。免费档整形层对模板幂等（测试锁定逐字节不变），整形逻辑本身不动。

- **会话独立性**：每次探测铸全新 `ses_` 会话号（含 v1 档 `x-opencode-request` 的 msg_ id，按会话+turn 派生）——账号之间、同账号复测之间互不相同，避免上游按会话聚合识别探测流量。
- **v2 档项目号**：发稳定 40-hex 项目号（`ocProbeProjectID`，sha256 截断），对齐官方客户端「恒发项目目录哈希、从不发 global」的行为；v1 档保持 global（客户端实抓即 global）。
- **展示名**：整批体检取一次官方目录（`helper.FetchOpencodeCatalog` 复用代理别名出口，15s 超时），失败回退模型 ID，不影响体检结论。
- **回退口径**：非免费模型或非 chat 协议回退面板极小请求，避免在付费档燃烧 9k token 的系统提示。
- **平台口径**：v2 模板 shell 工具描述按网关 GOOS 做 Linux/Windows 短语替换，与整形层 `opencodeToolDescription` 同口径。

### API 与页面

`POST /api/v1/channel/proxy-accounts/probe`（channel.go，同步返回）。前端新增「账号体检」页（`/proxy-accounts`）：渠道选择只列启用中且代理含占位符的免费档渠道；数量/并发输入 + 可选自定义账号列表（粘贴即按列表探测，数量输入失效）；结果表逐账号展示结论/HTTP/延迟/响应摘要（密钥已脱敏）。

### 账号台账与自动体检（同日追加）

生成/粘贴的账号**探测前即落账**（`op.UpsertOpencodeGeneratedAccounts`，source=generated），探测结论同步回写（`SaveOpencodeProbeResult`），台账长期保存。`GET /channel/proxy-accounts?filter=` 返回派生视图：可用=未冷却且（最近探测健康或 7 天内有转发成功）；今日未测试=今日既无体检也无成功（原「未测试」口径已被 [今日未测试笔记](2026-10-31-opencode-account-untested-today.md) 取代）；不可用=冷却中或最近探测被拒且无在效成功；网络错误=最近探测遇基础设施故障且无在效成功（不代表账号被封，单独筛选可复测）；**历史可用=累计成功过（success_count > 0，不依赖 last_success_at——存量库有旧版冷却落库伪造的非零最近成功时间，按时间判会把从未成功的账号误入历史可用）**。`DELETE /channel/proxy-accounts` 删除台账账号并同步清理进程内池。自动体检：设置键 `opencode_account_auto_probe`（默认开启，台账为空时空转；不进 DefaultSettings，随缓存初始化惰性补种），后台任务每 24 小时跑一轮，只测距上次体检超过周期的账号（最早优先，单渠道单轮上限与单次体检对齐，30000），健康探测会清除既有冷却（复用成功回填）。

**成功/失败计数按来源拆分**：台账新增 `real_success_count` / `real_failure_count` / `probe_success_count` 三列——下游真实转发在 handler 回填处计成功与失败（客户端取消/管理端中止不算失败，403/429 同时计失败并冷却），体检探测（账号体检与上下文探测）只计体检成功；`success_count` 仍为两路合计，成功天数口径不变。**成功落库同时清除库内 cooldown_until**（与池内回填语义对齐；旧行为只清内存不清库，台账页会在冷却期持续显示「健康但不可用」且重启后回退）。台账列表默认按「真实成功 + 体检成功」降序、同数按账号名升序。转发选号「已验证优先、最近成功优先」由 `TestOpencodeRelayPrefersVerifiedAccountAndCountsRealTraffic` 端到端锁定。

**多对话指纹稳定性（同日取证）**：同一问题在 v1(1.19.4)/v2(2.1.4) 各跑 3 次独立对话——主请求与命名请求正文全部逐字节一致；头部仅会话四件套与 v1 档 msg_ id 随对话变化，`x-opencode-project`/UA/Accept 恒定。据此把 v2 档 `b3`/`traceparent` 的 trace 段改为**进程级稳定值**（官方客户端同一安装内跨对话恒定，实抓 8 小时不变），span 段保持逐请求随机——旧行为逐请求换 trace-id 属可识别的非官方特征。

配套修复两处由「未探测账号落账」暴露的池语义问题：`loadOpencodeProxyPool` 不再整行视为 verified（按 last_success_at 判定），`markOpencodeProxyCooldown` 补做首次加载（原首载竞态会用库内旧行覆盖内存冷却）。

## Alternatives considered

- **只测代理连通性（GET /models 或 TCP 连通）** — 不打真实对话请求就测不出出口被 ban/限流，而账号健康度恰恰取决于此；否掉。
- **后台常驻体检任务（定时自动补池）** — 现阶段运营是手动触发场景，常驻任务引入调度与额度消耗治理，YAGNI；否掉，重访信号是需要无人值守补池时。
- **体检结果写独立表** — 池表（`opencode_proxy_accounts`）已承载 verified/cooldown 语义，独立表会造成双源；否掉，直接复用回填。

## Consequences

- **收益**：冷启动不再逐个试错；N 个账号一次并发体检后健康号即时可被转发优先复用；被拒号立即进入 24h 冷却，转发路径不会撞它们；体检流量与真实客户端流量同形，上游无法按请求形态区别对待。
- **代价与已知上限**：每账号消耗一次真实免费请求（客户端形态正文约 22k 字节、输入约 9k token，上游实报用量进分桶，估算值不进）；同步接口在大批量时页面需等待（前端已按 200 一键分批）；探测与上下文探测共用请求日志面板，大批量体检会产生同量级日志条目。
- **重访信号**：需要定时自动补池；或官方客户端改版导致模板漂移（重抓方式见 Verification 的抓包上游说明）。

## Verification

```sh
go build ./... && go vet ./... && go test ./internal/relay/ -run 'TestOcProbe|TestProbeOpencodeAccounts'
cd web-next && pnpm typecheck && pnpm lint && pnpm test -- ProxyAccounts
```

- `TestProbeOpencodeAccountsClassifiesAndSeedsPool`：假 CONNECT 代理按账号返回 200/403/502，断言三分类与池状态（verified / 冷却 / 失败仅普通候选）。
- `TestProbeOpencodeAccountsGenerateAndValidate`：生成模式（去重、`opencode` 前缀格式）、非法字符拒绝、超上限拒绝、非 `{account}` 渠道拒绝。
- `TestOpencodeAccountLedgerStates`：状态派生（健康/冷却/今日未测试/被拒无在效成功）、筛选与删除。
- `TestOpencodeLedgerHistoricFilterAndSplitCounters`：历史可用筛选口径（曾成功含冷却好号）与三类拆分计数。
- `TestProbeOpencodeAccountsRecordsProbeSuccessCounter`：体检成功只计体检计数。
- `TestOpencodeRelayPrefersVerifiedAccountAndCountsRealTraffic`：下游真实请求先选已验证健康账号，成功计真实计数。
- `TestOpencodeProxy403RetriesAnotherAccount`：附带断言真实失败/成功计数（坏号 1 失败、好号 1 成功）。
- `TestOpencodeAutoProbeDueAccounts`：到期选择（未探测立即到期、周期内跳过、上限截断）。
- `TestOcProbeRawRequestMatchesRealClient`：v1/v2 两档体检正文的键序、系统提示动态段（模型/展示名/当日日期/无残留标记）、全量工具名与顺序、流式参数、输出上限透传与付费回退。
- `TestOcProbeClientBodyShapingIdempotent`：免费档整形对模板幂等（逐字节不变）。
- `TestProbeOpencodeAccountsSendsClientFormAndUniqueSessions`：端到端走完整出站链路（代理隧道内抓取），断言请求行/会话四镜像头/UA/项目号/追踪头与正文客户端形态，且三个账号会话号互不相同。
- 逐字节取证（2026-10-24，本地抓包上游 + 真实客户端 1.19.4/2.1.4）：体检出站正文归一化模型名后与实抓正文 `cmp` 完全一致（v1 32772B / v2 22333B）。
- 前端用例：渠道过滤、默认参数与结果表、生成上限、一键测试（生成数量非法不阻塞）、四态筛选/勾选/自动体检、台账删除、批量删除已选、分页。
