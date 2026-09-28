# Agent Note: 模型评估支持手动加入排序、跳过实际评估

Status: implemented

## 问题

「评估排序」是 `auto` 分组的数据源：请求成功的评估（`ok` / `violation`）自动写入排序，管理员调整顺序后「更新 auto 分组」把该顺序固化为路由优先级。但要让某个模型进入排序，必须先对它发一次真实评估请求——这会消耗上游额度、占用时间，且上游刚创建/凭据未验证时评估会失败（`error` 不入排序），导致管理员无法先排好优先级、后补验证。需要一个「跳过评估、直接把渠道 + 模型放进排序」的入口。

## 决定

新增 `manual` 评估终态与批量手动加入接口：

- **新 outcome**：`internal/model/model_eval.go` 增加 `ModelEvalManual = "manual"`，语义是「手动加入排序、未运行实际评估、无回复内容」。它与 `ok` / `violation` 一样进入 `rankableOutcomes`（`internal/op/model_eval_rank.go`），因此出现在排序列表、可上移/下移、可应用于 `auto` 分组；`error` 依旧不入排序。`manual` 不写评估历史、不累计 `model_eval_stats`、没有 `content`，`source_eval_id` 恒为 0。
- **接口**：`POST /api/v1/model-eval/rank/manual-add`，请求体 `{ "channel_model_ids": number[] }`，返回 `{ "items": ModelEvalRankSummary[] }`。`op.ModelEvalRankManualAdd` 逐个解析 channel_model_id 为渠道+模型快照（`ChannelModelGet` + `ChannelGetCore`），校验渠道启用且模型存在，失败返回 `ErrEvalRankModelUnavailable`（映射 400）。
- **幂等与去重**：以 `(channel_id, model_name)` 为去重键。已存在排序条目时跳过、覆盖既有快照（不抹掉已评估的 `content`）；未存在的以 `outcome=manual`、空 `content` 追加到可入组区末尾。写入与 `nextRankablePosition` 分配都在 `modelEvalRankMu` 锁内完成，与 `ModelEvalRankUpsert` 共用同一把锁。
- **可升级**：已存在的 `manual` 条目后续成功评估时，`ModelEvalRankUpsert` 因 `isRankableOutcome(manual) == true` 而保留原 `position`，仅把 `outcome` 升级为 `ok` / `violation` 并回填 `content`，不产生重复行。
- **前端**：`EvalOutcome` 增加 `"manual"`，徽标「手动加入」（`info` 色）；排序列表用 `isRankableEvalOutcome` 统一过滤，`manual` 条目统计显示「未运行评估」而非 0 ms / 0 输出 tok。入口放在「当前评估」视图选择区：在原「开始评估」按钮下新增「直接加入排序（不评估）」次按钮，复用现有多选与筛选，点击后批量调用 `/rank/manual-add`、写入 `["model-eval","rank","list"]` 查询缓存并切到「评估排序」视图。

## 备选方案

- **复用 `ok` 作为手动条目的 outcome** — 最强论据是不新增枚举、改动面最小且排序列表无需改过滤；否掉因为语义错误：`ok` 表示「评估成功且格式合规」，手动条目没有评估历史，徽标「格式合规」与「未评估」事实冲突，且会把未验证模型混入「成功模型」计数，误导路由决策。
- **从评估历史「加入排序」扩展为不校验历史即可加** — 现有 `/rank/from-history` 必须引用一条真实 `eval_id`（且前端按钮只对 `ok` 可点），改造它需要伪造历史记录，绕不开「先评估」；手动加入的目标恰恰是「不产生历史」，故新开独立接口与独立 outcome 更直白。
- **只在排序视图内嵌一个模型选择器** — 最强论据是入口与排序结果同屏、心理模型更紧凑；否掉因为「当前评估」视图已有完整的渠道→模型展开/筛选/多选与「开始评估」按钮，复用它能零新增选择器组件、两处按钮语义并列（评估 vs 直接排序），改动更小且入口更可发现。

## 后果

- **收益**：管理员可以先把渠道/模型排进 `auto` 分组再按需补评估，新增未验证模型无需消耗上游额度即可进入路由优先级编排；手动条目与已评估条目在同一份排序里统一排序、统一应用到分组。
- **代价与已知上限**：`manual` 条目没有任何回复内容，排序内的 SVG 预览显示「没有可预览的 HTML」，等真实评估成功后可自动回填。路由会把未验证模型直接选路，出错由既有熔断/failover 兜底——这是「先排序、后验证」的刻意取舍。**重访信号**：若后续需要「手动条目必须至少一次成功评估才能写入 `auto` 分组」，应加一道 apply 时的门槛，而非禁止手动加入本身。

## 验证

后端 `internal/op/model_eval_rank_test.go` 的 `TestModelEvalRankManualAdd` 覆盖 manual 写入/空内容/幂等/停用渠道拒绝；`internal/server/handlers/model_eval_rank_test.go` 的 `TestManualAddModelEvalRank` 覆盖 `/rank/manual-add` 200 响应与 manual outcome 序列化。前端 `isRankableEvalOutcome` 定义于 `web-next/src/lib/model-eval.ts`，`EvalOutcomeBadge` 补全 `manual` 徽标，`EvalRanking` / `EvalSelection` 统一引用。校验：`go test ./internal/op/ ./internal/server/handlers/`、`cd web-next && pnpm typecheck && pnpm lint && pnpm test`。