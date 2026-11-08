# Agent Note: 总览用输入/输出 Token 替代不可用指标

Status: implemented

## 问题

总览顶部的“预计消耗”和“使用时长”依赖 `total_cost` 与 `total_duration_ms`。OpenCode 免费等上游不返回可靠成本，现有 24 小时数据里两个值都是 0，页面只能显示“—”；“Token 构成”同时展示 reasoning 与缓存命中，数据源没有这两类明细时恒为 0，显示 0.0% 会让用户误以为统计正确。

## 决定

- 6 个 KPI 中，把“预计消耗”“使用时长”替换为“输入 Token”“输出 Token”，数值分别取 `total_tokens_input`、`total_tokens_output`，使用 compact 格式，hint 仍随趋势档位联动。
- `TokenBreakdownChart` 只接收 `input` 和 `output` 两类，图例、环形弧、总 tokens 与占比都只按这两类计算；`Dashboard` 的描述改为 `input/output`。
- 单一非零类别用两段半圆弧绘制完整圆环，避免 SVG 起止点重合时不显示。
- `NowVersion.stats_available` 保留聚合可读状态；旧响应缺字段时按可用兼容，非对象响应或显式非法状态标记为不可用。聚合不可用时 KPI、Token 构成与模型 Top 显示不可用状态，不把失败误报为零用量。
- API 归一化与图表入口将负数、非有限值和非数字 Token 归零，避免负占比和无效弧线。
- 后端 `total_cost`、`total_duration_ms`、`reasoning_tokens`、`cached_tokens` 字段保留，不伪造或补零；前端不再展示不可确认的统计。

## 备选方案

- **用模型价格估算成本**：仓库没有可靠的多模型价格与缓存折扣来源，估算值会被误当成真实账单；不选。
- **继续显示使用时长并补数据**：当前数据为 0，无法确认所有请求都有 duration；用户明确允许改成输入/输出，先不展示不可确认值。
- **保留 reasoning/cache 但隐藏 0**：数据源不保证有值，隐藏 0 仍会在有部分数据时产生误导；直接移除这两个展示项。

## 后果

- **收益**：总览卡片和 Token 构成只展示当前数据源能确认的 input/output，不再出现固定 0.0% 的假统计。
- **代价与已知上限**：成本与时长不再出现在总览；以后接入可靠的成本/耗时统计时，需要重新评估是否恢复卡片。极端 Token 比值可能受 JavaScript 浮点精度限制，当前实际用量下不增加高精度处理。

## 验证

```sh
cd web-next && pnpm test -- Dashboard TokenBreakdownChart api api.endpoint
cd web-next && pnpm typecheck
pnpm verify-notes
```
