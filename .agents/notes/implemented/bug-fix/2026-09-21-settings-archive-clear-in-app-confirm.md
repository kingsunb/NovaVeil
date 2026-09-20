# Agent Note: 清空归档用站内二次确认替代原生 confirm

Status: implemented

## 问题

审计 OLD-30 确认：Settings 的「清空归档」按钮使用浏览器原生 `confirm()` 做不可恢复操作的确认。原生弹窗
与 NovaVeil 界面的警告级别不一致，自动化测试也无法稳定拦截，且用户在原生对话框里没法撤销加载态。

## 决定

- Settings 保留策略卡片改用仓库已有的 `ConfirmButton` 组件：`tone="destructive"`、`label="清空归档"`、
  `loadingLabel="清空中…"`，`onConfirm` 调 `clearMut.mutate()`；`disabled={!stats?.file_count}` 保留
  「无归档不可点」语义。
- `ConfirmButton` 增加 `loadingLabel` 与 `disabled` props，让非删除场景（清空归档）也能复用两段式确认。

## 备选方案

- **在 Settings 内联一套 armed state**：重复实现 `ConfirmButton` 已有的两段式逻辑，不选择。
- **保留原生 confirm，仅改进文案**：改动最小但不解决可测性与 UI 一致性问题。
- **把是否无归档的 disabled 逻辑放到 onConfirm 前 toast 提醒**：可点但有误导，保留 disabled。

## 后果

- **收益**：不可恢复操作得到站内一致、可测的二次确认；loading 文案从「删除中…」正确显示「清空中…」。
- **代价与已知上限**：`ConfirmButton` 仍只接受文字 label，未提供 icon 插槽（原按钮上的 Trash2 图标不再
  显示），若视觉审查要求图标需给组件加 `icon` prop。

## 验证

- `src/components/ui/confirm-button.test.tsx`：覆盖自定义 `loadingLabel` 与 `disabled`。
- Settings 现有测试全量通过；前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
