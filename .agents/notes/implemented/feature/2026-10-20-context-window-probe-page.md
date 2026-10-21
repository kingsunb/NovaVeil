# Agent Note: 上下文容量探测页（按档位探测并回填）

Status: implemented

## 问题

模型真实能接受多长的请求，本仓只有两个来源：OpenCode 目录同步（只覆盖该渠道的模型）和人工填写。两者都不可靠——目录里没有的模型只能猜，猜大了请求发过去撞上游 400，猜小了白白跳过可用成员。

模型评估页解决的是「模型答得好不好」，与「模型能装多长」是两回事：前者需要 prompt/content/outcome 一套字段，后者只需要「这个规模能不能过」。把探测塞进评估队列会污染那张表的语义。

## 决定

### 单点探测接口（`internal/relay/context_probe.go`）

`ProbeContextWindow(ctx, channelID, model, tokens)` 按指定标称规模构造一条请求并发出去，只回答「接受还是拒绝」。窗口定义入口与密度校准在 2026-10-21 更新，决定与取舍见 [上下文窗口手工定义与统一实测密度](2026-10-21-context-window-manual-limits-and-uniform-density.md)。

- 填充文本用随机常见英文单词, 基准口径「4 字符约 1 token」; 所有渠道同一规则, 不按上游类型设白名单。首测按「标称-冗余」构造(1M 档 980,000, 其余档位留 1,000), 已有实测密度时先换算构造规模。
- **实测密度按「上游端点:模型」缓存, 路由与探测共用**: 实测值取响应 usage, 被拒时取拒绝体 `requested (about) N tokens` 或 Anthropic `prompt is too long: N tokens`(大小写不敏感、在完整响应体上解析, 截断只用于展示); 拿到实测值即回写缓存, 与标称偏差超过 10%(1M 档 100K)时再按本次实测密度把目标改到容忍下限(1M 档 900K)自动重测一次并覆盖缓存; 拿不到实测值时不写缓存也不重测, 重测失败保留首测结论。
- usage 回显与实测解析共用同一套逻辑(非流式顶层 usage / 流式扫 SSE data 事件 / 都没有时 sanitizeUsage 兜底)。
- 出站构造复用 `newTestRequest` + `testRequestTarget`，并在命中 OpenCode 免费档时套用 `applyOpencodeFreeLaneHeaders` 与 `shapeOpencodeFreeBody`，与真实转发同形。不套这层的话免费档会被指纹校验 403，探测会把 403 误读成「容量不足」，直接判死。
- 判定以状态码为骨架，但不只看状态码：2xx 接受；400 / 413 / 422 还要看响应体是否指向上下文超限（`contextOverflowHints` 关键词匹配，含中英文措辞），指向才判「拒绝」，否则按「失败」带原因返回——参数错误、模型不认 `max_tokens` 之类的问题不能被误当成容量上限；401 / 403 / 429 / 5xx 与其余 4xx 一律按失败返回并附状态与响应片段。

`max_tokens` 固定为 1，只测输入容量，不把输出预算算进去。出站构造完成后还会再收敛一次输出预算（强制 `max_tokens` / `max_output_tokens` = 1，并去掉 `reasoning_effort` / `reasoning` / `thinking`）：渠道 `model_limits` 的 `max_output` 与 `thinking_level` 会在构造链末尾覆盖请求体，不收敛的话探测会变成大额输出，既烧额度也干扰容量判定。单次标称规模封顶 1,000,000（`contextProbeMaxTokens`，十进制 1M）。

探测同样接出口 IP 池：命中 `proxyNeedsOpencodeAccount` 时用 `pickOpencodeProxyAccount` 选号覆盖 `effective.ChannelProxy`，结束后按结果回填（2xx 记成功、403 记冷却）。不接池的话探测会用确定性别名，永远撞同一个被标记的 IP，表现为「第一次就 403 且不换号」。

### 接口与回填

- `POST /api/v1/model-eval/probe-context`：`{channel_id, model, tokens}` → `{tokens, accepted, status, detail, actual_tokens}`。
- `POST /api/v1/model-eval/apply-context`：把探测结果或手工输入写进 `channels.model_limits[model].context_window`；`context_window` 传 0 清除该模型的人工窗口。写的是手动覆盖字段，同步不会覆盖它，而 `effectiveContextWindow` 只采信它。保存是「读整表 → 改一个键 → 整表写回」，用进程内互斥串行，避免两个页面并发保存不同模型时互相覆盖。
- 探测失败原因与成功说明在落库、回显前先脱敏渠道凭据（Key、代理账号、Cookie 等），与模型评估共用 `redactChannelCredentials`。
- 探测页每行可直接填写窗口并保存/清除，勾选多个模型后可批量定义或批量清除；与编辑器手填、探测「应用」写同一字段。

### 前端（`/context-probe`）

勾选式组合探测，不做自动二分：

- 顶部一个搜索框同时过滤渠道与模型名，窄列表里定位目标更快
- 选定渠道后列出该渠道模型，复选框多选，支持「全选当前列表」（按关键字过滤后的集合）
- 目标规模五档可多选：64K / 128K / 256K / 500K / 1M（十进制 64,000 / 128,000 / 256,000 / 500,000 / 1,000,000）
- 每行同时提供窗口输入与保存/清除，可直接按「渠道 × 模型」定义人工窗口；勾选多个模型可批量定义/批量清除，批量提交逐项捕获失败并回显原因，结束后统一复位提交中状态
- 点探测后按「模型 × 规模」的笛卡尔积全局三路并发，但同一「上游端点:模型」同一时刻只允许一个探测在途，同模型按档位升序串行，避免并发压同一上游
- 结果逐行展示：模型、规模、接受 / 拒绝 / 失败、耗时、说明；说明列对拒绝显示 `HTTP 状态 · 响应片段`，对失败以红字显示完整原因，超长时悬停看全文；只有「接受」的行可「应用」写回窗口
- 底部「探测历史」表按时间倒序展示最近 100 条（渠道、模型、规模、结果、耗时）

不做二分是刻意的：二分会在一次点击里打出几十次大请求，成本与耗时都不可控，而用户通常已经知道自己的目标档位。要更细的边界，多勾几档即可。

### 历史与日志

- 每次探测（含失败）写一条 `context_probe_records`，字段是渠道名与模型名的快照，渠道删除后历史仍可读。
- 探测走 `startTestRequest` / `finishTestRequest` 记账，因此也会出现在请求日志面板，可与真实请求对照排查；客户端 IP 用 `testProbeClientIP` 标识。
- 记账会带上 usage，日志里不会出现 0/0：先取非流式响应的顶层 `usage`，再扫 SSE 的 `data:` 事件取最后一个带 usage 的（免费档被强制流式时走这条），两者都拿不到时用 `sanitizeUsage` 按请求体长度估算，与转发路径同口径。缓存字段按 OpenAI 的 `cached_tokens` 自动带出，Anthropic 的 `cache_read_input_tokens` 单独映射。
- 页面按渠道过滤历史（`GET /model-eval/probe-history?channel_id=`），探测结束自动刷新。

## 备选方案

- **自动二分逼近真实边界** — 一次点击即可得到精确值；否掉是因为单次操作会打出十几次大请求，成本与耗时不可控，且用户真正需要的是「我这档够不够」的判断，指定档位单点探测更贴合，要更细的边界多测几档即可。
- **复用模型评估队列** — 不必新建页面；否掉是因为评估任务的数据模型是 prompt/content/outcome，探测没有这三样，塞进去要么加一堆可空字段，要么共用表但语义完全不同。
- **用上游返回的 usage 直接判定容量** — 最准；否掉是容量判定只能看状态码，usage 只有被接受时才有，拒绝的一半拿不到。usage 现在只用于校准密度，不用于容量判定。
- **探测后自动回填** — 少一次点击；否掉是探测精度受填充文本与真实对话分布差异影响，自动写入会把误差固化成判定依据。

## 后果

- **收益**：目录没有数据的模型也能得到一个可用的窗口值，且这个值与路由判定同口径；探测失败能与「容量不足」区分开，不会把认证问题误记成模型太小；探测详情的渠道凭据在落库与回显前已脱敏，批量保存不会互相覆盖。
- **代价与已知上限**：探测消耗真实额度，1M 级探测对免费渠道是额度压力、对付费渠道是真金白银，页面没有内置成本拦截；实测密度是进程内缓存，重启后首次探测重新校准；填充文本是纯英文词，与实际对话的 token 分布（尤其中文与代码）存在偏差，测得值偏保守或偏乐观约一成；只测输入容量，不覆盖输出预留；对强制流式的免费档，读到状态码后即断开，上游侧可能留下未读完的响应。
- 重访信号：出现「探测结果与真实可用长度偏差明显」的反馈，或上游开始提供官方的模型容量查询接口。

## 验证

- `POST /model-eval/probe-context` 分别传 128000 与一个明显超出模型能力的规模，得到 `accepted: true` 与 `accepted: false`。
- 传错误模型名或已停用渠道时返回错误（HTTP 400），而不是 `accepted: false`。
- 应用后 `channels.model_limits[model].context_window` 有值，且渠道编辑器的 `ctx` 徽章显示该值。
- `grep -n "contextProbeMaxTokens" internal/relay/` 上限与前端 `PROBE_SIZES` 的最大档一致。
- `go test ./internal/relay/ -run 'ContextProbe|ProbeContext|TestProbeContextWindowDensityAndRetry|TestClampProbeOutputBudget|TestParseRequestedOverflowTokens'`
- `go test ./internal/server/handlers/ -run 'TestRedactChannelCredentials|TestApplyModelContextConcurrentSaves'`
- `cd web-next && pnpm test src/pages/ContextProbe.test.tsx`
