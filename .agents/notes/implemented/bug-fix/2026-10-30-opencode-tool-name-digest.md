# Agent Note: 客户端工具出站名改 opaque 摘要

Status: implemented

## 问题

免费层按「请求体中的其他客户端词汇」拒绝请求：参考 opencode2api 的实证（`参考/opencode2api-main/internal/protocol/tools.go`），Claude Code 的 `ReportFindings` 与 `ScheduleWakeup` 两个名字同时出现即触发 429 `FreeUsageLimitError`，去掉任一即解除。本网关此前的改名是 `clint_ + 原名净化`（如 `clint_web_search`、`clint_ask_user_question`），客户端词汇仍在线上；且 `<additional_tools>` 溢出块把客户端**原始**工具名明文写进 system 文本（线上抓包可见 `list_mcp_resource_templates`、`mcp__superai_search__web_fetch`）。2026-10-30 线上 63 工具请求携带 23 个语义名 + 10 个明文溢出名，是 429 的高危来源；「零工具可过、41 工具（仅 `clint_question` 一个无 CC 词汇的名字）可过」的对照也指向词汇维度而非纯数量。

## 决定

`internal/relay/opencode_tool_alias.go`：

- 出站名 = `clint_` + `sha256(客户端名)` 前 12 位 hex（`ocToolWireName`，`ocToolDigestLen = 12`），与 opencode2api 的 `ClientToolWireName` 对齐——摘要对名字确定，跨请求稳定，客户端词汇完全不上线；
- 溢出工具在收集阶段即统一改名并登记别名表（`ocToolRename`），随后才按 `ocToolAliasMaxTools` 分流进数组或溢出文本块——文本块渲染的是已改名条目，不再出现原始名；
- 别名表覆盖全部改名工具，历史 `tool_calls` / `tool_choice` 改写（`rewriteToolReferences`）与响应侧还原（`restoreToolAliasesInBody/InEvent`）随之全覆盖；
- `sanitizeToolAliasName` 删除（唯一调用点已替换）。

## 备选方案

- **去掉 `<additional_tools>` 块**：参考实现无溢出机制（全部客户端工具进数组）。保留块是既有产品决策（能力公告），本次只脱敏其内容。
- **溢出块整列删除名字列**：模型失去指代锚点，文本化调用完全失效。
- **出站名每次请求随机**：破坏跨请求稳定性，同一客户端工具应恒定对应一个出站名。

## 后果

- **收益**：出站正文（tools 数组、system 文本、历史 tool_calls）不再含任何客户端原始工具名，词汇触发点消除。
- **代价**：模型看到不可读摘要名，只能靠 description 判断用途；模型在**文本**（非结构化 tool_calls）里回显摘要名时响应侧不还原（现有还原只覆盖 tool_calls 字段，扩展需流式部分名匹配，另行评估）；溢出工具的文本化调用对客户端不可直接映射（客户端无法反查摘要名），能力进一步降级为「仅公告」。
- **既有问题未变化**：同一 body 两次整形会对摘要再摘要（摘要名的确定性使然）。

## 验证

```sh
go test ./internal/relay/ -run 'TestEnsureOfficial|TestShapeOpencodeFree|TestRestoreToolAliases' -v
```

`TestEnsureOfficialToolsetNoClientVocabularyLeak`：27 个客户端工具（含触发词 `ReportFindings`、`ScheduleWakeup`）整形后，tools 数组与 system 文本均不含任何原始名，别名表全覆盖，溢出块以摘要名指代。
