# Agent Note: route-loaders 改为 re-export page-loaders 单一注册表

Status: implemented

## 问题

审计 FE-07/F-L2 确认：`src/lib/route-loaders.ts` 与 `src/lib/page-loaders.ts` 各自维护一份 11 个页面的
loader 清单——前者裸 `import()`，后者经 `createPageLoader` 带 Promise 缓存与失败清空。两边路径只要漏改
一边，App 懒加载、Sidebar `preloadPage` 与命令面板预加载就会漂移，且翻车通常是运行时才暴露的 chunk 404。

## 决定

- `route-loaders.ts` 不再定义 loader，改为 re-export `page-loaders.ts` 的 11 个 `load*Page` 与
  `preloadPage`。两个模块名保留，调用方零改动。
- App 的 `lazy()`、Sidebar 的 `preloadPage` 与 CommandPalette 共享同一组 `createPageLoader` 实例，
  Promise 缓存和失败清 pending 全局唯一。

## 备选方案

- **route-loaders 直接删除，全量改为 page-loaders 导入**：最干净，但要改动全部 import；保留 re-export
  文件作为稳定公共出口，最小 diff。
- **generated loader 表**：用 Vite 的 `import.meta.glob` 按文件系统自动生成，少手写但会改变当前显式
  命名契约，不在本次范围内。
- **只加单测防漂移，保留双注册表**：能发现问题但不能消除重复，选择去重。

## 后果

- **收益**：loader 注册表只有一份；空 import 不再阻塞后端 pnpm build；新增页面只需在 `page-loaders.ts`
  增一处。
- **代价与已知上限**：`route-loaders.ts` 仍是一个薄 re-export 文件，需要存在是因为它是既有公共路径；
  未来若接受全量 import 替换可删除。

## 验证

- `src/lib/route-loaders.test.ts`：对 11 个 loader 与 `preloadPage` 逐一断言 `toBe` 同一引用。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
