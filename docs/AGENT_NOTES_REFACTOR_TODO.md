# docs/ 事实-因果拆分：进度与待办

> 本文件记录把 `docs/` 大设计文档按"事实/因果"拆分的进度。已完成项可直接看，未完成项是下次接续的清单。
> 原则：`docs/` 只留"现在怎么跑"的现在时事实；决策理由/备选/历史迁到 `.agents/notes/`。

## 已完成

| 文档 | 做了什么 |
|---|---|
| `docs/CHANNEL_PASSTHROUGH.md` | 全文重构为事实态：剥掉背景/目标、new-api 参考实现、与 new-api 差异对照、"改动前→已改为"的 diff 历史；留机制/支持 handler 表/跳过保留/Header 透传/applyChannelConfig/调用链路/边界。因果链到 `implemented/feature/2026-09-11-complete-channel-passthrough.md`。 |
| `docs/DEVELOPMENT_routing.md` | 剥"旧行为对照"段、"与 OmniRoute 对照"参考段、"设计依据是…"理由句；配置表去"本次开发新增"标注；§七从"实现状态映射(既有/本次开发补齐)"改为中性两列"设计条目与实现位置"。因果链到 `implemented/architecture/2026-09-11-session-affinity-circuit-breaker-routing.md`。 |
| 新笔记 ×3 | `implemented/architecture/2026-09-10-mask-placeholder-format.md`（占位符格式决策）、`implemented/architecture/2026-09-10-mask-stream-restore-channel-buffering.md`（流式还原通道缓冲决策）、`proposed/feature/2026-09-15-log-detail-mask-match-detail.md`（日志命中明细提案，练手）。 |

## 未完成（下次接续）

### 1. `docs/脱敏开发/README.md` —— 因果重，待重构
当前混了：§一背景与目标（1.1问题/1.2目标/1.3为什么在NovaVeil做）、§二参考maskit能力对照表、§四实施阶段与工作量（历史规划）、§七风险与待决策里"已锁定"的决策理由。
- **留**：§三核心原理图、§五关键工程约束（改现在时陈述为不变量）、§六目录规划、§八默认关闭保证（改现在时事实）。
- **剥**：§一/§二/§四 → 因果已在 `implemented/feature/2026-09-10-gateway-native-request-masking.md`；§七"已锁定"项的理由移笔记、"待决策"项保留为开放问题。
- 顶部加 blockquote 链到笔记。

### 2. `docs/脱敏开发/02-规则引擎设计.md` —— 轻改一处
§1.2"为什么用 6 位辅音串"是因果（理由：消除模型对 hex 做算术的诱因）。
- 把 §1.2 的理由删掉，改一句"格式理由见 [笔记](../../.agents/notes/implemented/architecture/2026-09-10-mask-placeholder-format.md)"。其余（规则定义、占位符规范、Go 实现）是事实，保留。

### 3. `docs/脱敏开发/05-借鉴思路总结.md` 与 `06-关键源码参考.md` —— 纯因果/参考
这两篇是 maskit 的设计智慧与源码片段，本质是"因果/参考"而非"NovaVeil 现在怎么跑"。
- **建议**：顶部加 blockquote 标注"本文为 maskit 设计参考（因果），NovaVeil 的决策与理由见 `.agents/notes/implemented/architecture/2026-09-10-mask-*.md`；本文保留供溯源"。不删内容（有价值的踩坑记录），但诚实标注其性质。
- 关键决策（占位符格式、流式通道缓冲、fail-closed、不模糊匹配）已抽进笔记，05 可在每节末尾链对应笔记。

### 4. `docs/脱敏开发/07-日志详情命中明细.md` —— 待实施提案
- 已写对应 `proposed/feature/2026-09-15-log-detail-mask-match-detail.md` 笔记。
- **建议**：07 顶部加 blockquote 链到 proposed 笔记，标注"本文为该提案的详细实施方案，决策状态见笔记"。内容（现状/方案/测试/安全边界/影响文件/风险）是提案细节，可保留。

### 5. `docs/脱敏开发/01-架构与插入点.md`、`03-流式还原设计.md`、`04-配置与前端.md` —— 事实为主，轻轸
这三篇主要是"怎么跑"的事实。只需把顶部"状态：已实现（…）本文档为设计参考"的历史框改为现在时陈述（如"本文档描述 … 的当前实现"），去掉"已实现/设计参考"的过去式框。内容不动。

### 6. 收尾
- 跑 `npx tsx scripts/agent-notes/verify-agent-note-*.ts` 三道门禁确认全绿（新增 3 篇笔记未跑过门禁）。
- 检查 docs 内新增的笔记链接路径是否都有效。
- 提交推送。
