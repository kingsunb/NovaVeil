# Agent Note: 分组级上下文窗口过滤

Status: implemented

## 问题

分组里往往混着窗口差异很大的模型：同一条长对话，发给 1M 窗口的模型正常，发给 32K 的会直接被上游拒绝或截断。本仓虽然已经有 `ChannelModel.ContextWindow`，但它只用于前端展示，路由选成员时完全不看，于是大请求会被反复投给装不下它的成员，白烧轮次和上游额度。

要按窗口过滤，前提是知道「这个请求有多大」。而下游请求**不携带上下文大小**：OpenAI 兼容请求里只有 `max_tokens` / `max_completion_tokens`，那是输出上限，不是输入长度；Anthropic 的 `max_tokens` 同理。所以大小必须由网关自己估。

本仓也没有任何分词能力：全仓没有 tiktoken 或其他 tokenizer 依赖。

## 决定

### 估算方式（`internal/relay/context_fit.go`）

按字符宽度加权，不引分词器：

- 非 ASCII 字符（中文、日文、emoji 等）按 1 token/字
- ASCII 字符按 4 字 1 token
- 遍历用 `utf8.DecodeRune` 走原字节切片，不复制请求体

选这条路的原因：各上游的 tokenizer 不同，网关不可能对每条渠道都装正确编码表，追求精确没有终点。过滤的目标只是「别把 200K 的请求发给 32K 的模型」，粗估足够，误差靠「只在明显超限时才跳过」吸收。

### 判定与跳过（`internal/relay/handler.go`）

分组级开关 `context_fit_enabled`（`GroupRelayConfig.ContextFitEnabled`，默认 false，旧分组 JSON 反序列化为关）。开启后，选出成员、装配好生效渠道之后、发起请求之前判定：

- `ContextWindow <= 0`（未知）一律放行，避免没同步过窗口数据的渠道被全部筛掉
- 估算值超过窗口则把该成员记入 `contextSkipped`，`exclude` 它并立即重选，**不计成员失败、不冷却**
- `contextSkipped` 覆盖到全部可路由叶子时，以专用错误 `errContextWindowTooSmall` 终止并回给客户端

不记失败是刻意的：模型窗口不匹配不是渠道故障，计入失败会把健康的渠道打成冷却状态。

### 手动覆盖窗口（`ChannelModelLimit.ContextWindow`）

上游目录并不覆盖所有模型，OpenCode 部分免费模型拿不到窗口数据，而这些模型恰恰最需要被判定。因此 `ChannelModelLimit` 增加 `context_window`：

- 判定只采信人工值：`effectiveContextWindow(limits, modelName)` 返回 `model_limits` 里大于 0 的窗口；没有人工值即返回 0（放行）。同步目录值只用于展示，不参与过滤。
- 手动值存 `channels.model_limits`，同步只重建 `channel_models`（`ChannelUpdate` 只传 `ID` 与 `Models`），所以手动值不会被同步冲掉。
- 空值或 0 表示「未定义人工窗口」：路由不拦，前端徽章回落到同步元数据展示。

窗口来源：渠道编辑器「按模型配置」手填、探测页每行直接填写/批量定义、探测结果行「应用」。三者写同一字段；探测页入口与密度校准的取舍见 [上下文窗口手工定义与统一实测密度](2026-10-21-context-window-manual-limits-and-uniform-density.md)。

前端模型行的 `xxxK ctx` 徽章显示生效值（人工值优先，否则同步元数据）。

### 前端（`web-next/src/pages/Groups.tsx`）

分组 Relay 配置区在「回显请求模型名」之后新增「上下文窗口过滤」开关，描述里注明窗口需要人工定义（渠道编辑器或探测页），同步目录值不参与过滤。

## 备选方案

- **引入 tiktoken-go 精确计数** — 精度最好；否掉是因为词典体积约 10MB、只对 OpenAI 系准确，Claude/DeepSeek 等仍要用各自的分词器，等于把依赖与维护成本乘以渠道数，换来的精度在「是否明显超限」这个判断上没有意义。
- **只按请求体字节数设阈值** — 实现最简；否掉是因为中文与英文的字节/token 比差异接近 3 倍，固定阈值对两类请求必然有一侧严重失真。
- **全局开关而非分组级** — 配置更少；否掉是因为估算有误差，不同分组的模型构成差异大，全局强制会让某些分组被误跳过，分组级才谈得上「谁需要谁开」。
- **把窗口不足计入成员失败** — 复用既有记账路径、代码更少；否掉是因为会污染渠道健康度，健康的模型仅因窗口小而被打入冷却。

## 后果

- **收益**：长请求不再被投给装不下它的成员，避免注定失败的往返；失败时给出明确的窗口错误，而不是笼统的「无可用渠道」。
- **代价与已知上限**：估算有 ±20% 量级的误差，边界请求仍可能误判（偏严时跳过本可用的成员，偏松时仍会撞上游 400）；只统计请求体，不计系统提示之外的上游侧开销；窗口数据依赖同步或手动覆盖——两者都没有的模型（窗口为 0）永远放行，等于开关对它无效。手动覆盖是逐模型配置，渠道模型多时维护成本不低。
- 重访信号：出现大量「边界请求被误跳过」的反馈，或上游开始返回可用的上下文水位信息（届时可改用上游数据替代估算）。

## 验证

- `grep -n "contextFitsWindow\|estimateModelTokens" internal/relay/handler.go` 只应有上下文过滤与错误文案两处调用。
- `ContextWindow` 为 0 的成员在开关开启时仍被选中（放行分支）。
- 全部成员窗口都不足时，客户端收到「没有候选模型的上下文窗口能容纳当前请求（请求约 X token，候选集最大窗口 Y token）」，带量化提示方便调用方主动截断；而非「暂无可用渠道」。
- 窗口值只信人工值的三个入口: 编辑器手填、探测页直接填写/批量定义、探测「应用」。同步目录带来的窗口是文档值, 未经实测, **不参与过滤**(未验证不拦, 宁可放行也不基于猜测误拒); 它仍在前端展示。`effectiveContextWindow` 无人工值即返回 0(放行)。
- 估算与探测共用一套尺子: `contextFitsWindow` 走 `estimateModelTokens`, 在基准口径(ASCII 4 字符/token, 非 ASCII 每字 1)之上乘以实测密度比(按「上游端点:模型」缓存于 `tokenDensityCache`, 探测实测写入, 进程内有效)。所有渠道同一规则, 不设白名单; 未实测的模型维持基准口径。聚合粒度是端点而非渠道 ID: OpenCode Free/2/3 同为 opencode.ai/inference, 测一个三个全部跟进; Free Old 走 zen 端点, 单独一份。拒绝消息里的「请求约 X token」也用同一估算。
- 分组配置 JSON 里 `context_fit_enabled` 缺省为 false，旧分组行为不变。
