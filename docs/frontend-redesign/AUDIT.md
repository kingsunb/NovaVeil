# NovaVeil 前端原型 · 审计报告

> ⚠️ **已归档历史快照**：本文形成于 2026-09，定位为 v1 原型审计，已被 `AUDIT-v2.md`（v2 原型审计）取代。作为一次性工作产物保留备查，**不再维护**，内容不代表当前实现。当前权威口径见 [FOLLOW_UP.md](FOLLOW_UP.md)（前端生产实施）与 [`web-next/DESIGN.md`](../../web-next/DESIGN.md)（设计）。

> 审计对象：`docs/frontend-redesign/prototype/`（v1 原型）
> 审计维度：视觉一致性、设计令牌、无障碍、响应式、代码质量、信息完整性
> 每条标注严重度：🔴 高 / 🟡 中 / 🟢 低

---

## 1. 设计令牌

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| T1 | **无间距标度**——各处混用 3/4/5/6/8/10/12/14/16/18/20/24/28/32/40px，无系统化 scale | 🔴 | 全局 |
| T2 | **无字号标度**——hero 用 clamp、其余硬编码 11/12/13/14/15/16/18/24/28/30px，缺 type scale | 🔴 | 全局 |
| T3 | `--text-subtle: #6B6B78` 对 `--surface: #16161A` 对比度 ≈ 3.8:1，**不达 WCAG AA 4.5:1** | 🔴 | 暗色次要文案 |
| T4 | `--text-muted: #9A9AA6` 对 `--surface` ≈ 5.2:1 达标，但亮色 `#6B6B78` 对 `#FFFFFF` ≈ 4.7:1 勉强达标，边界值应留余量 | 🟡 | 亮色次要文案 |
| T5 | 信号青 `--signal` 定义但**从未使用** | 🟢 | styles.css |
| T6 | `--glass-alpha` 定义但从未在样式中引用（玻璃效果靠 backdrop-filter + color-mix） | 🟢 | styles.css |
| T7 | 焦点环 `--focus-ring` 全局应用，但 `border-radius: 4px` 会**覆盖元素自身圆角** | 🟡 | :focus-visible |
| T8 | 无动效令牌（duration/easing 散落各处：.15s/.18s/.2s/.3s + 各种 cubic-bezier） | 🟡 | 全局 |

## 2. 视觉一致性

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| V1 | **流向动画节点图标不统一**——客户端用文字字符（⌘ ↗ ▸ ⊕），供应商用色点，其余 UI 用 SVG。三套图标体系混搭 | 🔴 | flow__node |
| V2 | 流向 SVG 绝对定位覆盖整个 `.flow`，但节点在 grid 单元格内，**连线起止点依赖 getBoundingClientRect**，字体加载/滚动后尺寸漂移会导致连线错位 | 🟡 | drawFlow() |
| V3 | KPI 卡 sparkline 绝对定位 `right:16px;bottom:14px`，窄屏时**与数值文字重叠** | 🟡 | .kpi-card |
| V4 | 甜甜圈卡 `.card__body--center` 用 flex 居中，但卡在 1fr 列里很窄，donut(140px)+legend **横向放不下会换行**，换行后视觉松散 | 🟡 | donut |
| V5 | 趋势图 viewBox 720×220 + `width:100%`，宽屏拉伸时**折线变扁**，失去纵向比例 | 🟡 | .trendchart |
| V6 | 特性卡 hover 有 `box-shadow:md` + 图标 `scale(1.1)`，但**无过渡延迟**，快速划过时动画抖动 | 🟢 | .feature-card |
| V7 | "工作原理"步骤间的 `→` 箭头字符在不同字体下渲染宽度不一，**对齐不稳定** | 🟢 | .step__arrow |
| V8 | 侧栏激活项用 `background: brand 10%`，但**无左侧激活指示条**，视觉锚点弱 | 🟡 | .sb__item.is-active |
| V9 | 顶栏 `.cmdk` 按钮和头像之间**无分隔**，视觉粘连 | 🟢 | .topbar__right |
| V10 | 活动时间线是**平铺列表**，无时间轴纵线，"时间线"名不副实 | 🟡 | .activity |
| V11 | 渠道拓扑每行只有文字+pill，**无健康度可视化**（进度条/比例），信息密度低 | 🟡 | .topology |
| V12 | 落地页 nav `top:52px` 硬编码跟随 proto-bar 高度，**脆弱耦合** | 🟢 | .landing-nav |

## 3. 无障碍 (A11y)

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| A1 | 分段控件有 `role="tablist"/"tab"` 但**未管理 `aria-selected`**，读屏无法获知选中态 | 🔴 | .seg__btn |
| A2 | 活动时间线**仅靠色点传达状态**，无文字标签，色盲用户无法区分成功/失败/熔断 | 🔴 | .activity__item |
| A3 | 主题切换按钮无 `aria-pressed` / `aria-label` 动态更新（label 固定"切换主题"） | 🟡 | #themeToggle |
| A4 | 视图切换后**焦点未移入新视图**，键盘用户 Tab 会跳回 proto-bar | 🟡 | view switch |
| A5 | 侧栏折叠后导航项文字 `display:none`，但**无 tooltip/aria-label 补偿**，折叠态读屏丢失项名 | 🟡 | .sb.is-collapsed |
| A6 | 流向动画 `aria-hidden="true"` 正确，但其传达的"聚合"信息**无文字冗余**（下方无说明） | 🟢 | .flow |
| A7 | SVG 图表（trend/donut）**无 `<title>`/`<desc>`**，读屏跳过全部数据 | 🟡 | charts |
| A8 | `color-mix()` 用于玻璃背景，旧浏览器（<2023）**无 fallback** | 🟢 | backdrop |

## 4. 响应式

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| R1 | 仪表盘侧栏**无移动端抽屉**——窄屏直接消失（`display:none` by collapse），无汉堡菜单入口 | 🔴 | dashboard <720px |
| R2 | KPI 四列在 860px 降为两列，但**两列时 sparkline 仍绝对定位**，重叠加剧 | 🟡 | .kpi-row |
| R3 | 流向动画 320px 固定高度 + 三列 grid，**720px 以下未适配**，节点挤压 | 🟡 | .flow |
| R4 | 顶栏 `.topbar__right` 元素在窄屏**不换行不隐藏**，溢出 | 🟡 | .topbar |
| R5 | 代码块 `white-space` 未控制，长命令在窄屏**水平溢出**（有 overflow-x:auto 但无视觉提示） | 🟢 | .codeblock |

## 5. 代码质量

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| C1 | `flowTimer` interval 在**切走落地页时未清除**，后台持续运行 | 🟡 | view switch |
| C2 | 趋势/甜甜圈用 `dataset.drawn` 防重绘，但**切主题后颜色不更新**（stroke 硬编码） | 🔴 | drawTrend/drawDonut |
| C3 | resize 只重画 flow，**不重画 trend/donut**（容器宽度变了图不适应） | 🟡 | resize handler |
| C4 | `drawFlow` 用 `svg.innerHTML=""` 清空，再 `createElementNS` 混用 innerHTML 设 defs——**命名空间不一致**（innerHTML 不自动带 SVG ns） | 🟡 | drawFlow defs |
| C5 | `navigator.clipboard?.writeText()` 无 fallback，HTTP 下 clipboard API 不可用 | 🟢 | copyBtn |
| C6 | 无 `requestAnimationFrame` 节流 resize，虽有 setTimeout debounce 但可更规范 | 🟢 | resize |

## 6. 信息完整性

| # | 问题 | 严重度 | 位置 |
|---|------|--------|------|
| I1 | **无登录页视图**——NovaVeil 核心入口之一，原型缺失 | 🟡 | 缺失 |
| I2 | 仪表盘**无空状态/加载骨架**，真实数据未就绪时无反馈 | 🟡 | 缺失 |
| I3 | 仪表盘**无面包屑/页面标题层级**，OmniRoute 有 breadcrumbs | 🟢 | .topbar |
| I4 | 落地页"快速开始"只有 Docker 命令，**无二进制/源码构建**两种方式 | 🟢 | .getstarted |
| I5 | 特性卡**无"了解更多"链接**，无法深入 | 🟢 | .feature-card |

---

## 7. 修复计划

上述问题在 v2 重写中**全部修复**，具体映射：

| 审计项 | 修复方式 |
|--------|---------|
| T1-T2 | 新增 `--space-*` 8pt 标度 + `--text-*` 字号标度，全局替换硬编码值 |
| T3-T4 | 暗色 `--text-subtle` 提亮至 `#84849A`(4.6:1)，亮色提深至 `#5F5F6B`(5.3:1) |
| T5-T6 | 信号青用于流向连线；glass-alpha 用于 fallback 背景色 |
| T7 | 焦点环去掉 `border-radius:4px`，改用 `outline-offset` 不破坏元素圆角 |
| T8 | 新增 `--ease/--dur-*` 动效令牌 |
| V1 | 流向节点全部改用 SVG 图标 + 统一容器 |
| V2 | drawFlow 用 ResizeObserver + rAF 重画，字体加载后重算 |
| V3 | sparkline 改为 KPI 卡内 flex 布局，不绝对定位 |
| V4 | 甜甜圈图 + 图例改为纵向居中 |
| V5 | 趋势图用 preserveAspectRatio + 响应式 viewBox |
| V8 | 侧栏激活项加 3px 左侧品牌色指示条 |
| V10 | 活动列表加纵向时间轴 rail + 状态文字标签 |
| V11 | 拓扑每行加健康度迷你条 |
| A1 | 分段控件管理 `aria-selected` + `tabindex` |
| A2 | 活动项加 sr-only 状态文字 |
| A4 | 视图切换后 focus 移入视图容器 |
| A5 | 折叠态导航项加 `aria-label` + title |
| A7 | SVG 图表加 `<title>/<desc>` |
| C1 | 切走视图时 clearInterval(flowTimer) |
| C2 | 图表颜色改用 CSS 变量 / 主题切换时重画 |
| C3 | resize 重画所有可见图表 |
| C4 | defs 用 createElementNS 创建子元素 |
| R1 | 窄屏侧栏改抽屉 + 汉堡按钮 |
| I1 | 新增登录视图 |
| I2 | KPI/卡片加骨架占位态 |
