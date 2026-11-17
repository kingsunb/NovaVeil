# Agent Note: 跟进 opencode2api 2026-11-17 上游修复

Status: implemented

## 问题

上游 opencode2api 在 2026-11-17 连发 11 个提交，其中四项与 Nova 的免费档形态直撞：

1. **`clint_` 已被免费层列入拒绝字面量**（95575c5）。同样的整形正文换前缀就通过、用 `clint_` 就返回 429 `FreeUsageLimitError`，说明被识别的是前缀本身而非改名动作。Nova 此前沿用 `clint_`（[v2 工具改名笔记](../feature/2026-10-25-opencode-v2-tool-alias-and-day-anchor.md)），带自定义工具的请求全部踩这个门。
2. **标题请求不能补 agent 工具集**（14faec7）。OpenCode 的 title 请求没有 tools，免费层接受该形态；一旦附上 agent 工具集就返回 403 `FreeTierError` 或 429 `FreeUsageLimitError`。Nova 对所有免费档 Chat 请求无条件整形，标题请求也会被补上官方工具集。
3. **旧版 `functions` 会被重复编码**（14faec7）。先转换再进入共用改名流程才能保证名字只编码一次；Nova 此前完全不处理 `functions`。
4. **传输失败会吞掉前一轮的 HTTP 结果**（14faec7）。重试只发生传输错误时，最终报出的是通用错误，客户端拿不到真实状态码与 `Retry-After`。

另有两项体验改进：Responses 跨协议转换把交错的 assistant 文本与工具调用拆成多条消息（14faec7），以及注入提示词的环境字段只能跟随网关主机（14faec7）。

## 决定

- `ocToolAliasPrefix` 由 `clint_` 改为 `mcp_`，跟随上游选择。理由：`mcp_` 与 MCP 客户端的 `mcp__<server>__<tool>` 同前缀，上游若拒绝它会连带拒绝自己的 MCP 流量，短期最安全。代价是前缀暴露后需要轮换，注释已写明轮换信号（只有带工具的请求回 429 时）。
- `shapeOpencodeFreeBody` 用**客户端原始正文**判定标题请求（注入前判定），命中时跳过 `ensureOfficialToolset`、v2 核心工具补齐与 v1 `tool_choice: auto` 注入，仍强制 `stream=true` 并注入官方提示词。判定复用 `isTitleGenerationRequest`，补充 v2 的 5 条提示词标记，新增「出现 assistant/tool/function 角色即不是标题请求」的保护，并把判定输入归一化：Chat 读 `messages`，Responses 读 `input` 并把 `instructions` 当作 system 消息——2.x 标题请求的指令在 instructions、消息在 input，只看 messages 会漏判。
- `ensureOfficialToolset` 在改名之前先调用 `ocConvertLegacyFunctions`：`functions[]` 转成 `tools[]` 条目、删除原字段，随后与 `tools` 一起进入同一次改名循环。`rewriteToolReferences` 补充顶层 `function_call.name` 与 `messages[].function_call.name`，保证历史与具名选择同步改名。
- `buildOfficialToolTargets` 同时登记 `tools` 与旧版 `functions`，否则只有 `functions` 的客户端拿不到官方工具名的响应回映；`mergeAssistantTurn` 合并时保留旧版 `function_call`，两侧都有则放弃合并，避免回合合并丢历史。
- 重试保留上游失败：循环内用 `lastHTTPErr` 记住最近一次可解析出状态码的上游错误（排除 413，它由请求级不可行单独处理）；轮次超限与截止两个收尾点调 `logRetainedUpstreamFailure`，把状态码与错误原文写进 warn 日志。**对外仍统一回 `errNoAvailableChannels`（400）**，不透出上游 429/5xx 与 `Retry-After`——这是既有下游契约，客户端应按确定性失败自行重试。
- Responses 回合合并放在 `conversionMiddleware.OnOutboundRawRequest` 的 Chat 分支（跨协议转换后）：`mergeAssistantTurns` 把连续 assistant 消息的正文与 `tool_calls` 合并成一条，正文形态冲突（string 对 array）时放弃合并；`reasoning_content` 只保留一份。用户输入与工具结果仍分隔回合。
- 新增 `prompt_environment` 设置项（JSON），可覆盖 `working_directory`、`workspace_root`、`platform`、`is_git_repo`。渲染官方提示词时按整行替换 env 块字段，保留缩进与字段顺序；`platform` 同步改写官方工具描述里的 `on Linux using bash` / `OS: linux, Shell: bash` 措辞。校验在 `Setting.Validate`：路径最长 256 字节且拒绝控制字符，平台仅接受 `linux`/`darwin`/`win32`。

## 备选方案

- 换一个自造前缀（不用 `mcp_`）：不会与 MCP 命名撞车，但失去「拒绝即自伤」的保护，上游学习成本更低；不采用。
- 标题请求直接拒绝：Nova 已有 `RejectTitleRequests` 分组策略，但默认放行时不整形才是与上游一致的行为；把拒绝设为唯一路径会改变默认语义。
- 在 axonhub 依赖里修 Responses 项合并：改动落在第三方库，本仓库无法维护；改为在转换后的 Chat 正文上做等价合并。
- 把 `prompt_environment` 做成渠道级字段：注入提示词由免费档整形统一生成，与渠道无关；放设置层更贴合实际生效范围。

## 后果

- 带自定义工具的免费档请求不再因 `clint_` 被拒；`mcp_` 只用于出站，响应侧仍按别名表还原客户端声明名，JSON 与三种协议的流式还原路径不变。
- 标题请求不再被补工具集，不会再出现「整形后 403/429」；含对话轮次的 agent 请求仍照常整形。
- 旧版 `functions` 客户端可以正常走免费档，工具名只编码一次，历史引用与 `tool_choice` 同步。
- 重试耗尽时对外行为不变：客户端仍收到 400「暂无可用渠道」，503 只保留给满载/一键停止/WARP 槽位；上游 429 与 `Retry-After` 只进内部日志，供排障定位。
- Responses 客户端跨协议到 Chat 上游时，同一回合的文本与工具调用保持相邻，减少上游把工具调用当成新回合的概率。
- `prompt_environment` 默认空，出厂行为与官方模板完全一致；只有显式配置才改写注入内容。

## 验证

由用户执行（AGENTS 禁止 AI 运行 build/test/vet）：

```sh
gofmt -l internal/relay internal/model
go test ./internal/relay -run 'TestEnsureOfficialToolsetConvertsLegacyFunctions|TestShapeOpencodeFreeBodySkipsTitleTools|TestShapeOpencodeFreeBodyShapesAgentWithTurn|TestMergeAssistantTurns|TestEnsureOfficialToolsetNoClientVocabularyLeak|TestShapeV1AlsoAlignsToolset|TestRestoreToolAliasesInEventAndBody' -v
go test ./... && go vet ./...
cd web-next && pnpm typecheck && pnpm lint
```

线上复核：用带自定义工具的免费档请求打一轮，确认出站 `tools[].function.name` 以 `mcp_` 开头、响应还原为客户端原名；再发一条 title 请求，确认出站正文没有 `tools` 数组。
