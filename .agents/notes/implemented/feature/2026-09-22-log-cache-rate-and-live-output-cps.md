# Agent Note: 日志实时观测增强——缓存命中率百分比与流式实时输出速度

Status: implemented

## 问题

日志页的观测能力有两处「有数据但读不出决策」的断点：

1. **缓存命中只报计数不报占比**。令牌列/详情汇总只显示 `缓存 N`，但同样的 N 在 100 token 与 100k token 的输入里含义完全不同——看不到命中率就无法一眼判断 prompt cache 的收益比例，调 prompt 结构时缺反馈。
2. **流式进行中没有任何速度反馈**。详情「Token 速度」恒为 `—`：token/s 需要终态才有的完整 `usage` 与定稿耗时，而进行中的请求（committed）既无终态用量也无定稿耗时，只能等到结束才显示。同时运行中卡片/详情的耗时每 1000ms 才刷一次，秒数跳变明显，实时感弱。

不做会怎样：管理员在长耗时流式请求期间只能干等「响应中」+ 冻结的秒数，无法判断当前吐字速率是否异常（卡死/空转 vs 正常输出）。

## 决定

三点一起落地，均属日志页「实时观测」面：

1. **缓存命中率百分比**。`web-next/src/pages/Logs.tsx` 新增 `cacheRateOf`：命中率 = `prompt_tokens_details.cached_tokens / prompt_tokens × 100`，经 `CacheSuffix`（`缓存 N (xx%)`，四舍五入到 0.1、去无意义尾 0）在令牌列、详情底部汇总、详情 Tokens 行三处统一展示；无缓存命中或 `prompt_tokens = 0` 时不展示百分比。
2. **刷新间隔 1000ms → 500ms**。`useElapsedTick`（日志卡片耗时单元格与详情 `now` 共用的定时器）改 500ms，与后端输出字符节流发布对齐；`useGroupRuntime` 的模块级 1s 时钟只服务渠道冷却倒数，不动。
3. **流式实时输出速度 c/s，终态切回 t/s**。
   - 后端 `RequestState` 新增 `OutputChars`（累计已转发输出字符数），流式转发循环每写完一个事件就 `noteOutputChars(utf8.RuneCount(event.Data))` 累加；`noteOutputChars` 持全局锁做 cheap 自增，并按 `outputCharsPublishInterval = 500ms` 节流调用 `publishRequestLocked`——把原先只在 `startRound/finishRound/markCommitted/终态` 这些离散生命周期点发布的状态流，扩展为流式进行中至多 2Hz 的增量发布。
   - `MarshalJSON` 新增 `output_chars`（`omitempty`，为 0 不出现）。前端 `types.ts` 加 `output_chars?`，`DetailTab` 在非终态且已首字时按 `output_chars / (now - first_token_at)` 实时折算，显示「输出 X c/s」；终态沿用原「总 X tok/s · 输出 Y tok/s」，两者互斥。

   **字符数的口径**：`OutputChars` 按还原后 SSE payload 的 UTF-8 字符数累计，含结构化帧的 JSON 开销，是「已转发输出量」的近似代理，不是精确的正文字符数。它不是计费数据，只服务实时速率观感。

## 备选方案

- **按字节数 `len(event.Data)` 计数** — 最强论据是零 CPU 开销、且就是真实网络吞吐量；否掉因为「c/s」字面是字符/秒，中文等多字节字符按字节计会把速率系统性放大约 3 倍，`utf8.RuneCount` 更贴近展示语义，开销可忽略。
- **沿用 octopus 的 `addOutput(2)` 启发式（每事件近似 +2 字符）** — 最强论据是实现最简、无字符串扫描；否掉因为它把事件粒度误当成内容长度，长短块严重失真，NovaVeil 直接计量真实 payload 长度更能反映实际输出速度。
- **只在前端累加已渲染字符数** — 最强论据是不动后端契约；否掉因为 SSE 断连/重连期间会漏计数，多客户端各自口径不一；服务端 `OutputChars` 是唯一权威累计源，重连拿全量快照也能读到同一数字。
- **不做节流、每块都发布** — 最强论据是发布延迟最低；否掉因为每次发布都要克隆整个 `RequestState` 并序列化推给所有状态流订阅者，高吞吐流式的块频（可达每秒上百块）会放大成等量的全量快照推送，却不产生任何观测增量（前端 500ms 才读一次 `now`），2Hz 已覆盖「实时」观感。

## 后果

- **收益**：缓存命中率可一眼读取，prompt 结构调优有了直接反馈；流式进行中能看到实时输出速率，空转/卡死与正常输出从速率上一目了然；运行中卡片与详情耗时刷新翻倍，秒数与速率跳变更平滑。
- **代价与已知上限**：`output_chars` 进入 SSE 状态快照与 `RequestState` 内存（累加自增、按请求生命周期回收），体积为单个 int64 可忽略；流式进行中状态流发布频率从「每次生命周期点」提升到「至多 2Hz」，每次多一次全量快照序列化与推送，已由 500ms 节流封顶。`c/s` 是含 JSON 帧开销的近似吞吐量，不是精确正文字符数，也不能当作计费或审计口径。**重访信号**：若后续需要精确的「输出正文 token/字符速率」，应在上游 usage 流（OpenAI `stream_options.include_usage` 之类）或 response 聚合处挂精确计量，而不是继续放大本代理口径的精度。
- **取代关系**：本篇不取代任何现有笔记，属新增的日志观测面能力。

## 验证

- `internal/relay/state_json_test.go`：`TestRequestStateJSONIncludesOutputChars` 验证 `output_chars` 序列化与为 0 时省略；`TestNoteOutputCharsAccumulates` 验证逐块累加并忽略非正数。
- `web-next/src/pages/Logs.test.tsx`：缓存命中率「缓存 30 (29.7%)」、无缓存不展示百分比、流式进行中展示 `c/s` 且无 `tok/s`、终态展示 `tok/s` 且无 `c/s`。
- 本地门禁：`go build ./...`、`go test ./...`、`go vet ./...`、`cd web-next && pnpm typecheck && pnpm lint && pnpm test` 全绿。