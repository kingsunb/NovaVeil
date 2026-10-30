# Agent Note: 移除自定义工具数量上限与溢出机制

Status: implemented

## 问题

同日在 [工具限量笔记](2026-10-30-opencode-tool-limit-33.md) 落地的名额上限（初值 33、晚些收紧 23）与 `<additional_tools>` 溢出机制，经与 opencode2api 参考实现对照后判定为**误诊产物**：

1. 早间对照矩阵自证：总数同为 75，带 `subagent_fork` 失败、换 `team_task_create` 成功——**数量相同、换一个名字结果翻转**，触发因素是名字而非数量；
2. opencode2api 实证免费层按「请求体中的其他客户端词汇」拒绝（Claude Code 的 `ReportFindings`+`ScheduleWakeup` 组合即触发）；其实现**没有任何数量上限**，全部客户端工具摘要改名进数组；
3. 上限存在期间，词汇触发点（`clint_+原名` 语义名与溢出块明文）未被消除，数量曲线实为词汇命中的代理变量。

工具名 opaque 摘要化（见 [出站名摘要笔记](2026-10-30-opencode-tool-name-digest.md)）落地后，词汇触发点已消除，数量上限与溢出机制失去存在理由。

## 决定

`internal/relay/opencode_tool_alias.go`：

- 删除 `ocToolAliasMaxTools` 名额上限、`overflow` 收集与 `appendExtraToolsNote`（含 `extraToolDescLimit`/`extraToolCompactThreshold`）——全部客户端工具摘要改名后进数组，官方全集在前；
- 白名单保留，语义从「占位名额」降为「数组内排序优先」；
- `shapeOpencodeFreeBody` v1 档删除为溢出块兜底的第二轮 CRLF 归一（溢出块不复存在，首轮归一已覆盖全部 system 内容来源）。

与 opencode2api 的结构对齐：客户端工具（摘要名）+ 官方全集同数组，无文本注入。

## 备选方案

- **保留上限作为保险**：词汇触发已消除，保险防的是一个不存在的触发器，却让 dsh 46 个、Pi 107 个工具的客户端持续损失能力（溢出工具模型无法结构化调用）。
- **仅提高上限不删除溢出**：两套机制并存，溢出块携 description 语义仍是潜在指纹面，且维护两套路径。

## 后果

- **收益**：dsh/Pi 等多工具客户端的全部自定义工具恢复结构化可调用；与参考实现结构一致；删除约 90 行死代码路径。
- **代价**：Pi（107 工具）等客户端请求体积显著增大（全部 schema 上数组）；若日后实证上游确有数量/体积门禁，需重引限量——届时应以 opaque 名 + 稳定对照环境重测，不复用本轮被污染的数据。
- **遗留修正**：`2026-10-30-opencode-tool-limit-33.md` 的边界结论（62/63/64 曲线）判定为 super 通道噪声与词汇触发的混合污染，以本篇为准。

## 验证

```sh
go test ./internal/relay/ -run 'TestEnsureOfficial|TestShapeOpencodeFree|TestRestoreToolAliases' -v
```

- `TestEnsureOfficialToolsetNoClientVocabularyLeak`：27 个客户端工具全部进数组、零原始名泄漏、无溢出块。
- 部署后以多工具客户端发一条请求，抓包确认 `tools` 数组 = 官方全集 + 全部摘要名工具，system 无 `<additional_tools>`。
