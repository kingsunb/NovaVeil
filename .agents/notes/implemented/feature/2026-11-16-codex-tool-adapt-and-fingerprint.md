# Agent Note: Codex 客户端工具适配与描述指纹混淆

Status: implemented

## 问题

NovaVeil 向免费上游广播官方工具集（bash、read、edit、todowrite、patch 等），并在响应中把模型调用的官方工具名与参数还原为客户端声明的等价名。三类缺口导致部分客户端无法正常使用：

1. Codex 类客户端只声明 `exec_command`（shell）、`update_plan`（待办）、`apply_patch`（补丁），不含 `Bash`/`bash`/`TodoWrite` 等候选名。`ocOfficialToolCandidates` 未收录这三个名，`buildOfficialToolTargets` 找不到匹配，官方 bash/todowrite/patch 的调用无法还原为客户端名，客户端收到不认识的工具调用而报错。
2. 严格上游（DashScope、Cloudflare）对 `messages[].content` 为 `null` 或缺失的消息返回 400 "content field is required"。NovaVeil 的 `normalizeChatRoles` 只做角色归一，不处理 content 空值。
3. NovaVeil 用 SHA256 摘要改工具名以规避免费层按名过滤，但工具描述（`function.description`）原样保留。若上游按描述指纹识别客户端自定义工具，改名不足以规避。

参考来源：jasonxu114514/opencode2api PR #177（Codex 工具适配）与 PR #179（描述指纹混淆）。按 AGENTS.md 铁律，社区实现只作交叉参考，不照抄；以下决策基于 NovaVeil 自有架构独立设计。

## 决定

### 概念 1+2：Codex 候选与参数变换

文件 `internal/relay/opencode_tool_alias.go`：

- `ocOfficialToolCandidates` 末尾追加 `"exec_command"`（bash、shell）、`"update_plan"`（todowrite）、新增 `"patch": ["apply_patch"]`。`buildOfficialToolTargets` 按优先级匹配，Codex 客户端只声明 exec_command 时 bash 映射到 exec_command。
- `ocOfficialArgumentAliases` 追加 `"patchText": ["input"]`（patch→apply_patch 参数键）。
- `ocOfficialToolTarget` 新增 `transform func(official, args string) string` 字段。`buildOfficialToolTargets` 在 todowrite→update_plan 时注册 `todoWriteToUpdatePlanArgs`：把 `todos` 数组转为 `plan` 数组，丢弃 cancelled 条目，`content` 映射为 `step`，status 归一为 in_progress/completed/pending 三态。
- `restoreOfficialToolCallsInBody`（非流式）与 `officialToolStream.flushPending`（流式）在 `adaptOfficialToolArguments` 之前调用 `transform`（若有）。transform 先做结构变换，adaptOfficialToolArguments 再按客户端 schema 做键名改写与未声明键丢弃。

不含 Windows PowerShell 命令合成（read/write/edit/glob/grep→exec_command，300+ 行 Windows 专属代码）。按 YAGNI，仅做平台无关的 shell→exec_command + todowrite→update_plan + patch→apply_patch。

### 概念 3：null content 归一

文件 `internal/relay/upstream.go`：

- 新增 `normalizeChatContent`：扫描 `messages`，`content` 为 null 或缺失时 sjson 设为 `""`。数组 content（多模态）与非空字符串 content 不变。
- 在 Chat 出站 `normalizeChatRoles` 之后调用（`upstream.go` Chat 分支内）。

### 概念 4：描述指纹混淆

文件 `internal/relay/opencode_tool_alias.go`：

- 新增 `obfuscateToolDescription`：在空格前插入零宽空格（U+200B），可见文本不变，幂等（已含 U+200B 则原样返回）。无空格描述不变。
- 新增 `sanitizeSchemaDescriptions` 与 `obfuscateDescInPlace`：递归遍历 JSON schema，对所有 `description` 字段调用 `obfuscateToolDescription`。无 description 的 schema 原样返回（不重新序列化）。
- `ocToolRename` 改名后对 `function.description` 与 `function.parameters` 调用上述函数。

## 备选方案

- **照搬 PR #177 的 tooladapt.go（509 行）**：含 Windows PowerShell 命令合成、apply_patch 全套处理。NovaVeil 架构不同（relay 层 vs gateway+protocol），且 Windows 专属逻辑按 YAGNI 暂不需要；否掉。
- **只加候选名，不做 transform**：bash→exec_command 的参数键 `command` 恰好同名，adaptOfficialToolArguments 能处理；但 todowrite→update_plan 的 `todos`→`plan` 是结构变换（数组元素内层键 content→step + 丢弃 cancelled），键名映射无法表达；故 transform 机制必要。
- **null content 归一放进 normalizeChatRoles**：normalizeChatRoles 有多条早返回路径，嵌入会增加分支复杂度；独立函数职责单一，调用点明确；采用独立函数。
- **描述混淆用全字符插 U+200B**：每个字符间都插，描述长度翻倍；仅在空格前插已足以改变指纹，且对 JSON 序列化更友好；否掉全字符方案。

## 后果

- **收益**：Codex 类客户端（只声明 exec_command/update_plan/apply_patch）的官方工具调用可正确还原；严格上游不再因 null content 返回 400；工具描述指纹被零宽空格扰乱，降低按描述识别的风险。
- **代价与已知上限**：transform 仅覆盖 todowrite→update_plan，其他结构变换（如 Windows 文件工具→PowerShell）未实现；描述混淆仅对含空格的描述生效，无空格描述不变；sanitizeSchemaDescriptions 在有 description 时用 json.Marshal 重建 schema，键序按字典序排列（JSON 语义不变）；obfuscateToolDescription 幂等，二次整形不会重复插入。
- 不改变路由、计费、用量统计或脱敏逻辑；transform 在 adaptOfficialToolArguments 之前执行，非法 JSON 由 transform 原样返回后交由既有校验拦截。

## 验证

- `go test ./internal/relay -run 'TestBuildOfficialToolTargetsCodexCandidates|TestTodoWriteToUpdatePlanArgs|TestObfuscateToolDescription|TestSanitizeSchemaDescriptions|TestNormalizeChatContent' -count=1`
- `go test ./internal/relay -run 'ToolAlias|OfficialTool|Content|Fingerprint|PreservesSameName|ProtocolContract' -count=1`
- `go vet ./internal/relay/...`

以上命令由用户执行。
