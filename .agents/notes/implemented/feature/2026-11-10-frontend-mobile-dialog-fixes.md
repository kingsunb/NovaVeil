# Agent Note: 前端移动端弹窗与渠道卡片可用性修复

Status: implemented

## 问题

窄屏（320px）与短视口下存在多处阻断性布局与语义问题。居中 `Dialog` 只有 `max-w-lg`，没有视口宽度与高度约束，短视口时标题和底部按钮被裁掉且无法滚动到；`Channels` 把整张渠道卡设成 `role="button"`，内部又嵌 `Switch` 与四个按钮，键盘 Space 会同时触发开关和编辑器，嵌套交互控件也违反 ARIA；渠道编辑器的模型行操作按钮在 320px 下溢出裁切，展开配置固定两列进一步挤压；`Dashboard` 的 5 档时间分段控件在窄屏撑破卡片头，6 张 KPI 卡各自重复同一档位提示；图表图例与折线配色不一致（图例 `#60A5FA`/`#34D399`，线条 `#6D5AE6`/`#2FAE86`），Token 构成图在 320px 仍强制横排。

## 决定

- `web-next/src/components/ui/dialog.tsx`：居中 `dialog` 变体增加 `w-[calc(100vw-2rem)]`、`max-h-[min(90dvh,calc(100dvh-2rem))]`、`overflow-hidden`、`flex flex-col`，`DialogBody` 保持 `flex-1 overflow-y-auto`，`DialogFooter` 加 `shrink-0`；`wide` 变体改 `w-full sm:w-4/5`，移动端全宽、桌面仍靠右占八成。`sheet` 与 `fullscreen` 类名不变。
- `web-next/src/pages/Channels.tsx`：渠道卡恢复为纯展示 `<article>`，删除整卡的 `role`、`tabIndex`、`onClick`、`onKeyDown`；渠道名改为独立 `<button>`，保留 `aria-label="编辑渠道 <名称>"` 与 `aria-haspopup="dialog"`；`Switch` 增加 `aria-label="切换 <名称> 启用状态"`，与底部操作按钮各自聚焦，不再需要 `stopPropagation`。
- `web-next/src/pages/channels/channel-editor.tsx`：模型行改 `flex flex-wrap`，操作区加 `ml-auto shrink-0`；批量操作工具行允许换行；展开的按模型配置改 `grid-cols-1 sm:grid-cols-2`。
- `web-next/src/pages/Dashboard.tsx`：趋势卡 `CardHeader` 加 `flex-wrap`，分段控件包一层 `max-w-full overflow-x-auto` 横向滚动；KPI 档位提示从 6 张卡片移除，改为 KPI 网格上方唯一一行「统计窗口：…」；图例颜色改用共享常量。
- `web-next/src/components/charts/token-colors.ts`：新增 `TOKEN_IN_COLOR`/`TOKEN_OUT_COLOR`，`TokenBreakdownChart`、`TokenTrendChart` 的折线、hover 圆点、tooltip 图例与 `Dashboard` 图例全部引用同一常量；`TokenBreakdownChart` 根节点改 `flex flex-col items-center gap-6 sm:flex-row`。
- 测试同步：`dialog.test.tsx` 锁定居中弹窗宽度/最大高度/flex 契约与 `wide` 全宽；`Channels.test.tsx` 改为点击渠道名打开编辑器，并新增开关聚焦、Space 切换只发启用请求且不弹编辑器；`Dashboard.test.tsx` 新增档位提示唯一性与图例/折线同色断言。
- 与既有 KPI 决策的关系：KPI 口径仍按 [总览输入/输出 Token 卡片](../bug-fix/2026-11-07-dashboard-token-kpis.md) 的 input/output 语义，本次只改变提示的展示位置与次数。

## 备选方案

- **保留整卡点击，在 Switch 上继续 `stopPropagation`**：能少改模板，但嵌套交互控件与 Space 双触发仍在，读屏软件无法正确区分两个动作；不选。
- **只加一个独立「编辑」按钮、整卡保持纯展示**：底部操作栏已有编辑图标，再加按钮信息重复；渠道名本身就是用户最先点击的目标，改名为按钮更直接。
- **窄屏把 5 档分段控件换成 `Select`**：少占横向空间，但引入第二套交互并隐藏可选项，还要维护两套受控逻辑；先选横向滚动，保持单一控件。
- **KPI 提示只保留在第一张卡或 CSS 隐藏其余**：能消除重复文本，但卡片高度与可读性不对称；改为区域级单行提示。
- **用 `max-h-[90dvh]` 而不取 `min`**：在极短视口下 90dvh 仍可能高于扣除 2rem 后的可用高度；两者取小才能同时满足边距与高度上限。
- **图表颜色改 CSS 变量放进 `index.css`**：与现有设计令牌一致，但 SVG 属性与内联样式读取变量更绕；共享 TypeScript 常量更直接，且不扩大全局令牌面。

## 后果

- **收益**：短视口下的居中弹窗可滚动且不裁切标题/按钮；渠道卡键盘语义合法，开关与编辑互不误触；320px 下模型操作按钮不再溢出，Dashboard 时间筛选与图例可换行/滚动；图例与线条颜色不会再分叉。
- **代价与已知上限**：整卡不再可点，可点击面积缩小到渠道名与操作按钮；`wide` 弹窗在移动端占满宽度，左滑关闭的遮罩区消失；图表配色从「两处硬编码」变为单一常量，未来换主题需改 TS 而不是 CSS 变量；`dialog.test.tsx` 对 Tailwind 任意值类名做契约断言，重构类名时需同步更新。
- **重访信号**：产品要求恢复整卡点击时，应改为在卡片上提供明确的非嵌套编辑入口；图表迁移到 CSS 变量主题时，替换 `token-colors.ts` 并同步 Dashboard 图例。

## 验证

```sh
cd web-next && pnpm test -- Dialog Channels Dashboard TokenTrendChart TokenBreakdownChart
cd web-next && pnpm typecheck && pnpm lint
pnpm verify-notes
```

人工复核：浏览器缩到 320px 宽，打开渠道编辑器确认模型行按钮换行且可点；把窗口高度压到 420px 以下打开任一确认弹窗，确认正文可滚动、底部按钮始终可见。
