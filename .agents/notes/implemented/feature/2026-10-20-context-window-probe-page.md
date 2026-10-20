# Agent Note: 上下文容量探测页（二分探测窗口并回填）

Status: implemented

## 问题

模型真实能接受多长的请求，本仓只有两个来源：OpenCode 目录同步（只覆盖该渠道的模型）和人工填写。两者都不可靠——目录里没有的模型只能猜，猜大了请求发过去撞上游 400，猜小了白白跳过可用成员。

模型评估页解决的是「模型答得好不好」，与「模型能装多长」是两回事：前者需要 prompt/content/outcome 一套字段，后者只需要「这个规模能不能过」。把探测塞进评估队列会污染那张表的语义。

## 决定

### 单点探测接口（`internal/relay/context_probe.go`）

`ProbeContextWindow(ctx, channelID, model, tokens)` 按指定规模构造一条请求并发出去，只回答「接受还是拒绝」：

- 填充文本用随机常见英文单词（非随机字母数字串），按「4 字符约 1 token」生成。随机字母数字的 BPE 密度约 2.5-3 字符/token，用它生成会让实际 token 数比目标大 40% 左右，判定偏保守；英文单词序列的密度接近自然文本，与口径一致。
- **估算不等于精确**：各家 tokenizer 密度不同，本地只能给近似值。请求被接受时解析响应 `usage.prompt_tokens`（Anthropic 为 `usage.input_tokens`）拿到真实 token 数，随结果返回 `actual_tokens`，界面显示「实 26.8万」。免费档被强制流式时前若干字节里没有末尾的 usage 事件，该值为 0。
- 出站构造复用 `newTestRequest` + `testRequestTarget`，并在命中 OpenCode 免费档时套用 `applyOpencodeFreeLaneHeaders` 与 `shapeOpencodeFreeBody`，与真实转发同形。不套这层的话免费档会被指纹校验 403，探测会把 403 误读成「容量不足」，二分直接判死。
- 判定以状态码为骨架，但不只看状态码：2xx 接受；400 / 413 / 422 还要看响应体是否指向上下文超限（`contextOverflowHints` 关键词匹配，含中英文措辞），指向才判「拒绝」，否则按「失败」带原因返回——参数错误、模型不认 `max_tokens` 之类的问题不能被误当成容量上限；401 / 403 / 429 / 5xx 与其余 4xx 一律按失败返回并附状态与响应片段。

`max_tokens` 固定为 1，只测输入容量，不把输出预算算进去。单次规模封顶 1M（`contextProbeMaxTokens`）。

探测同样接出口 IP 池：命中 `proxyNeedsOpencodeAccount` 时用 `pickOpencodeProxyAccount` 选号覆盖 `effective.ChannelProxy`，结束后按结果回填（2xx 记成功、403 记冷却）。不接池的话探测会用确定性别名，永远撞同一个被标记的 IP，表现为「第一次就 403 且不换号」。

### 接口与回填

- `POST /api/v1/model-eval/probe-context`：`{channel_id, model, tokens}` → `{tokens, accepted, status, detail}`。
- `POST /api/v1/model-eval/apply-context`：把探测结果写进 `channels.model_limits[model].context_window`。这一步写的是手动覆盖字段，同步不会覆盖它，而 `effectiveContextWindow` 又优先取它——探测结果因此直接进入路由判定与徽章展示。

### 前端（`/context-probe`）

勾选式组合探测，不做自动二分：

- 顶部一个搜索框同时过滤渠道与模型名，窄列表里定位目标更快
- 选定渠道后列出该渠道模型，复选框多选，支持「全选当前列表」（按关键字过滤后的集合）
- 目标规模八档可多选：64K / 128K / 256K / 400K / 500K / 888K / 980K / 1M
- 点探测后按「模型 × 规模」的笛卡尔积串行执行，避免并发压同一上游
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
- **用上游返回的 usage 反推真实 token** — 最准；否掉是探测的判定发生在请求之前（要么发要么不发），usage 只有被接受时才有，无法用于「拒绝」的那一半二分。
- **探测后自动回填** — 少一次点击；否掉是探测精度受填充文本与真实对话分布差异影响，自动写入会把误差固化成判定依据。

## 后果

- **收益**：目录没有数据的模型也能得到一个可用的窗口值，且这个值与路由判定同口径；探测失败能与「容量不足」区分开，不会把认证问题误记成模型太小。
- **代价与已知上限**：探测消耗真实额度，1M 级二分对免费渠道是额度压力、对付费渠道是真金白银，页面没有内置成本拦截；填充文本是纯英文词，与实际对话的 token 分布（尤其中文与代码）存在偏差，测得值偏保守或偏乐观约一成；只测输入容量，不覆盖输出预留；对强制流式的免费档，读到状态码后即断开，上游侧可能留下未读完的响应。
- 重访信号：出现「探测结果与真实可用长度偏差明显」的反馈，或上游开始提供官方的模型容量查询接口。

## 验证

- `POST /model-eval/probe-context` 分别传 131072 与一个明显超出模型能力的规模，得到 `accepted: true` 与 `accepted: false`。
- 传错误模型名或已停用渠道时返回错误（HTTP 400），而不是 `accepted: false`。
- 应用后 `channels.model_limits[model].context_window` 有值，且渠道编辑器的 `ctx` 徽章显示该值。
- `grep -n "contextProbeMaxTokens" internal/relay/` 上限与前端 `PROBE_SIZES` 的最大档一致。
