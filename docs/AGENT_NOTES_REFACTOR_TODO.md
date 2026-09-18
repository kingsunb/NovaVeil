# docs/ 事实-因果拆分：已完成

> 本文件原为「把 `docs/` 大设计文档按事实/因果拆分」的进度追踪。拆分工作已完成，本文改为收口记录，不再追踪待办。
> 原则：`docs/` 只留「现在怎么跑」的现在时事实；决策理由/备选/历史迁到 `.agents/notes/`。

## 已完成

| 文档 | 做了什么 |
|---|---|
| `docs/CHANNEL_PASSTHROUGH.md` | 全文重构为事实态：剥背景/目标、参考实现与 diff 历史；留机制/支持 handler 表/Header 透传/调用链路/边界。因果链到 `implemented/feature/2026-09-11-complete-channel-passthrough.md`。 |
| `docs/DEVELOPMENT_routing.md` | 剥「旧行为对照」「设计依据」等理由段；配置表去「本次开发新增」标注。因果链到 `implemented/architecture/2026-09-11-session-affinity-circuit-breaker-routing.md`。 |
| `docs/脱敏开发/07-日志详情命中明细.md` | 状态置「已实施」；命中明细下发有界原文片段（label + original + placeholder，受 128 条 / 32KB 上限与截断标记约束）。决策见 `implemented/feature/2026-09-15-log-detail-mask-match-detail.md` 与其翻转篇 `implemented/feature/2026-09-17-mask-match-original-display.md`。 |
| `docs/脱敏开发/05`、`06` | 顶部标注为「maskit 借鉴/参考材料（因果）」，当前事实与决策见 agent notes；保留溯源。 |
| Agent Notes | `implemented/` 树 10 篇；`proposed/feature/2026-09-15-log-detail-mask-match-detail.md` 已迁至 `implemented/feature/`；`pnpm verify-notes` 全绿。 |

## 历史

拆分进度与逐项理由的原始记录见 git 历史（本文件 2026-09-16 初版）。当前以 `docs/README.md` 索引 + `.agents/notes/` 为权威入口，不再维护本清单。