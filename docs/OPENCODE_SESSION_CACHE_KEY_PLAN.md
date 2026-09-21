# OpenCode 合法 ses_ 原样上送，并保留 prompt_cache_key

> 状态：已落地（2026-09-22 工作区，未提交，不是发布说明）。下文保留实施约束；与代码不一致时以代码和 [Agent Note](../.agents/notes/implemented/feature/2026-09-22-opencode-session-and-prompt-cache-key.md) 为准。
> 当前实现：入站合法 `ses_` 原样作为 `x-opencode-session`，优先于 `X-Session-Id`；都不合法则新铸。该值不写入共享随机头。模型同步仍每次新铸。`prompt_cache_key` 只在转到 Chat 或 Responses 且出站还没有该字段时，从脱敏后的正文回写。Anthropic 不新增该字段。
> 与 [按模型协议计划](OPENCODE_NATIVE_PROTOCOL_PLAN.md) 分开落地。本项不改出站协议选择，那一项也不改会话头。

## 1. 问题

OpenCode 上游要求 `x-opencode-session` 是 `ses_` 加 12 位小写十六进制再加 14 位字母数字。格式不对会被拒绝。NovaVeil 已经用 `internal/utils/opencodeid` 生成这种值，并只在 `OpencodeCompat` 渠道上注入。

生成点把客户端自己带来的合法 `ses_` 换掉了。`handler.go` 把 `X-Session-Id` 或入站 `x-opencode-session` 当作 `sessionKey`，用来做会话粘合和脱敏命名空间。`resolveRequestRandomValue` 再用这个键去 `sessionUUIDFor`，未命中就新铸一个 `ses_` 写进上游头。入站头只当缓存键，不上送。上游因此看不到客户端为 prompt cache 准备的会话号。

`prompt_cache_key` 是请求体字段，不是头。同协议透传时请求体原样转发，这个字段已经在。走 axonhub 转换时，出站 JSON 按转换器认识的字段重写，仓库里没有任何路径把 `prompt_cache_key` 抄回去。转换后的每一跳都会丢掉它。

这两件事都只影响上游 prompt cache 能不能命中。它们不改变分组选路。

## 2. 决定

`OpencodeCompat` 渠道的上游 `x-opencode-session` 按下面的顺序取一个值，整次请求的所有重试都用这一次取到的值：

1. 入站 `x-opencode-session` 整段匹配 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$` 时，原样上送。不改大小写，不截断，不重新编码。
2. 否则入站 `X-Session-Id` 匹配同一模式时，原样上送。
3. 否则保持今天的行为：`sessionUUIDFor(sessionKey, 首次渠道, 首次 Key)`。空 `sessionKey` 仍每次新铸且不进缓存。

合法 `ses_` 命中第 1 或第 2 步时，不再调用 `GenerateSessionID`。缓存可以记下这个值，但缓存不得覆盖本请求已经带进来的合法值。下一次请求如果又带了另一个合法 `ses_`，以上游头为准，不用旧缓存顶掉它。

`prompt_cache_key` 只在客户端请求体里已经有、且是非空 JSON 字符串时保留。透传路径不用额外处理。转换路径在出站 JSON 写完之后，若目标是 OpenAI Chat 或 OpenAI Responses，且该字段还不存在，把原字符串写回去。目标是 Anthropic 时不发明字段，也不把它映射成 `cache_control`。超过 256 字节、或类型不是字符串的值直接忽略。

## 3. 为什么不能塞进现有的随机头值

`injectRandomHeaders` 把本渠道所有动态头都设成同一个 `randomValue`。`x-opencode-session` 和全局随机头规则（例如 `x-trace-id`）共用这一次解析结果。若把客户端的 `ses_` 放进 `randomValue`，其他动态头会变成同一个会话号。

实施时把 `x-opencode-session` 从这组共享值里拆出来。其他动态头继续走 `resolveRequestRandomValue`。只有 OpenCode 会话头走第 2 节的三条规则。`OpencodeCompat` 为假的渠道两个路径都不注入 `x-opencode-session`。

模型同步和探测没有客户端会话。`applyOpencodeCompatHeaders` 继续每次新铸，不读、不写转发缓存。

## 4. 不碰的身份边界

`sessionKey` 的读取顺序不变：先 `X-Session-Id`，没有再用入站 `x-opencode-session`。脱敏键在 `apiKeyID > 0` 且会话非空时仍然是 `apiKeyID + ":" + sessionKey`。上送的 `ses_` 不得反过来当粘合键或脱敏键。

本项不顺便改粘合表。relay 复核已经单独记下：`sticky.go` 和 `sessionUUIDFor` 仍用原始 `sessionKey`，不同 API Key 的同名会话会粘到同一成员并共用上游会话号；控制台 `api_key_id=0` 时脱敏键没有前缀。那是独立修复，应先于或与本项一起落地。原样上送只决定头的值，不扩大这张表的键。

不把首条用户消息、`conversation_id`、`metadata.session_id` 或任何额外头收进 `sessionKey`。不把非法会话号哈希成 `ses_`。UUID、空值和其他形状继续走现有新铸逻辑，避免和「原样」混成一种规范化。

`prompt_cache_key` 从已经做过脱敏的请求体上读取。不回到脱敏前把原文再写进上游。不把这个字段写入日志、追踪或错误响应。

## 5. 不做什么

- 不改 `opencodeid.GenerateSessionID` 的算法。
- 不在自定义渠道上恢复 `opencode_compat`。
- 不补 `x-opencode-request`、`x-opencode-project`，不把 `x-opencode-client` 改成 `cli`，不改 User-Agent。
- 不保留 `safety_identifier`、`service_tier`、`store`。本项只有 `prompt_cache_key`。
- 不改完全透传的请求体规则。透传本来就会留下这个字段。
- 不把协议选择、能力目录或计价放进本项。

## 6. 实施顺序

1. 抽出「是否为合法 `ses_`」判断，与 `fetch_test.go` 里已有的格式正则对齐，单独测大小写、长度和前后空白。空白不算合法，不做 trim 后放行。
2. 转发路径按第 2 节选择会话头，并让 `injectRandomHeaders` 不再用共享值覆盖它。重试断言该头不变，同时断言其他动态头仍是原来的随机值。
3. 转换完成后回写 `prompt_cache_key`。覆盖 Chat→Responses、Chat→Anthropic、以及同协议透传三种出口。Anthropic 出口的上游 JSON 里不得出现这个字段，除非转换器本来就会生成它。
4. 行为落地时另写 Agent Note。本文保持计划，不改成现在时事实。

## 7. 验收

- 入站 `x-opencode-session` 为合法 `ses_` 时，OpenCode 渠道的上游头与它逐字节相同，包括同一请求内的故障转移重试。
- 只有 `X-Session-Id` 为合法 `ses_` 时，上游头等于它。两者都合法但不相同的，以上游头用 `x-opencode-session`。
- `X-Session-Id` 为普通 UUID、且没有合法 `x-opencode-session` 时，上游头仍是新铸的合法 `ses_`，粘合键仍是那个 UUID，脱敏键仍带 API Key 前缀。
- 无会话头时，每次请求新铸，不进缓存；同一请求的重试复用这一次的值。
- 渠道配置了别的动态头时，那个头的值不等于 `x-opencode-session`，除非两者本来就会撞上。
- 非 OpenCode 渠道不出现 `x-opencode-session`。模型列表请求仍是每次新铸。
- Chat 转 Responses 后，客户端原来的合法 `prompt_cache_key` 还在，且不超过 256 字节的限制被遵守。转到 Anthropic 时不新增该字段。同协议透传不改请求体里的这个字段。
- 脱敏把该字段里的命中替换成占位符后，上游看到的是占位符，不是脱敏前原文。
