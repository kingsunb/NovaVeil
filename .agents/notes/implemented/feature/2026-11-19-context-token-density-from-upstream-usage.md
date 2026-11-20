# Agent Note: 上下文 token 密度改由真实流量校准、落库并删除探测页

Status: implemented

> 缓存写入顺序与失效契约见 [会话与密度缓存生命周期及 sidecar 诊断边界](../bug-fix/2026-11-20-session-density-cache-lifecycle-and-sidecar-diagnostics.md)；本文的真实流量采样与估算规则继续有效。

## 问题

手动上下文窗口填的是上游真实 token 容量，但本地估算长期是另一把尺子：

- 上下文探测页按「4 字符/token」构造填充文本，页面上的 `100K` 是构造标称，不是上游实际 token。上游对同一请求回 `usage.prompt_tokens = 87K` 时，探测才把这个偏差记成密度。
- 探测密度只在有人主动点探测后写入，重启即丢；真实请求从不校准，路由过滤与探测结论可能长期各算各的。
- `sanitizeUsage` 在上游不返回 usage 时按 `len(requestBody)/3` 估算，又引入第三种口径；日志里的「估算」与路由估算对不上。
- 探测页本身要发真实大请求、消耗额度，合成英文文本与中文/代码请求的 token 分布也不同；用户已明确反馈该功能没有实际价值。

需要统一为一句话：**上游 `usage.prompt_tokens` 是唯一权威，本地只学它与请求体基准估算的比例。**

## 决定

### 一套估算口径

基准估算保持不变：ASCII 每 4 字符 1 token，非 ASCII 每字符 1 token。它只提供请求体规模的相对值。

密度比定义为「上游 `usage.prompt_tokens` ÷ 基准估算」，键为「渠道 ID:模型」。所有渠道同一规则，不设白名单。

- 冷启动默认密度 `defaultTokenDensity = 4/7.1`，即英文词文本约 7.1 字符/token。
- 每个「渠道:模型」的前 10 条成功请求参与密度确定：首条直接初始化，后续按指数滑动平均更新，权重 `0.2`，夹取 `0.25..16`；一次真实请求即可对齐上游。
- 满 10 条后每天只验证前 3 条成功请求；观测值与当前密度相差 `3%` 以内保持不变，超过 `3%` 直接更新。
- 基准估算不足 `2000` token 的请求不参与校准，避免模板固定开销放大倍率。
- 首次观测每个「渠道:模型」打一条 Info 日志，后续降为 Debug；日志同时给出 observed、EWMA、prompt 与 base，便于本地核对。
- 密度写入 `token_densities` 表（渠道 ID + 模型唯一），由 `internal/op/token_density.go` 统一管理缓存与落库；重启后从数据库恢复。写入顺序与生命周期契约见 [会话与密度缓存生命周期及 sidecar 诊断边界](../bug-fix/2026-11-20-session-density-cache-lifecycle-and-sidecar-diagnostics.md)。
- 设置页「上下文密度」可查看已学习记录并逐条清除或全部清除；清除同时失效进程缓存，下一次成功请求重新初始化。
- 渠道 Base URL 变更即清空该渠道全部密度，避免旧上游口径沿用到新端点；删除渠道或模型同样级联清理。

### 校准来源与边界

只采信真实转发成功、且上游返回了非零 `usage.prompt_tokens` 的请求。账号体检请求、测试请求、上游失败请求都不参与，避免把探测模板或错误回包学进路由。

`usage.prompt_tokens` 已包含缓存命中部分：Anthropic 口径在转换层把 `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` 合成 prompt，OpenAI 口径保留 `prompt_tokens`，因此不需要再单独加缓存 token。

### 同一密度服务三个出口

- 选路过滤：`contextFitsWindow` 与 `scanContextFit` 按「渠道:模型」的当前密度估算。
- 拒绝消息：全部候选手动窗口都装不下时，消息里的「请求约 X token」取最大窗口候选按当前密度算出的值，不再固定用默认口径。
- 日志兜底：`sanitizeUsage` 在上游不返回 usage 时，用同一密度估算 prompt token，替代原来的 `len/3`。

手动 `channels.model_limits[model].context_window` 仍是唯一参与路由的窗口；同步目录的 `channel_models.context_window` 继续只展示，不参与过滤。

### 删除上下文探测

删除探测页、`/probe-context`、`/probe-history`、`/apply-context`、历史表 `context_probe_records` 及其前端路由/API/类型/文案。新增 Version 31 迁移删除该表。

账号体检复用的 `probeOnceWithAccount`、用量解析与上下文超限判定保留，迁到 `internal/relay/test_probe.go`；探测专用的构造器、重测与密度回写全部删除。

## 备选方案

- **保留探测页，只修展示** — 改动最小；否掉是探测仍要花额度、仍用合成英文文本，且只能得到单次样本，真实请求依旧不校准，问题会在重启后复发。
- **引入本地 tokenizer（tiktoken 等）** — 可离线精确计数；否掉是每个上游分词器不同，OpenAI 系之外仍需各自实现，依赖与维护成本高，对「是否明显超限」的收益有限。
- **固定 4 字符/token** — 最简单、跨渠道完全一致；否掉是实测英文约 7.1 字符/token，固定 4 会让 200K 窗口按错误口径放行或拒绝，正是本次要修的偏差。
- **默认密度取 1（基准口径）** — 改动最小；否掉是英文实际密度约 0.56，默认 1 会让 200K 窗口只放行约 112K 实际 token，冷启动严重偏严。
- **只存进程内** — 实现最轻；否掉是重启后回到默认密度，用户实测的 0.87 不能跨重启复用，长尾模型每次重启都要重新学习。
- **把转换后的真实上游请求体拿来做分母** — 分子分母完全同源；否掉是转换后的 body 只在请求发送路径内部可见，为校准把它透出到 handler 需要改跨文件契约，当前用下游请求体已能稳定学习比例。
- **同步目录窗口直接参与路由** — 零配置；否掉是目录值未经本地验证，会误拒健康成员，产品口径仍是只信手动窗口。
- **继续用 `len/3` 兜底日志** — 无需改测试；否掉是它既不是上游口径，也不是路由口径，会继续制造 100K/87K 这类无法解释的差异。

## 后果

- **收益**：路由、拒绝消息和日志估算共用一条由上游 usage 驱动的尺子；不再主动发探测大请求，省额度；真实流量越多，密度越贴近该渠道/模型的实际分词与模板开销；落库后重启直接复用，用户实测的密度不会丢。
- **代价与已知上限**：前 10 条成功请求用于确定密度，之后每天最多 3 条验证，模型工况突变时响应会慢一个验证周期；若某模型在冷启动就被本地误拦或持续被上游 400 拒绝，校准会变慢；分母是下游请求体，不含上游额外注入的 system/tool 文本，观测密度会把固定开销摊进比例，边界估算仍有偏差；删除渠道或模型会级联删除对应密度，设置页也可手动清除。
- **重访信号**：出现大量「日志估算与上游 usage 偏差明显」或「同一模型密度长期学不动」时，再考虑单独拆分固定开销与正文密度的两段模型。

## 验证

- `go test ./internal/relay/... -run 'TestEstimateRequestTokens|TestEstimateModelTokens|TestContextFitsWindow|TestContextDensity|TestSanitizeUsage' -count=1`
- `go test ./internal/op/... -run 'TestTokenDensity' -count=1`
- `cd web-next && pnpm typecheck`
- 以 `log.level = info` 启动后发送一条基准估算超过 2000 token 的真实请求，日志应出现一次 `token density <渠道 ID>:<模型> observed=... ewma=... prompt=... base=... samples=... daily=...`；用 `usage.prompt_tokens / base` 可复核 observed。重启后设置页「上下文密度」应仍显示该记录。

[历史快照：上下文容量探测页（按档位探测并回填）](../../archived/feature/2026-10-20-context-window-probe-page.md)

[历史快照：上下文探测窗口草稿与分组窗口展示](../../archived/bug-fix/2026-10-21-context-probe-draft-and-window-display.md)

[历史快照：上下文窗口清理按模型名大小写不敏感](../../archived/bug-fix/2026-10-21-context-window-case-insensitive-clear.md)
