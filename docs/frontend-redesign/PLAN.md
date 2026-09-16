# NovaVeil 前端美化规划

> 参考对象：[OmniRoute](https://github.com/diegosouzapw/OmniRoute)（Next.js 16 + Tailwind v4 + React 19）
> 本项目：NovaVeil（Vite + React 18 + Tailwind v3 + Radix UI）
> 产出位置：`docs/frontend-redesign/`（原型，不改动生产代码）
>
> 阶段说明（2026-09-13）：本文保留原型阶段的背景、技术栈快照与约束，不代表当前生产前端进度。`web-next/` 的已有美化实现、与原型的差异、后续优先级和验收清单见 [FOLLOW_UP.md](FOLLOW_UP.md)。
> 生产进度核对（2026-09-16）：列表编辑缓存回填、模型评估及分组排序、渠道模型批量操作的源码现状见 `FOLLOW_UP.md` §3.4；路由性能仍有未实施项，见 [ROUTE_TRANSITION_PERFORMANCE_PLAN.md](ROUTE_TRANSITION_PERFORMANCE_PLAN.md)。以下原型快照与历史约束不作追溯改写，源码存在不等于已验收。

---

## 1. 背景与目标

NovaVeil 当前的 `web-next` 前端已经有一套相当完整的「macOS 磨砂玻璃」设计系统
（Apple 蓝主色、SF Pro 字体、glass-panel 工具类、WCAG AA 对比度、reduced-motion 守卫），
整体质量不错。但与 OmniRoute 这类成熟网关控制台相比，仍有几个明显差距：

| 维度 | NovaVeil 现状 | OmniRoute 参考 | 差距 |
|------|--------------|---------------|------|
| 公开落地页 | ❌ 无（直接跳登录） | ✅ Hero + 特性 + 流向动画 + 步骤 + 页脚 | 缺少产品门面 |
| 仪表盘信息密度 | 4 KPI + 趋势图 + 模型 Top + 近期错误 | 拓扑图 + 配额 + 用量甜甜圈 + 状态点 + 时间线 | 可视化偏单薄 |
| 视觉「惊艳感」 | 克制、偏工具向 | 辉光、脉冲、品牌渐变、流向动画 | 缺少记忆点 |
| 状态语义可视化 | Pill 圆点 | 流量灯色 + 编排状态令牌 + StatusDot 组件 | 可统一增强 |
| 侧栏能力 | 两组 + 折叠 | 搜索 + 置顶 + 拖拽排序 + 图标配色 | 体验可升级 |

**目标**：在不破坏 NovaVeil 既有品牌（Apple 蓝紫渐变、磨砂玻璃）的前提下，
借鉴 OmniRoute 的**视觉技法**与**信息架构**，产出一份可评审的原型，验证美化方向后再决定是否落地。

**约束**：
- 只在 `docs/frontend-redesign/` 下产出，**不改动 `web-next/` 生产代码**。
- 原型为自包含 HTML/CSS/JS，浏览器直接打开即可预览，无需构建。
- 保留 NovaVeil 品牌色（蓝紫渐变），不照搬 OmniRoute 的珊瑚红。

---

## 2. 从 OmniRoute 借鉴的具体点

### 2.1 落地页（Landing）—— NovaVeil 目前完全没有

OmniRoute 的 `src/app/landing/` 由六个组件构成：

- **HeroSection**：版本徽标（脉冲圆点）+ 大标题（品牌色高亮词）+ 描述 + 双 CTA + 顶部辉光 blur。
- **Features**：4 列特性卡，**每张卡有独立配色**（蓝/橙/玫/紫/琥珀/天蓝/翠/紫红），hover 时边框、底色、图标缩放联动。
- **FlowAnimation**：左侧 CLI 工具 → 中心 Hub（脉冲环）→ 右侧供应商，SVG 连线 + 2s 轮播高亮，**直观传达「聚合」价值**。这条对 NovaVeil（同样是聚合网关）几乎可以原样映射。
- **HowItWorks**：3 步骤卡。
- **GetStarted**：代码块 + 复制按钮。
- **Footer**：链接 + 版权。

→ **NovaVeil 可新增一个公开落地页**，用蓝紫渐变替换珊瑚红，复用流向动画讲「客户端 → NovaVeil → 多供应商」。

### 2.2 仪表盘增强

OmniRoute `home/` 页面有：
- `ProviderTopology`：供应商拓扑可视化（可用/不可用状态点）。
- `ProviderQuotaWidget`：配额进度环。
- `HomeRecentRequests`：最近请求时间线。
- analytics 模块：recharts 甜甜圈图 + 用量柱图 + 时间范围选择器。

→ **NovaVeil Dashboard 可增加**：
1. **渠道健康拓扑**：把现有「渠道」页的数据做成迷你拓扑卡，状态点（绿=可用/黄=半开/红=熔断）。
2. **模型分布甜甜圈**：现有「模型 Top」是表格，可加一个 SVG 甜甜圈图作为视觉摘要。
3. **KPI 卡内嵌迷你趋势**（sparkline）：OmniRoute 的 KPI 偏纯数字，NovaVeil 可更进一步。
4. **最近请求时间线**：现有「近期错误」可扩展为带状态色的活动流。

### 2.3 设计令牌精修

OmniRoute `globals.css` 有几个值得借鉴的令牌技法：

| 令牌 | OmniRoute 做法 | NovaVeil 现状 | 建议 |
|------|---------------|--------------|------|
| 品牌色暖阴影 | `--shadow-warm: 0 2px 12px -2px rgba(品牌色,0.12)` | 仅有中性 Apple 阴影 | 新增 `shadow-brand`，按钮/激活态用 |
| 网格墙纸可见度 | body::before 固定层，opacity 调高到能看见 | `bg-gradient-subtle` 已有但偏淡 | 适当提高网格对比，让「控制台」感更强 |
| 焦点环 | `0 0 0 2px bg, 0 0 0 4px accent` 双环 | 未显式定义 | 加 `--focus-ring`，键盘导航才显示 |
| 状态令牌 | `--orch-status-success/warning/error/muted` | 散落在 Pill | 统一抽成 CSS 变量 |
| 圆角语义 | `--radius-card:14px / --radius-control:9px` | card 12 / control 8 | 接近，可对齐到 14/9 增加柔和感 |

### 2.4 侧栏与导航

OmniRoute Sidebar 支持：搜索过滤、分组置顶、拖拽排序、每项图标独立配色、折叠态 tooltip。
NovaVeil Sidebar 目前是两组 + 折叠，功能够用但偏简。

→ **原型先不做拖拽**（复杂度高），但展示「搜索框 + 图标微配色 + 状态徽标」的增强态，供评审。

---

## 3. 产出物

| 文件 | 说明 |
|------|------|
| `docs/frontend-redesign/PLAN.md` | 本文档——规划 |
| `docs/frontend-redesign/DESIGN.md` | 设计稿——设计令牌、组件规范、页面线框 |
| `docs/frontend-redesign/prototype/index.html` | 原型入口（含落地页 + 增强仪表盘两个视图，顶部切换） |
| `docs/frontend-redesign/prototype/styles.css` | 原型样式（独立，不依赖 Tailwind） |
| `docs/frontend-redesign/prototype/app.js` | 原型交互（视图切换、流向动画、主题切换、甜甜圈绘制） |

原型用纯 HTML/CSS/JS 实现，刻意不引入构建链，目的是**最快速度让 stakeholder 在浏览器里看到效果**。
若方向获批，再按 `DESIGN.md` 的令牌映射回 `web-next/` 的 Tailwind 体系。

---

## 4. 不做什么（本轮排除项）

- ❌ 不改动 `web-next/src/` 任何生产代码。
- ❌ 不引入新依赖（recharts / xyflow 等）——原型用原生 SVG 画图。
- ❌ 不做拖拽排序、i18n、Electron 适配等 OmniRoute 的高级特性。
- ❌ 不照搬 OmniRoute 的珊瑚红品牌色——NovaVeil 保持蓝紫渐变。
- ❌ 不做响应式断点的完整覆盖——原型以桌面 1440px 为主，兼顾窄屏可读。

---

## 5. 落地路径（原型获批后，仅供参考）

1. **令牌层**：把 `DESIGN.md` §2 的新令牌（`--shadow-brand`、`--focus-ring`、状态变量、圆角 14/9）
   合并进 `web-next/src/index.css` 与 `tailwind.config.ts`。
2. **落地页**：在 `web-next/src/pages/` 新增 `Landing.tsx`，路由 `/landing`（公开），登录页加「了解 NovaVeil」链接。
3. **仪表盘**：在 `Dashboard.tsx` 增加渠道拓扑卡、模型甜甜圈、活动时间线；新增 `components/charts/DonutChart.tsx`。
4. **侧栏**：`Sidebar.tsx` 增加搜索框与图标配色。
5. 每步配套 Vitest + Playwright 回归，保证 a11y 与对比度不退化。

---

## 6. 风险与取舍

- **品牌一致性**：增加辉光/动画可能削弱 NovaVeil「克制工具向」气质。原型会提供「轻装饰」与「重装饰」两档供选。
- **性能**：backdrop-blur 在低端机有成本。NovaVeil 已有 `prefers-reduced-transparency` 降级，沿用即可。
- **信息密度 vs 留白**：OmniRoute 仪表盘很密，NovaVeil 偏留白。原型取中间值，不盲目堆砌。
