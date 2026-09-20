# Agent Note: DB 导入后失效全部受影响 React Query 分区

Status: implemented

## 问题

审计 FE-04/F-L1 确认：Settings 的 DBDump 导入成功后只 invalidate `channels`、`groups`、`keys`、`settings` 等
少数 query family；Mask、Dashboard（usage-heatmap/token-trends/recent-errors）、Logs（log-errors/
log-stop-all-state/模型评估）这些也被导入覆盖的数据分区不会失效，用户要在最长 30s stale 窗口之后才
可能看到新数据。

## 决定

- `Settings.tsx` 的 `importMut.onSuccess` 在保留原有失效项的基础上，增加：
  `["apikeys","secret"]`、`["mask-config"]`、`["mask-rules"]`、`["usage-heatmap"]`、
  `["token-trends"]`、`["recent-errors"]`、`["log-errors"]`、`["log-stop-all-state"]`、
  `["model-eval"]`。
- 使用 React Query 部分匹配语义：失效 key 为前缀，匹配 query 如 `["usage-heatmap", 365]`。

## 备选方案

- **导入成功后 `qc.clear()` 清空全部缓存**：最彻底，但会丢掉与导入无关的在线状态和请求缓存，也重显加载态。
- **按页面维护「失效 key 常量表」**：更结构化，但不在本次最小修复范围内；当前列表集中在一个函数里已可读。
- **等 staleTime 自然过期**：不改代码，但导入后数据表现不可接受。

## 后果

- **收益**：DB 导入在系统各分区的可见性立即一致，不做整窗刷新也能保持数据正确。
- **代价与已知上限**：新增失效列表是手工与页面 queryKey 对齐的，新增页面分区时需回到此处补一行；可在
  后续引入单测生成器自动核对。

## 验证

- `src/pages/Settings.test.tsx`：预置 `apikeys/secret`、`mask-config`、`mask-rules`、`usage-heatmap`、
  `token-trends`、`recent-errors`、`log-errors`、`log-stop-all-state`、`model-eval` 缓存，导入后断言
  全部 `isInvalidated` 为 `true`。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
