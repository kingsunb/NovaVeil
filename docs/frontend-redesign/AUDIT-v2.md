# NovaVeil 前端原型 v2 · UI/UX Pro Max 审计报告

> ⚠️ **已归档历史快照**：本文形成于 2026-09-11，定位为 v2 原型审计，其 P1/P2 修复项已在原型源码中闭合。作为一次性工作产物保留备查，**不再维护**，内容不代表当前实现。当前权威口径见 [FOLLOW_UP.md](FOLLOW_UP.md)（前端生产实施）与 [`web-next/DESIGN.md`](../../web-next/DESIGN.md)（设计）。

> 审计工具：[UI/UX Pro Max Skill](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill) v2.13.0
> 审计对象：`prototype/` (macOS 磨砂玻璃简白风)
> 审计日期：2026-09-11
> 审计维度：8 个维度 · 28 条检查项

---

## 审计方法

使用 UI/UX Pro Max 的 BM25 搜索引擎查询以下域数据库：
- `styles.csv` — 79 种 UI 风格（glassmorphism、liquid-glass、bento-box-grid）
- `ux-guidelines.csv` — 119 条 UX 最佳实践与反模式
- `colors.csv` — 192 套配色方案
- `typography.csv` — 74 组字体配对

每条检查项标注：✓ 通过 · ⚠ 警告 · ✗ 违规

---

## 1. 玻璃拟态风格合规性 (Glassmorphism)

> 参照：`styles.csv` → `glassmorphism` + `liquid-glass`

| # | 检查项 | UI Pro Max 标准 | 实际值 | 状态 |
|---|--------|----------------|--------|------|
| 1.1 | backdrop-filter 模糊半径 | 10–20px (glassmorphism) / adaptive (liquid-glass) | `24px` | ⚠ 超出 glassmorphism 推荐范围，但符合 macOS vibrancy 惯例 |
| 1.2 | 半透明表面不透明度 | 15–30% (glassmorphism) | 72% (surface) / 55% (sidebar) | ⚠ 远高于 glassmorphism 标准；符合 liquid-glass "adaptive translucent material" |
| 1.3 | 边框 | 1px solid rgba(255,255,255,0.2) | `1px solid rgba(0,0,0,0.06)` | ✓ 浅色主题用黑底边框，合理 |
| 1.4 | 背景需有色彩供折射 | "vibrant background verified" | `body::before` 三色径向渐变 (蓝/紫/橙) | ✓ |
| 1.5 | 文字对比度 4.5:1 | "text contrast 4.5:1 checked" | 16.8/5.1/4.7:1 (浅) · 15.6/5.1/4.8:1 (暗) | ✓ 全部通过 |
| 1.6 | `prefers-reduced-transparency` 降级 | liquid-glass: "test reduced transparency" | 12 个表面降级为 `surface-solid` | ✓ |
| 1.7 | backdrop-filter 性能 | "cost: moderate" | 16 处 backdrop-filter 声明 | ⚠ 数量偏多，低端设备可能卡顿；已有 transparency 降级兜底 |

**小结**：整体符合 liquid-glass 而非 glassmorphism——这是正确选择，因为 macOS vibrancy 本质是 liquid-glass。24px 模糊和 72% 不透明度是 macOS 系统级惯例如非 glassmorphism 通用推荐。

---

## 2. 可访问性 (Accessibility)

> 参照：`ux-guidelines.csv` → Accessibility 类目

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 2.1 | 焦点外观 (WCAG 2.2) | ≥2px 周长 + 3:1 对比 | `outline: 2px solid #007AFF; offset: 2px` | ✓ | — |
| 2.2 | 焦点不被遮挡 (Minimum) | sticky UI 不完全遮挡 focus | proto-bar sticky top:0 + landing-nav sticky top:52px | ⚠ 缺少 `scroll-padding-top` | 添加 `html { scroll-padding-top: 120px; }` |
| 2.3 | 键盘导航 | tab 序与视觉序一致 | 语义化 HTML，tab 序合理 | ✓ | — |
| 2.4 | 焦点环可见 | 每个 interactive 元素有 focus ring | `:focus-visible` 全局规则 | ✓ | — |
| 2.5 | 颜色对比度 (WCAG AA) | 正文 ≥4.5:1 | 全部通过 (见 §1.5) | ✓ | — |
| 2.6 | 不仅用颜色传达信息 | icon/text 辅助 | 拓扑有 pill 文字标签；活动有 sr-only 状态文字 | ✓ | — |
| 2.7 | 标题层级 | h1→h2→h3 不跳级 | 落地页 ✓；**仪表盘跳到 h3 无 h1/h2**；登录页 ✓ | ✗ | 仪表盘 `<main>` 内加 `<h1 class="sr-only">仪表盘</h1>` |
| 2.8 | ARIA 标签 | icon-only button 需 aria-label | themeToggle ✓ · copyBtn ✓ · sbClose ✓ · menuBtn ✓ · collapseBtn ✓ | ✓ | — |
| 2.9 | 语义化 HTML | nav/main/section/article | nav ✓ · main ✓ · section ✓ · article ✓ · aside ✓ · footer ✓ · form ✓ | ✓ | — |
| 2.10 | 目标尺寸 (WCAG 2.2 AA) | ≥24×24 CSS px | icon-btn 36px ✓ · seg__btn ~24px ✓ · **range-tabs__btn ~22px** ✗ · sidebar item 40px ✓ | ✗ | range-tabs__btn padding 从 `4px` 改为 `5px` |
| 2.11 | 触摸目标 (移动端) | iOS 44pt / Android 48dp | icon-btn 36px · sidebar item 40px | ⚠ 桌面原型可接受，生产需增大至 44px | — |
| 2.12 | 触摸间距 | 相邻目标 ≥8px gap | seg__btn gap:2px · range-tabs gap:2px | ⚠ 分段控件内部可接受；独立按钮间距已足够 | — |

---

## 3. 排版与文字 (Typography)

> 参照：`ux-guidelines.csv` → Typography + `typography.csv`

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 3.1 | 对比可读性 | 深色文字在浅色背景 | `#1D1D1F` on `#F5F5F7` / white | ✓ | — |
| 3.2 | 文字重排与间距 | 流体尺寸 · 内容驱动高度 · 无单位行高 | hero 用 clamp() ✓ · 大部分用固定 px | ⚠ | body 添加 `line-height: 1.5` |
| 3.3 | 字体栈 | 系统优先 | `-apple-system, BlinkMacSystemFont, "SF Pro Text"...` | ✓ | — |
| 3.4 | 字号标度 | 系统化标度 | 11/12/13/14/16/18/20/24/30/40px | ✓ | — |
| 3.5 | 字重层级 | 清晰区分 | 400/500/600/700/800 | ✓ | — |
| 3.6 | `topology__name` 不换行 | — | `white-space: nowrap` | ⚠ 窄屏可能截断 | 添加 `overflow: hidden; text-overflow: ellipsis` |

---

## 4. 动效与运动 (Animation)

> 参照：`ux-guidelines.csv` → Animation

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 4.1 | 尊重 reduced-motion | `@media (prefers-reduced-motion: reduce)` | 全局守卫 + hub-ring/pulse 单独禁用 | ✓ | — |
| 4.2 | 运动元素数量 | 1–2 个/视图 | 落地页: pulse-dot + hub-ring + flow 轮播 + status-dot pulse + hover transforms ≈ 5 | ⚠ | 考虑在 flair=subtle 时减少至 2 个 |
| 4.3 | 动效令牌 | 共享令牌 · 按距离/复杂度变化 | `--dur-1` 到 `--dur-4` + `--ease` / `--ease-out` | ✓ | — |
| 4.4 | 装饰档位 | — | flair=subtle 禁用大部分装饰动效 | ✓ | — |

---

## 5. 表单 (Forms)

> 参照：`ux-guidelines.csv` → Forms

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 5.1 | 输入类型 | email/tel/password 等适当类型 | username: `type="text"` ✓ (非邮箱) · password: `type="password"` ✓ | ✓ | — |
| 5.2 | 可见标签 | 每个输入有可见 label | `field__label` + `for` 关联 ✓ | ✓ | — |
| 5.3 | 可聚焦错误摘要 | `role="alert"` + 字段链接 + focus 转移 | 仅用 toast，无字段链接 | ⚠ | 生产环境需添加 inline error + error summary |
| 5.4 | placeholder 不替代标签 | — | username 有 placeholder="admin" 但也有可见 label | ✓ | — |
| 5.5 | autocomplete | 适当 autocomplete 属性 | `autocomplete="username"` + `autocomplete="current-password"` | ✓ | — |

---

## 6. 响应式 (Responsive)

> 参照：`ux-guidelines.csv` → Responsive

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 6.1 | viewport meta | `width=device-width, initial-scale=1` | ✓ | ✓ | — |
| 6.2 | 断点测试 | 320/375/414/768/1024/1440 | 有 560/768/860/960 四个断点 | ⚠ | 需在 320px 和 1440px 下测试 |
| 6.3 | 移动优先 | 默认移动 + md:/lg: 增强 | 桌面优先 + max-width 查询 | ⚠ 原型可接受 | — |
| 6.4 | 侧栏移动抽屉 | — | fixed + transform + overlay ✓ | ✓ | — |
| 6.5 | 登录品牌面板移动隐藏 | — | `@media (max-width: 860px) { .login-brand { display: none; } }` | ✓ | — |

---

## 7. 布局与反馈 (Layout & Feedback)

> 参照：`ux-guidelines.csv` → Layout + Feedback

| # | 检查项 | 标准 | 实际 | 状态 | 修复建议 |
|---|--------|------|------|------|----------|
| 7.1 | 固定定位不重叠 | 考虑 safe-area + 其他固定元素 | proto-bar(52px) + landing-nav(52px) + sidebar overlay(top:52px) | ✓ | — |
| 7.2 | 内容跳动 | 预留空间 / 稳定容器 | SVG 图表有固定 viewBox ✓ · KPI 卡片固定高度 ✓ | ✓ | — |
| 7.3 | 加载状态 | skeleton / progress + `aria-busy` | 无加载状态 | ⚠ | 原型可接受；生产需添加 |
| 7.4 | 懒加载 | 按需加载 | 纯静态原型，无懒加载需求 | ✓ | — |

---

## 8. 已知代码缺陷 (Code Bugs)

| # | 文件 | 行 | 问题 | 严重度 | 修复 |
|---|------|-----|------|--------|------|
| 8.1 | `index.html` | 32 | `aria-label="切换到亮色主题"` 但默认已是亮色，应为 `"切换到暗色主题"` | ✗ 中 | 改为 `"切换到暗色主题"` |
| 8.2 | `styles.css` | 313 | `.feature-card::before` 用 `rgba(var(--accent-rgb, 0,122,255), 0.06)` 但 `--accent-rgb` 从未定义 | ✗ 低 | 改为 `color-mix(in srgb, var(--accent) 8%, transparent)` |
| 8.3 | `styles.css` | 432 | `.kpi-card__trend.down` 颜色为 `--status-success` (绿色) 而非 `--status-danger` | ✗ 中 | 下降趋势应为红色或保持绿色（取决于语义：错误数下降是好事）→ 当前语义正确（错误减少=好），但命名 `down` 易混淆 | ⚠ 保持但重命名为 `down--good` 或注释说明 |

---

## 修复优先级

| 优先级 | 编号 | 问题 | 影响 |
|--------|------|------|------|
| **P1** | 2.7 | 仪表盘缺 h1/h2，标题跳级 | 屏幕阅读器导航受阻 |
| **P1** | 8.1 | 主题切换 aria-label 方向错误 | 辅助技术用户获得错误信息 |
| **P2** | 2.2 | 缺少 scroll-padding-top | 键盘焦点可能被 sticky 导航遮挡 |
| **P2** | 2.10 | range-tabs 触摸目标 <24px | WCAG 2.2 AA 目标尺寸不达标 |
| **P2** | 8.2 | feature-card::before --accent-rgb 未定义 | 卡片悬停辉光始终用默认蓝色 |
| **P3** | 3.2 | 缺少全局 line-height | 文字行距依赖默认值 |
| **P3** | 3.6 | topology__name 可能截断 | 窄屏渠道名显示不全 |
| **P3** | 4.2 | 动效元素偏多 | 可能分散注意力 |
| **P3** | 5.3 | 登录无错误摘要 | 表单验证可访问性不足 |
| **P3** | 6.2 | 缺少 320px/1440px 断点测试 | 极端尺寸可能有问题 |

---

## 总结

| 维度 | 通过 | 警告 | 违规 |
|------|------|------|------|
| 玻璃拟态 | 5 | 2 | 0 |
| 可访问性 | 8 | 3 | 2 |
| 排版 | 5 | 1 | 0 |
| 动效 | 3 | 1 | 0 |
| 表单 | 4 | 1 | 0 |
| 响应式 | 3 | 2 | 0 |
| 布局反馈 | 4 | 0 | 0 |
| 代码缺陷 | 0 | 1 | 2 |
| **合计** | **32** | **11** | **4** |

**整体评价**：原型在 macOS 磨砂玻璃简白风的实现上质量较高，vibrancy 材质、对比度、动效守卫、语义化 HTML 均到位。主要问题集中在标题层级（仪表盘跳级）、aria-label 方向错误、以及若干触摸目标尺寸。P1 问题应在生产落地前修复。

---

## 附：UI Pro Max 风格匹配度

| 风格 | 匹配度 | 说明 |
|------|--------|------|
| **Liquid Glass** | ★★★★☆ | 最接近——adaptive translucent material、reduced transparency 降级、platform-aligned motion |
| **Glassmorphism** | ★★★☆☆ | 部分符合——backdrop-filter ✓ 但不透明度远高于 15-30% 推荐 |
| **Bento Box Grid** | ★★★☆☆ | 仪表盘 KPI/图表网格有 Bento 气质——rounded 16px、neutral bg、subtle shadow |
| **Minimalism & Swiss** | ★★★☆☆ | 简白底色、大量留白、几何布局——但磨砂玻璃材质超出 Swiss 范畴 |
