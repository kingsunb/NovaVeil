# Agent Note: OpenCode 会话号原样上送并保留 prompt_cache_key

Status: implemented

## 问题

OpenCode 上游只接受 `ses_` 加 12 位小写十六进制再加 14 位字母数字的 `x-opencode-session`。转发层本来会用 `sessionUUIDFor` 新铸一个号，客户端自己带来的合法会话号只当作粘合和脱敏的键，不上送。上游因此对不上客户端为 prompt cache 准备的会话。

`prompt_cache_key` 是请求体字段。同协议透传时正文原样带走它。走转换时出站 JSON 按目标协议重写，这个字段会丢掉。把它放进和其他动态头共用的随机值里，`x-trace-id` 也会变成同一个会话号。

## 决定

`OpencodeCompat` 渠道的上游 `x-opencode-session` 在首次真正发上游前取一次，同一请求的全部重试复用这次的值。取值顺序是：入站 `x-opencode-session` 整段匹配 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$` 则原样上送；否则 `X-Session-Id` 匹配同一模式则原样上送；两者都合法但不相同的，用 `x-opencode-session`。都不合法时用这次已经解析好的 `sessionUUIDFor` 结果。不改大小写，不 trim 后放行，不把非法值哈希成 `ses_`。空会话仍每次新铸且不进缓存；同一请求的重试复用这一次铸出的值。

这个会话号不进入 `injectRandomHeaders` 的共享 `randomValue`。`x-trace-id` 等其他动态头继续用原来的随机值。`OpencodeCompat` 为假时不注入 `x-opencode-session`，全局随机头规则里的同名头也不再写成共享随机值。命中入站合法 `ses_` 时不把该值写进随机头缓存，缓存里的旧随机值也不能盖过本请求带进来的合法值。

粘合键和脱敏键仍是 `sessionScopeKey`，读取顺序仍是先 `X-Session-Id`、没有再用入站 `x-opencode-session`。上送的 `ses_` 不反过来当粘合键或脱敏键。模型同步的 `applyOpencodeCompatHeaders` 仍每次新铸，不读转发缓存。

`prompt_cache_key` 只在转换完成后回写。客户端正文里该字段已是非空 JSON 字符串、解码后不超过 256 字节、目标是 OpenAI Chat 或 Responses、且出站 JSON 还没有该字段时，把这个字符串写回去。读取的是脱敏之后的 `raw.Body`。目标是 Anthropic 时不新增该字段，也不映射成 `cache_control`。同协议透传不改这个字段。回写不进入 `JSONBody`，也不另写日志。

## 备选方案

- **把客户端 `ses_` 放进共享 `randomValue`**：一次解析就能让重试稳定，但 `x-trace-id` 会变成会话号。会话头因此从这组共享值里拆出来。
- **把入站合法 `ses_` 写进 `sessionUUIDFor` 的缓存**：下次没带头时可以复用。这张缓存同时也是其他动态头的值，写进去会让那些头变成会话号。入站合法值只决定本次头，不覆盖这张缓存。
- **非法会话号哈希成 `ses_`，或 trim 后放行**：能多送出一些头，但和「整段原样」混成规范化。空白和形状不对的值继续走现有新铸。
- **Anthropic 出站把 `prompt_cache_key` 映射成 `cache_control`**：目标协议没有这个字段。映射会发明客户端没写的缓存控制。

## 后果

- **收益**：OpenCode 渠道把客户端合法会话号原样交给上游，重试不会换成另一个号。转换到 Chat 或 Responses 时，脱敏后的 `prompt_cache_key` 还在。
- **代价与已知上限**：没有合法入站 `ses_` 时，会话头和其他动态头仍可能是同一次 `sessionUUIDFor` 的结果。下一次请求若不再带合法 `ses_`，上游会话号回到缓存里的随机值，而不是上一请求的客户端号。超过 256 字节或非字符串的 `prompt_cache_key` 被丢掉。模型同步路径仍没有客户端会话，每次新铸。
