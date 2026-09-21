# OpenCode 按模型、按档记录原生协议

> 状态：已落地（2026-09-22 工作区，未提交，不是发布说明）。下文保留实施约束；与代码不一致时以代码和 [Agent Note](../.agents/notes/implemented/feature/2026-09-22-opencode-upstream-protocol.md) 为准。
> 当前实现：`ChannelModel.upstream_protocol` 只允许空、`chat`、`responses`、`anthropic`。空值按渠道类型转发。仅 `OpencodeCompat` 且协议非空时选用现有转换器。完全透传优先。自定义渠道写了协议也忽略。六个出厂免费模型种子为 `chat`。官方 Go 渠道出厂无模型。思考档位注入仍按渠道类型，不看模型协议。
> 参考的是 OpenCode 公开目录「同一模型在 Zen 与 Go 上可以不是同一种协议」这一约束。不移植 opencode2api 的协议包、Key 池或目录解析实现。

## 1. 问题

NovaVeil 把 OpenCode 的两个档做成两条内置渠道，而不是一个专用网关：

| 渠道 | BaseURL | 渠道类型 | 出厂模型 |
|---|---|---|---|
| OpenCode Free | `https://opencode.ai/zen` | `openai` | 手工列表，`Source=manual` |
| OpenCode | `https://opencode.ai/zen/go` | `openai` | 空；管理员开 AutoSync 后拉取 |

两条渠道都开启 `OpencodeCompat`，并固定带 `x-opencode-client: desktop` 与 `User-Agent`。自定义渠道的 `opencode_compat` 在创建和启动清理时被强制关掉。这个边界保持不变。

`buildOutbound` 和 `supportsNativeFormat` 只看 `channel.Type`。`openai` 渠道只把 Chat、图片、音视频、Embedding 当成原生格式。客户端走 Responses 或 Anthropic Messages 时，会用 OpenAI Chat 出站转换器改写到 `/v1/chat/completions`。

OpenCode 的公开目录不是这样分的。Zen 与 Go 不共用一套协议：同一个模型可以在免费档走 Chat，在付费档走 Messages。跨渠道故障转移必须按目标档重新编码。渠道类型表达不了这件事，因为内置渠道的类型被锁定，不能把整条渠道改成 `openai_responses` 或 `anthropic`。

## 2. 决定

只给模型行增加上游原生协议。渠道类型仍是 `openai`。转发时若该模型行有协议，用现有的 OpenAI Chat、OpenAI Responses、Anthropic Messages 转换器；没有则保持今天的渠道类型行为。

协议是渠道模型的属性，不是全局模型名的属性。同一名字在 Free 与官方渠道上可以不同。故障转移本来就会对每一跳重新 `buildOutbound`，所以换档时自然重编码，不新增 Key 池，也不在分组层记协议。

## 3. 数据

在 `ChannelModel` 增加可空字段 `UpstreamProtocol`，只允许三个值：

| 值 | 出站转换器 | 与客户端格式一致时 |
|---|---|---|
| `chat` | 现有 OpenAI Chat outbound | `llm.APIFormatOpenAIChatCompletion` 走透传 |
| `responses` | 现有 Responses outbound | `llm.APIFormatOpenAIResponse` 走透传 |
| `anthropic` | 现有 Anthropic outbound | `llm.APIFormatAnthropicMessage` 走透传 |
| 空 | 按 `channel.Type`，即今天的行为 | 不变 |

空字符串是默认值。非 OpenCode 渠道、历史行、目录未覆盖的模型都保持空，选路结果与现在相同。

不要把协议塞进 `ModelLimits`。那一列只表达最大输出和思考等级。不要改 `Channel.Type`。不要为这三值新增渠道类型。

持久化走现有迁移注册方式，下一号是 17。升级只加列，不改已有渠道的类型、Key、请求头、启停和模型名单。已部署库不会因为种子定义更新而重写模型行：`EnsureBuiltinChannels` 对已存在渠道不覆盖。因此要有一次只填空值的回填，范围仅限两条 `Builtin && OpencodeCompat` 渠道；管理员已经写过的非空协议不覆盖。

## 4. 协议从哪来

分三层，后一层失败不得抹掉前一层已经写下的值。

1. **种子。** OpenCode Free 出厂的六个手工模型，在 `BuiltinFreeChannels` 的模型行上直接写协议。这是离线默认值，启动不访问网络。官方渠道出厂没有模型，不在种子里编一份会过期的全量名单。
2. **同步。** 仅当渠道 `OpencodeCompat` 为真时，模型同步在拉取 `/v1/models` 之外，再读 OpenCode 公开能力目录（机器可读 JSON，按档区分 Zen 与 Go）。用目录里的提供方 SDK 判断 `chat` / `responses` / `anthropic`。同步写回的 auto 模型在同一次 `ChannelUpdate` 里带上协议。官方渠道与开了 AutoSync 的免费渠道都走这条路径，所以同一模型名会按各自渠道落成不同的值。
3. **未知。** 目录没有该模型、目录请求失败、或 SDK 无法识别时，协议留空，模型仍按 Chat 渠道转发。不把模型从列表隐藏，不把请求改写成免费档接受的形状。

目录失败与 `/v1/models` 失败分开处理。后者继续沿用现有保护：非 2xx 报错，合法空列表且已有 auto 模型时跳过删除。前者失败只跳过协议填充，保留模型行上一次的非空协议。`SyncModelsTask` 今天重建 auto 模型时只写名字和 `Source`；实施时必须在重建时抄回或重新填上协议，否则每一轮同步都会把协议抹掉。

不抓取说明文档的 Markdown 表格作为必需数据源。不拉取 models.dev 价格，不写上下文窗口、工具和模态；那些是另一项，不放进本计划。

手动模型：管理员在模型行上指定协议，缺省为空。AutoSync 不覆盖 `Source=manual` 的行，现有同步循环已经这样，协议字段跟着这条规则走。

## 5. 转发

`handler.go` 在每一跳已经持有 `channelModel`。`buildOutbound` 与 `supportsNativeFormat` 增加模型参数，按下面的顺序决定出站：

1. `PassThroughBodyEnabled` 为真且渠道不是 custom：维持完全透传，忽略模型协议。透传仍经过分组路由、故障转移和 Key 轮询。
2. 渠道 `OpencodeCompat` 为真且该模型 `UpstreamProtocol` 非空：用该值选择现有转换器，并据此判断这一跳是透传还是转换。
3. 其余情况：保持按 `channel.Type` 的现有判断。自定义渠道即使模型行被写上协议也忽略，因为 `opencode_compat` 不允许为真。

`startRound` 里现在用 `supportsNativeFormat(channel, format)` 和 `upstreamTypeLabel(channel.Type)` 记轨迹。这两处改成同一套有效协议，避免日志仍写 `openai_chat` 而实际出站已经是 Messages。

BaseURL 不加新的拼接规则。Responses 与 Anthropic 转换器继续用渠道上的 `https://opencode.ai/zen` 或 `https://opencode.ai/zen/go`。实施时用测试确认这两条 BaseURL 不会被再插入一段错误的版本前缀；发现错位就改转换器的 BaseURL 归一，不在计划里预先发明路径。

会话头不在本计划改动。`resolveRequestRandomValue` 仍在首次出站前解析，随后重试复用。入站 `ses_` 原样上送和 `prompt_cache_key` 另案处理，避免和协议选择缠在一起。

## 6. 不做什么

- 不嵌入 opencode2api，不复制它的协议转换、目录解析、匿名请求改写或按代理轮换出口。
- 不恢复自定义渠道的 `opencode_compat`，不新增 `anonymous` 开关。免费渠道的公开 Key `public` 保持原样。
- 不建 Zen/Go Key 池。两个档继续是两条渠道，先后顺序用分组优先级和故障转移。
- 不把 `PreferPassthrough` 或完全透传收成权重调度。完全透传的优先级高于模型协议。
- 不改三态熔断，不做「凭据 + 模型」短冷却。那是另一项。
- 不增加 Gemini 入站、Rerank、Responses WebSocket、订阅 OAuth。
- 不在本项改前端信息架构。模型行需要能看见并保存协议；OpenCode 渠道上 auto 模型的协议只读，manual 模型可改。其他渠道编辑器不出现这个控件。

## 7. 实施顺序

1. 模型字段、迁移、空值回填。回填只碰两条内置 OpenCode 渠道的空协议。
2. `buildOutbound` / `supportsNativeFormat` 改为按模型协议选择现有转换器，并让轮次轨迹使用同一结果。
3. 种子模型写上协议。同步在重建 auto 模型时保留或填充协议；目录失败不删除模型、不清已有协议。
4. 渠道编辑器对该字段的读写。没有这一步时，手工模型只能通过已有模型保存接口带上字段，不能作为唯一配置手段上线。
5. 补测试后再改 `CHANNEL_PASSTHROUGH.md` 与 `DEVELOPMENT_routing.md` 各一段：模型协议不是透传，跨渠道重试会按目标模型重新编码。行为落地时另写 Agent Note，本文保持为计划，不把「待实施」改成现在时事实。

## 8. 验收

- OpenCode 渠道上，模型协议为 `anthropic`、客户端为 Messages：这一跳透传，请求路径落在 Messages，而不是 Chat Completions。
- 同一模型协议为 `anthropic`、客户端为 Chat：这一跳走现有 Anthropic 转换器，不新增转换代码。
- 协议为空的 OpenCode 模型，以及任何非 OpenCode 渠道：与改动前的透传/转换矩阵一致，包括图片、音视频和 Embedding。
- 完全透传开启时，模型协议不生效。
- 同一模型名在 Free 与官方渠道上可以一个是 `chat`、一个是 `anthropic`。分组先选中前者并失败后，下一跳按后者重新编码。
- 目录不可用时，已有 auto 模型还在，已有非空协议还在；新出现且目录不认识的模型协议为空，请求仍按 Chat 发出。
- 自定义渠道保存 `UpstreamProtocol` 或打开 `opencode_compat` 后，转发仍忽略该协议，启动清理仍会把 `opencode_compat` 写回 false。
- 升级已有库不改变渠道类型、Key、启停、请求头和模型集合。

测试落点：`internal/relay/channel_test.go` 覆盖选择矩阵；`internal/task/sync.go` 侧覆盖「重建 auto 模型不丢协议」和「目录失败不删模型」；`internal/builtin/builtin_test.go` 覆盖种子值与回填不覆盖管理员非空值。不把真实 OpenCode 网络调用放进单元测试。
