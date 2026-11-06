# Agent Note: 免费档官方工具名回映客户端声明

Status: implemented

## 问题

OpenCode 免费档 Chat 上游同时看到官方工具集与客户端工具（客户端工具在出站时改名为 `clint_<摘要>`）。模型会选择官方 `read`/`bash`/`edit`/`todowrite`，并使用官方参数键（`filePath`、`oldString`、`todos[].priority`）。Nova 响应侧只把 `clint_` 摘要名还原成客户端名，官方名不在别名表，原样透传给客户端。

客户端只声明自己的工具名与严格 schema：Claude Code 声明 `Read`/`Bash`/`Edit`/`TodoWrite` 等，`additionalProperties:false`；DSH 声明 `read`/`edit`/`todo_write`/`pwsh` 等。结果：

- Claude Code：收到官方 `read`/`bash`，不在声明内，最终空回复（会话 `8cb565ac...`，outputTokens 0）。
- DSH：`Error: unknown tool "bash"`、`Error: unknown tool "todowrite"`；`missing required property "file_path"`、`old_string`、`new_string`；`todos[].priority` 被 `additionalProperties:false` 拒绝。
- New API 只是透传到 Nova；上游响应中能看到 `tool_use name=read` + `input.filePath`，以及 `name=bash` + `input.command`，根因在 Nova 的响应映射。

PR #166（https://github.com/jasonxu114514/opencode2api/pull/166）只做官方名→客户端名映射，参数原样透传；即使照搬，`filePath` 与 Claude Code 的 `file_path` 仍不匹配，因此不能只移植该 PR。

## 决定

保留官方工具集，不删除；只在响应侧做映射与参数适配，避免 #163/#161 的 body shape/429 风险。

`internal/relay/opencode_tool_alias.go`：

- `ocOfficialToolCandidates`：官方名 → 客户端候选名；`bash`/`shell` 含 `Bash`、`bash`、`pwsh`，`todowrite` 含 `TodoWrite`、`todo_write`、`todo`，其余 read/edit/write/glob/grep/question/task/subagent/webfetch/websearch/skill 同理。
- `buildOfficialToolTargets`：从已转成 Chat 的客户端请求 `tools[].function.name/parameters` 建 target；跳过非 function 与 `clint_`；客户端没声明等价名则不登记，官方工具保持原样。
- `adaptOfficialToolArguments`：gjson 解析参数；顶层按 `ocOfficialArgumentAliases` 改键（`filePath`→`file_path`/`path`、`oldString`→`old_string`、`newString`→`new_string`、`replaceAll`→`replace_all`、`background`→`run_in_background`、`agent`→`subagent_type`、`name`/`id`→`skill`、`command`→`script`/`cmd`、`workdir`→`cwd`）；`ocPruneOfficialValue` 按客户端 schema 递归裁剪 object/array，丢弃未声明键（含 `todos[].priority`）；`webfetch` 缺 required `prompt` 时补默认值。
- `restoreOfficialToolCallsInBody`：非流式 Chat `choices[].message.tool_calls[]` 的 name/arguments 映射；`ocSJSONPath` 转义反斜杠和点。

`internal/relay/upstream.go`：

- `conversionMiddleware.officialTools` 保存本次请求的 target；`OnOutboundRawRequest` 在 `shapeOpencodeFreeBody` 前构建，并在重置别名表时一起重置。
- `OnOutboundRawResponse` 先还原 `clint_` 别名，再做官方名/参数映射；`OnOutboundRawStream` 在 official 非空时包 `officialToolStream`，否则保持 `toolAliasStream`。
- `officialToolStream` 按 `(choice index, tool index)` 合并流式 name/arguments 分片；工具调用事件暂存，`finish_reason`/`[DONE]`/EOF 时合成 Chat chunk 下发，再发原 finish；文本/推理事件原样通过；上游 `error` 或流错误时丢弃半成品。这样解决名字被拆成多个 SSE chunk 时无法映射的问题。
- `sendPassthrough` 在整形前构建本地 `officialTools`；流式与 collapsed 走 `applyToolAliasRestoreWithOfficial`，非 Chat/完全透传与普通非流式 body 追加 `restoreOfficialToolCallsInBody`。`sendConverted` 普通路径由 raw hooks 覆盖，collapse 在聚合前已由 raw 阶段映射。

## 备选方案

- 删除官方工具集：终态更干净，但 #163/#161 证明免费层对 body shape 敏感；没有上游 replay 证明 429 不回来，不做。
- 只移植 PR #166：名称能对上，参数仍会被 Claude Code/DSH 的严格 schema 拒绝。
- 把官方工具 schema 改成客户端 schema 再出站：会改变请求形态，风险同删除官方工具集；不采用。
- 只在客户端做兼容：客户端 schema 是 `additionalProperties:false`，无法要求所有客户端放宽；不采用。

## 后果

- Claude Code/DSH 在声明了等价工具时能收到自己声明的名字和参数键；未声明等价名时官方工具仍原样透传，保持现有行为。
- 代价是流式工具调用被缓冲到 finish/EOF 才下发，工具参数不再逐 token 到达客户端；文本/推理不受影响，总耗时基本不变，内存为正在缓冲的参数大小。上游错误时丢弃半成品，避免把不完整工具调用交给客户端。
- 文本回显（非结构化 tool_calls）仍不还原，和既有摘要名限制一致。
- 官方工具集保留，body shape/429 风险不变。

## 验证

由用户执行（AGENTS 禁止 AI 运行 go build/test/vet）：

```sh
gofmt -w internal/relay/opencode_tool_alias.go internal/relay/upstream.go internal/relay/official_tool_restore_test.go
go test ./internal/relay -run 'TestBuildOfficialToolTargets|TestRestoreOfficialToolCalls|TestOfficialToolStream|TestEnsureOfficial|TestRestoreToolAliases|TestShape' -v
go test ./internal/relay && go vet ./internal/relay
go test ./...
```

重点用例：`TestBuildOfficialToolTargetsAndAdaptArguments`（含 Edit/Read/Bash/todo_write/WebFetch/pwsh、`todos[].priority` 裁剪）、`TestRestoreOfficialToolCallsInBody`、`TestOfficialToolStreamBuffersSplitCalls`、`TestOfficialToolStreamFlushesAtEOF`、`TestOfficialToolStreamKeepsAliasArguments`。
