# Agent Note: 日志详情展示脱敏命中明细（规则/占位符）

Status: implemented

## 问题

日志页已显示"已脱敏"标记与占位符，但详情中看不到"哪个内容触发了哪条脱敏规则"，规则调优与审计只能靠猜。脱敏引擎 `internal/relay/mask/engine.go` 的 `Apply` 已返回结构化 `MaskResult.Matches`（label/original/placeholder），但转发入口 `applyRequestMask` 只用了 `Masked` 与 `Mapping`，命中明细被丢弃。需求不改变请求转发和响应还原逻辑。属于网关层脱敏的后续增强，总决策见 [网关层脱敏](./2026-09-10-gateway-native-request-masking.md)。

## 决定

把引擎已产出的命中明细接通到日志详情，**首期只展示规则标签 + 占位符安全摘要，不下发/不持久化/不展示命中原文 original**（实施边界修订，见下）：

1. `applyRequestMask` 返回 `[]mask.Match`（开关关闭/未命中时返回空明细，零额外展示数据）。
2. `RequestState` 增加 `mask_matches` 字段，元素为 `MaskMatch{Label, Placeholder}`——**不含 Original**。与预览接口 `/api/v1/mask/test` 的 `maskTestMatch`（仍保留 original）类型分离：预览是管理员主动测试可看原文，日志命中明细首期不下发原文。
3. `handler.go` 应用脱敏后把命中明细（仅 label+placeholder）复制到 `request.MaskMatches` 并发布请求状态，使已打开的日志详情实时收到。
4. 前端 `MaskMatches` 组件在请求体面板内增加"脱敏命中"区：每条一行（规则标签 Pill + 占位符），**不展示原文、不提供展开入口**。无命中不渲染。
5. 持久化错误日志：`model.ErrorLog` 的 `MaskMatches` TEXT 字段只存 label+placeholder，旧记录空数组天然兼容无需迁移；命中明细保留策略与完整请求体配额对齐。

不变项：不把原始请求体写回 body、不改发往上游内容、不新增明文恢复接口、跨轮占位符复用与 `StreamRestorer` 逻辑全部现状保留。详细方案见 [docs/脱敏开发/07-日志详情命中明细.md](../../../docs/脱敏开发/07-日志详情命中明细.md)。

## 实施边界修订：为什么不展示 original

> **2026-09-17 翻转**：本节"不展示 original"的决定已被 [日志命中明细下发命中原文片段](./2026-09-17-mask-match-original-display.md) 取代。命中原文片段（非整份请求体）现已下发，可见性边界复用日志详情既有管理员可见性。本节作为历史因果保留。

初版提议曾考虑"原文默认视觉弱化、管理员点击展开"。落地时按上游安全边界收紧为**完全不展示 original**：

- 查看日志原文不是已批准能力，需另行安全决策；不能靠管理员鉴权或 CSS 模糊代替，前端直接不渲染。
- 后端数据最小化：`MaskMatch` 不携带 Original，SSE 下发与 ErrorLog 持久化的命中明细天然不含原文，即使前端被改也无法展示。
- 预览接口（mask test）的 `maskTestMatch` 仍保留 original，两者类型分离，避免日志链路误复用预览的完整 original。

## 备选方案

- **不做，只看占位符** — 最强论据是零改动、不碰凭据红线；否掉因为无法定位"哪个内容触发哪条规则"，规则误报调优与审计只能靠猜。
- **展示原文（弱化/展开）** — 初版提议；否掉因为命中原文是敏感数据，日志链路展示原文扩大暴露面，与"日志只存脱敏后内容"红线有张力。改为只存 label+placeholder。
- **新增请求体明文恢复接口** — 最强论据是信息最全；否掉因为扩大攻击面，违反凭据红线，命中明细随状态流下发已足够且不新增明文接口。

## 验证

`internal/relay/state_json_test.go` 的 `TestRequestStateJSONMaskMatches` 验证命中明细序列化只含 label/placeholder、不含 original，无命中时 omitempty 不出现。`web-next/src/components/logs/MaskMatches.test.tsx` 验证前端命中区只渲染规则标签 + 占位符、不提供「显示原文」入口。`go test ./internal/relay/...`、`pnpm typecheck`、`pnpm vitest run` 全绿（504 用例）。

## 后果

- **收益**：管理员在日志详情看到"哪个占位符来自哪条规则"，规则误报调优与审计不再靠猜；命中明细随状态流实时下发，无需额外 API；MaskMatch 不携带 original 使日志链路天然不含原文，即使前端被改也无法展示。
- **代价与已知上限**：日志命中明细不展示原文，定位"具体命中原文内容"需到预览接口（mask test）复测——预览是管理员主动测试场景，可看原文，与日志解耦。重访信号：若审计频繁需要"从日志直接看命中原文"，可评估是否为日志原文展示单独立项安全决策（需凭据红线评审），当前边界修订优先数据最小化。
- **SSE 载荷**：命中明细通常几十条以内且只含 label+placeholder，体积可控；进程内状态终态后按 `maxFinished`（200）裁剪。旧版本进程/旧记录字段缺省为空，前端与反序列化按可选处理，天然兼容。
