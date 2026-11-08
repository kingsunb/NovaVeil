# Agent Note: 移除顶栏命令面板

Status: implemented

## 问题

顶栏右上角的“命令面板”入口与 `Ctrl/Cmd + K` 快捷键会打开一个独立的页面跳转面板。用户明确表示不再需要该功能；继续保留会占用顶栏空间、维护额外组件和快捷键监听，也会让“顶栏搜索”的语义与侧栏导航重复。

## 决定

- 删除 `web-next/src/components/layout/CommandPalette.tsx`。
- `Topbar` 移除 `onOpenCommand` 属性、搜索图标按钮与快捷键提示；`AppShell` 移除 `cmdkOpen` 状态、`Ctrl/Cmd + K` 键盘监听和面板渲染。
- 页面导航由现有 `Sidebar` 承担；同步更新 `Topbar`/`AppShell` 测试与前端文档。loader 统一方案仍由 [route-loaders 决策记录](2026-09-21-route-loaders-re-export-page-loaders.md) 约束，当前消费者为 App 与 Sidebar。

## 备选方案

- **只隐藏按钮、保留快捷键**：用户说功能不需要，保留快捷键仍会打开面板并形成隐藏入口；不选。
- **保留组件供未来复用**：会留下未使用的组件、测试和路由预加载引用，增加维护面；不选。
- **改成普通搜索框**：用户没有提出搜索需求，且会引入新的交互与后端接口；不选。

## 后果

- **收益**：顶栏更精简，少一个全局弹层与键盘监听；导航入口唯一，行为更可预测。
- **代价与已知上限**：习惯 `Ctrl/Cmd + K` 的用户需要改用侧栏；以后若重新引入全局搜索，需要重新设计入口和可访问性。

## 验证

```sh
cd web-next && pnpm test -- Topbar AppShell
cd web-next && pnpm typecheck
```
