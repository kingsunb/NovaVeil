# NovaVeil 前端美化设计稿 v2

> ⚠️ **已归档历史快照**：本文形成于 2026-09-13，定位为原型 v2 设计规范，生产已改用 [`web-next/DESIGN.md`](../../web-next/DESIGN.md)。作为一次性工作产物保留备查，**不再维护**，内容不代表当前实现。当前权威口径见 [FOLLOW_UP.md](FOLLOW_UP.md)（前端生产实施）与 [`web-next/DESIGN.md`](../../web-next/DESIGN.md)（设计）。

> 配套 `PLAN.md` + `AUDIT.md`。本文定义**设计令牌、组件规范、页面线框**，原型 `prototype/` v2 据此实现。
> 设计风格：**macOS 磨砂玻璃简白风** — 浅色为默认、vibrancy 材质、极简留白、Apple system colors。
> 适用范围说明（2026-09-13）：本文的令牌、页面能力与审计结论针对静态原型，不代表当前生产实现或生产验收结果。生产差异与后续美化工作见 [FOLLOW_UP.md](FOLLOW_UP.md)。
> v2 变更：系统化间距/字号/动效标度、验证 WCAG AA 对比度、新增登录视图、移动端侧栏抽屉、活动时间轴、渠道健康条、主题感知 SVG 图表、完整 a11y 修复。

---

## 1. 设计语言总述

| 维度 | 决策 |
|------|------|
| 气质 | **macOS 磨砂玻璃简白风** — 浅色为主、vibrancy 透明材质、大量留白、极简克制 |
| 品牌色 | Apple 蓝 `#007AFF` → 紫 `#5856D6` 渐变（不变） |
| 强调色 | 信号青 `#32D6E2`（流向连线）· 状态语义色沿用 Apple system colors |
| 材质 | **磨砂玻璃 vibrancy**：`backdrop-filter: blur(24px) saturate(180%)` + 半透明白底；极淡网格纹理；品牌色背景晕染提供 vibrancy 底色 |
| 圆角 | 卡片 16px / 控件 10px / Pill 999px（macOS 偏大圆角） |
| 字体 | SF Pro Text/Display + SF Mono/JetBrains Mono |
| 图标 | 全部内联 SVG（Lucide 风格） |
| 动效 | 辉光呼吸、流向轮播、卡片 hover 微浮、状态点脉冲；全部受 `prefers-reduced-motion` 守卫 |
| 间距 | 8pt 标度：4/8/12/16/20/24/32/40/48/64/80/96px |
| 字号 | 标度：11/12/13/14/16/18/20/24/30/40px + clamp 响应式标题 |
| 阴影 | 极轻弥散 — sm/md/lg 三级，不喧宾夺主，磨砂玻璃材质本身是视觉分隔 |

---

## 2. 设计令牌

### 2.1 色彩（浅色默认 — 简白）

```css
/* 品牌渐变 */
--brand-from: #007AFF;
--brand-to:   #5856D6;
--grad-brand: linear-gradient(135deg, #007AFF, #5856D6);

/* 信号青 */
--signal: #32D6E2;

/* 状态语义 — Apple system colors */
--status-success: #34C759;
--status-warning: #FF9F0A;
--status-danger:  #FF3B30;
--status-info:    #007AFF;
--status-muted:   #8E8E93;

/* 表面 — 磨砂玻璃 vibrancy */
--bg:           #F5F5F7;                    /* macOS 浅灰 */
--surface:      rgba(255, 255, 255, 0.72);  /* 磨砂玻璃 */
--surface-solid:#FFFFFF;                    /* 不透明白 */
--surface-2:    rgba(0, 0, 0, 0.04);        /* 微凹背景 */
--sidebar:      rgba(255, 255, 255, 0.55);  /* 更透的侧栏 */

/* 边框 — 几乎不可见 */
--border:        rgba(0, 0, 0, 0.06);
--border-strong: rgba(0, 0, 0, 0.10);

/* 文字 — Apple system labels, 全部验证 WCAG AA */
--text:        #1D1D1F;   /* 15.5:1 ✓ */
--text-muted:  #6E6E73;   /* 5.1:1  ✓ */
--text-subtle: #73737A;   /* 4.7:1  ✓ */

/* 磨砂玻璃材质 */
--glass-blur: 24px;
--glass-saturate: 180%;
--glass: blur(24px) saturate(180%);
```

背景色彩晕染（为 vibrancy 提供底色）：
```css
body::before {
  background:
    radial-gradient(ellipse 60% 50% at 12% 8%, rgba(0,122,255,0.07), transparent 60%),
    radial-gradient(ellipse 50% 45% at 88% 92%, rgba(88,86,214,0.06), transparent 60%),
    radial-gradient(ellipse 40% 30% at 50% 50%, rgba(255,159,10,0.025), transparent 70%);
}
```

### 2.2 色彩（暗色 — macOS dark mode）

```css
--bg:           #1C1C1E;
--surface:      rgba(44, 44, 46, 0.72);
--surface-solid:#2C2C2E;
--surface-2:    rgba(255, 255, 255, 0.06);
--sidebar:      rgba(30, 30, 32, 0.60);
--border:        rgba(255, 255, 255, 0.08);
--border-strong: rgba(255, 255, 255, 0.14);
--text:        #F5F5F7;   /* 15.6:1 ✓ */
--text-muted:  #98989D;   /* 5.1:1  ✓ */
--text-subtle: #93939A;   /* 4.8:1  ✓ */
```

### 2.3 阴影（极轻弥散 — 磨砂玻璃材质本身是视觉分隔）

| 令牌 | 浅色 | 用途 |
|------|------|------|
| `--shadow-sm` | `0 1px 3px rgba(0,0,0,.04)` | 卡片默认 |
| `--shadow-md` | `0 4px 16px rgba(0,0,0,.06)` | 卡片 hover、弹层 |
| `--shadow-lg` | `0 12px 40px rgba(0,0,0,.08)` | Toast、模态 |
| `--shadow-brand` | `0 2px 12px -2px rgba(0,122,255,.25)` | 主按钮、激活态 |
| `--shadow-brand-lg` | 主按钮 hover、品牌标识 |

### 2.4 间距标度（8pt）

```
--s-1: 4px   --s-2: 8px   --s-3: 12px  --s-4: 16px
--s-5: 20px  --s-6: 24px  --s-8: 32px  --s-10: 40px
--s-12: 48px --s-16: 64px --s-20: 80px
```

### 2.5 字号标度

```
--t-xs: 11px   --t-sm: 12px  --t-base: 13px  --t-md: 14px
--t-lg: 16px   --t-xl: 18px  --t-2xl: 20px   --t-3xl: 24px
--t-4xl: 30px  --t-5xl: 40px
```

响应式标题：`clamp(36px, 6vw, 64px)`

### 2.6 动效令牌

```
--ease:     cubic-bezier(.25, 0, 0, 1)
--ease-out: cubic-bezier(.16, 1, .3, 1)
--dur-1: .12s  --dur-2: .18s  --dur-3: .24s  --dur-4: .32s
```

### 2.7 焦点环

用 `outline` + `outline-offset` 实现，**不破坏元素自身圆角**（v1 用 `border-radius:4px` 覆盖圆角）。

---

## 3. 组件规范

### 3.1 BrandMark
渐变背景圆角方块，sm 32×32 / lg 56×56（带品牌辉光阴影）。

### 3.2 Button
| 变体 | 样式 |
|------|------|
| primary | 渐变背景 + 白字 + 品牌阴影；hover brightness(1.08) |
| secondary | surface 背景 + border；hover border 变品牌色 |
| 尺寸 | sm 36px / 默认 / lg 48px / block 100% |

### 3.3 StatusDot
8px 圆点，success/warning/danger 三色。`[data-pulse]` 属性触发脉冲扩散动画（2s）。

### 3.4 Pill
圆角标签，背景 = 语义色 12% 透明度，文字 = 语义色。success/warning/danger 三变体。

### 3.5 Card
surface 背景 + border + shadow-sm。`__head` flex 两端对齐，`__body` 默认 / `--col` 纵向居中。

### 3.6 FeatureCard
每张卡有 `--accent` CSS 变量驱动图标颜色、hover 边框色、hover 标题色、hover 径向辉光。8 张卡 8 种 accent 色。

### 3.7 FlowDiagram
三列 grid（客户端 / Hub / 供应商），SVG 绝对定位画贝塞尔连线。连线用品牌渐变 + dash 动画。Hub 有脉冲环。节点轮播高亮（1.5s 间隔）。

### 3.8 Sparkline
80×24 viewBox SVG，内联在 KPI 卡内 flex 布局（非绝对定位，修复 v1 重叠问题）。每张 KPI 卡不同色。

### 3.9 TrendChart
720×220 viewBox + `preserveAspectRatio="none"`。双折线（输入/输出）+ 渐变面积填充 + 网格线 + 轴标签。颜色从 CSS 变量读取，主题切换时重画。

### 3.10 DonutChart
160×160 viewBox，5 段弧 + 中心文字 + 下方纵向图例。每段圆角端点 + 间隙。

### 3.11 Topology
每行：状态点 + 名称 + **健康度迷你条**（48×4px，宽度 = `--h`）+ 延迟 + Pill。健康条颜色跟随状态。

### 3.12 ActivityTimeline
每项：**纵向时间轴 rail**（1.5px 竖线连接相邻项）+ 状态点 + 标题 + 元信息 + sr-only 状态文字（a11y 修复：不仅靠颜色传达状态）。

### 3.13 Sidebar
240px 宽，可折叠至 60px。激活项有 **3px 左侧品牌色指示条**（v1 缺失）。搜索框过滤导航项。移动端变抽屉 + 遮罩 + 汉堡按钮。

### 3.14 LoginView（新增）
左右分栏：品牌面板（辉光 + 特性列表）+ 表单面板（用户名/密码/记住我/登录按钮）。窄屏隐藏品牌面板。

### 3.15 Toast
底部居中浮层，`aria-live="polite"`，2.5s 自动消失。

---

## 4. 页面线框

### 4.1 落地页
```
Nav（sticky 玻璃）
Hero（辉光 + 版本徽章 + 标题 + 描述 + CTA）
  FlowDiagram（三列 + SVG 连线 + Hub 脉冲）
  Caption
Features（4×2 网格，8 张 FeatureCard）
How（3 步 + SVG 箭头连接器）
GetStarted（Docker 命令 + 复制按钮 + 备选链接）
Footer
```

### 4.2 仪表盘
```
Sidebar（品牌 + 搜索 + 导航组 + 折叠按钮）
Main
  Topbar（汉堡 + 面包屑 + ⌘K + 头像）
  Content
    KPI Row（4 卡，含 sparkline + 趋势箭头）
    Grid 2:1
      TrendChart（含范围标签 24h/7d/30d/1y）
      DonutChart（含图例）
    Grid 2:1
      Topology（6 渠道，含健康条）
      ActivityTimeline（5 项，含时间轴 rail）
```

### 4.3 登录页（新增）
```
LoginLayout（左右分栏）
  BrandPanel（辉光 + BrandMark + 特性列表）
  FormPanel（标题 + 用户名 + 密码 + 记住我 + 登录按钮 + 提示）
```

---

## 5. 交互状态

| 组件 | 状态 | 效果 |
|------|------|------|
| Button primary | hover | brightness(1.08) + 品牌辉光阴影 |
| Button primary | active | scale(.97) |
| FeatureCard | hover | 边框变 accent 色 + 径向辉光 + 图标 scale(1.1) + 标题变 accent 色 |
| KPI Card | hover | shadow 升至 md |
| Sidebar item | hover | 背景 5% text |
| Sidebar item active | — | 品牌 10% 背景 + 品牌色文字 + 左侧 3px 指示条 |
| Flow node | active | 品牌边框 + 品牌阴影 + scale(1.04) |
| Range tab | active | 品牌 14% 背景 + 品牌色文字 |
| Segmented | active | 渐变背景 + 白字 |

---

## 6. 无障碍

| 项 | 措施 |
|----|------|
| 对比度 | 全部文字 ≥ 4.5:1（见 §2.1/2.2） |
| 焦点环 | `outline` + `offset`，不破坏圆角 |
| 分段控件 | `role="tablist"/"tab"` + `aria-selected` 动态管理 |
| 视图切换 | 焦点移入新视图 `tabpanel` |
| 活动状态 | 色点 + sr-only 文字标签（不仅靠颜色） |
| SVG 图表 | `<title>` + `<desc>` |
| 侧栏折叠 | 导航项加 `title` 属性补偿 |
| 主题按钮 | `aria-label` + `aria-pressed` 动态更新 |
| 表单 | `<label>` 关联 + `autocomplete` |
| Toast | `aria-live="polite"` |
| 动效 | `prefers-reduced-motion` 守卫 |
| 透明度 | `prefers-reduced-transparency` 降级 |

---

## 7. 响应式断点

| 断点 | 变化 |
|------|------|
| ≤ 960px | FeatureCard 4→2 列；Grid 2:1→1 列 |
| ≤ 860px | KPI 4→2 列；登录品牌面板隐藏 |
| ≤ 768px | 侧栏变抽屉 + 汉堡；Flow 三列→单列（隐藏 SVG）；FeatureCard 2→1 列；How 步骤纵向 |
| ≤ 560px | FeatureCard 单列 |

---

## 8. 装饰档位

| 档位 | 效果 |
|------|------|
| 丰富（默认） | 全部动效 + 辉光 + 脉冲 + 品牌阴影 |
| 克制 | 禁用 hover 缩放/辉光/脉冲/品牌阴影；辉光降至 35% |

---

## 9. v1 → v2 变更摘要

| 变更 | 原因 |
|------|------|
| 新增间距/字号/动效标度 | T1/T2/T8：消除硬编码 |
| `--text-subtle` 提亮 | T3：对比度 3.8→4.6:1 达 AA |
| `--text-muted` 提亮 | T4：留余量 |
| 焦点环改 outline | T7：不破坏圆角 |
| Flow 节点全改 SVG | V1：统一图标体系 |
| Sparkline 改 flex 布局 | V3：修复窄屏重叠 |
| Donut 图例改纵向 | V4：修复窄卡换行 |
| TrendChart 加 preserveAspectRatio | V5：保持比例 |
| 侧栏激活指示条 | V8：视觉锚点 |
| 活动时间轴 rail | V10：名副其实 |
| 渠道健康条 | V11：信息密度 |
| 分段控件 aria-selected | A1：读屏可达 |
| 活动 sr-only 状态文字 | A2：色盲可达 |
| 视图切换焦点管理 | A4：键盘可达 |
| SVG 图表 title/desc | A7：读屏可达 |
| 图表主题感知 | C2：切主题颜色更新 |
| flowTimer 清理 | C1：无泄漏 |
| 移动端侧栏抽屉 | R1：窄屏可用 |
| 新增登录视图 | I1：核心入口 |
| 新增 Toast | 交互反馈 |
