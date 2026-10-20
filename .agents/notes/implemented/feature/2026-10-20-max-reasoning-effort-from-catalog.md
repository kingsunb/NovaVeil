# Agent Note: 从目录提取最大思考等级，thinking_level 支持 auto

Status: implemented

## 问题

管理台给模型配思考等级时要靠猜：`thinking_level` 是一个固定词表（`off/minimal/low/medium/high/xhigh/max`），但每个模型实际支持的档位并不相同——`gpt-5` 只有 `minimal/low/medium/high`，`claude-opus` 系列到 `max`，`kimi-k3` 只有 `max`。管理员不知道该模型认哪些值，填错了上游会拒绝或静默忽略。

OpenCode 公开目录其实声明了这些信息，只是此前没有解析：模型的 `reasoning_options` 数组里，`{"type":"effort","values":[...]}` 就是该模型支持的等级枚举，且按从低到高排列。

## 决定

### 目录解析（`internal/helper/opencode_catalog.go`）

`opencodeCatalogModel` 增加 `reasoning_options`，解析时由 `maxEffortFromOptions` 取 effort 项 `values` 的最后一个值作为该模型的最大等级，`none` 不视为等级故跳过。结果写入 `OpencodeModelInfo.MaxReasoningEffort`。

其余三种形态（`toggle`、`budget_tokens`、空数组）没有等级概念，解析结果为空串。

### 元数据落库（`ChannelModel.MaxReasoningEffort`）

`channel_models` 新增 `max_reasoning_effort` 列（AutoMigrate 自动加列，默认空串）。同步任务写入它，并计入 `metadataDirty` 判断——否则老数据里的空值不会触发一次补写。

### 注入语义（`thinking_level = auto`）

`applyChannelModelLimits` 在解析等级前先展开 `auto`：用该模型名在 `channel.Models` 里找到 `MaxReasoningEffort`，展开后走原有分支。因此：

- `auto` + 目录有数据 → 按最大等级注入（OpenAI 写 `reasoning_effort`、Responses 写 `reasoning.effort`、Anthropic/Gemini 走 `thinkingBudgets` 的等级到预算映射）
- `auto` + 目录无数据（toggle/budget/空）→ 展开为空串，等同于不注入，保持上游默认
- 选具体等级 → 维持原有无条件覆盖语义

`auto` 存的是 `model_limits.thinking_level` 字符串，同步不会改写它（同步只重建 `channel_models`），所以这个选择是持久的手动配置，而实际等级随目录数据更新。

### 前端（`channel-editor.tsx`）

- 思考等级下拉在「未配置」后新增「自动（模型最大：xxx）」，没有目录数据时显示「自动（模型最大，无目录数据）」而不是隐藏，避免用户以为功能不存在
- 模型行新增 `thinking: max` 徽章，直接暴露目录声明的最高等级

## 备选方案

- **同步时自动把 `thinking_level` 写成最大等级** — 用户零操作；否掉是因为这会打破「同步不碰 `model_limits`」的既有约定，管理员手调过的值会在下一次同步被覆盖，而且目录没有数据的模型会被写成空值，反而抹掉用户原本的选择。
- **加一个渠道级「自动使用最大思考等级」开关** — 语义更显式；否掉是因为本质上只是「这个模型该用哪个等级」的一个取值分支，`auto` 作为等级选项就能表达，多一个开关意味着每个模型都要先判断开关再判断等级，两套优先级叠在一起更难解释。
- **把完整等级枚举都存下来供前端展示区间** — 信息最全；否掉是当前只需要「最大」，存整个数组要么加 JSON 列要么加子表，为一个展示用了不上的区间付结构成本（YAGNI）。

## 后果

- **收益**：选思考等级时能看到模型真实支持的上限，不必试错；`auto` 让「跟随模型能力」成为可持久化的选择，目录调大档位后无需再改配置。
- **代价与已知上限**：只取最大等级，不表达「该模型也支持 low/medium」这类区间信息；`budget_tokens` 型模型（如部分 Claude、qwen）没有等级可提升，`auto` 对它们等于不注入；目录数据的准确性由上游决定，解析层不做二次校验。
- 重访信号：上游给 `budget_tokens` 型模型也补上等级枚举，或出现「auto 展开后的等级不被该渠道接受」的反馈（说明目录声明与实际上游能力不一致）。

## 验证

- `curl -sL https://models.opencode.ai/api.json | grep -o '"reasoning_options":\[[^]]*\]' | head` 应能看到 `effort` 与 `values` 组合。
- `grep -n "MaxReasoningEffort" internal/` 应覆盖 helper / model / op / task / relay 五处。
- 选 `auto` 且模型目录有 `values: ["low","medium","high"]` 时，出站请求体带 `reasoning_effort: "high"`；目录为空时请求体不带该字段。
