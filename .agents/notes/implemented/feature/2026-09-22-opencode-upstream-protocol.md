# Agent Note: OpenCode 模型行记录上游原生协议

Status: implemented

## 问题

OpenCode 的 Zen 与 Go 是两条内置渠道，渠道类型都锁成 `openai`。`buildOutbound` 只看渠道类型，所以客户端走 Responses 或 Messages 时会被改写成 Chat Completions。公开目录里同一个模型在两个档上可以不是同一种协议，渠道类型表达不了这件事，跨档故障转移会用错转换器。

## 决定

`ChannelModel.UpstreamProtocol`（JSON `upstream_protocol`）只允许空、`chat`、`responses`、`anthropic`。空字符串是默认值，按渠道类型转发，和引入该字段之前一样。

出站顺序是：完全透传且渠道不是 custom 时仍整包透传，忽略模型协议；`OpencodeCompat` 且协议非空时选用现有的 OpenAI Chat、Responses 或 Anthropic 转换器；其余情况，包括自定义渠道写了协议，仍按 `channel.Type`。轮次轨迹的上游协议和透传标记用同一套有效协议。BaseURL 仍是 `https://opencode.ai/zen` 与 `https://opencode.ai/zen/go`，转换器只追加一次 `/v1` 和协议路径。

迁移 017 只加列。回填单独执行，只填 `Builtin && OpencodeCompat` 渠道上仍为空的协议，不覆盖非空值，也不改类型、Key、启停和模型名单。`EnsureBuiltinChannels` 对已存在渠道继续不覆盖。

OpenCode Free 的六个出厂模型在公开目录 `https://models.opencode.ai/api.json` 的 `opencode` 提供方下都没有单独的 `provider.npm`，继承 `@ai-sdk/openai-compatible`，因此种子写成 `chat`。官方 OpenCode 渠道出厂仍然没有模型。同步只对 `OpencodeCompat` 渠道额外读这份目录，按 Zen/Go 分档，用 SDK 映射协议：`openai-compatible` 为 `chat`，`@ai-sdk/openai` 为 `responses`，包名含 `anthropic` 为 `anthropic`，其余留空。目录失败不清模型、不改已有协议。重建 auto 模型时带上协议；手动模型不被 AutoSync 替换。

完全透传与模型协议的优先级见 [完全渠道透传](2026-09-11-complete-channel-passthrough.md)。

## 备选方案

- **把整条内置渠道改成 `openai_responses` 或 `anthropic`** — 改动面小，一条渠道一种转换器；否掉是因为内置渠道类型锁定，而且同一模型在 Zen 与 Go 上可以不是同一种协议。
- **把协议塞进 `ModelLimits`** — 不用新列；否掉是因为那一列只表示最大输出和思考等级，和协议选择混在一起后两边都会误写。
- **移植 opencode2api 的协议包和目录解析** — 已经处理过 Zen/Go 差异；否掉是因为 NovaVeil 要复用现有转换器，不引入另一套 Key 池、匿名改写或 Markdown 表格抓取。

## 后果

- **收益**：跨渠道故障转移按目标模型重新选择现有转换器；协议为空的历史行和非 OpenCode 渠道行为不变。
- **代价与已知上限**：目录不认识或 SDK 无法映射时协议留空，请求仍按渠道类型发出。`applyChannelModelLimits` 仍按 `channel.Type` 注入，不按模型协议改写思考参数。协议转换调试轨迹拿不到模型行，因为 `beginConvTrace` 的调用在不改的 `upstream.go` 里。前端编辑器尚未提供该字段；手工模型通过现有模型保存接口写入。重访信号：OpenCode 更换 SDK 包名，或 Zen/Go 的 BaseURL 不再是在原地址后追加一次 `/v1`。
