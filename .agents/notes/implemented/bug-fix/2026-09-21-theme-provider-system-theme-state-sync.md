# Agent Note: ThemeProvider 系统主题变化同步更新 context.resolved

Status: implemented

## 问题

审计 FE-05/F-L4 确认：`ThemeProvider` 只调用 `resolve(theme)` 在 render 期间求一次 `resolved`；系统主题
（`prefers-color-scheme`）变化时监听器只改 `document.documentElement.dataset.theme`，从未触发 React
state 更新。因此所有消费 `useTheme().resolved` 的组件（Topbar、Settings 等）在系统主题切换后仍使用旧
值，DOM 与 context 长期不一致。

## 决定

- `systemDark` 提升为 React state，初始值取 `matchMedia("(prefers-color-scheme: dark)").matches`。
- `resolved` 用 `useMemo` 基于 `[theme, systemDark]` 计算；`resolve` 改为纯函数接收 `systemDark`。
- `matchMedia` 的 `change` 监听器始终挂载，变化时 `setSystemDark(e.matches)`；`useEffect` 只负责把
  最新的 `resolved` 写入 `document.documentElement.dataset.theme`。
- 监听器注册后立即校准一次 `setSystemDark(mq.matches)`，避免挂载与查询初值之间的微小时差。

## 备选方案

- **系统主题变化时同步调用 `setThemeTheme`？继续直接改 dataset**：零 state 但 context 消费者永远陈旧，
  正是本次要修的缺陷。
- **仅在 `theme === "system"` 时挂载监听器**：省监听器，但切回 system 时仍需补一次查询且多一条分支；
  始终挂载并用已 state 化的 `systemDark` 计算更简单。
- **用 `useSyncExternalStore` 订阅 matchMedia**：更通用，但当前组件只有一对 theme/resolved，useState
  即可，不引入新抽象。

## 后果

- **收益**：系统主题变化时 `useTheme().resolved` 与 DOM dataset 同源同步。
- **代价与已知上限**：`systemDark` 为非 system 模式也保持更新（不触发消费者重渲，因为 context value 相同）；
  若基准库从假 media query 改为多个媒体维度，需重访。

## 验证

- `src/components/layout/ThemeProvider.test.tsx`：模拟 matchMedia change，断言 system 模式下 resolved
  由 light→dark→light；显式 dark 模式下不跟随系统变化。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
