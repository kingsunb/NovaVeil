# Agent Note: Mask 页写操作前强制重取最新配置

Status: implemented

## 问题

web-next 的 Mask 页(脱敏配置)是整份读-改-写: `patch` 用 React Query 缓存快照合并后 PUT 全量配置, 而该查询无 refetchInterval 且全局关闭 refetchOnWindowFocus。长开的标签页里切一个开关, 会把别处(另一管理员或另一标签页)期间新增的拦截词、改动的规则开关整份覆盖回旧值——经典丢失更新。删除条目还按数组下标定位, 快照过期时下标错位会删错条目。

## 决定

- `web-next/src/pages/Mask.tsx` 三条写路径(全局/内置规则开关的 patch、新增词 addTerm、删除词 removeTerm)写前一律 `await configQ.refetch()` 取最新配置, 以最新快照为底合并后 PUT, 并把新数据反映到 UI。
- refetch 失败时 toast 显式报错并中止写操作, 不静默降级到旧快照。
- 删除定位由裸下标改为按 `value` 匹配(MaskConfigTerm 无 id, value 是天然唯一键且 UI 本就按 value 防重): 点删除按钮记录 value, 确认时在重取后的新快照中按 value 过滤, 下标漂移不再导致误删。
- 新增词的查重同样基于重取后的最新词集, 弹窗打开期间别处已加同词则报"该敏感词已存在"。

## 备选方案

- **后端提供字段级更新端点** — 最强论据是从根上消除读改写窗口; 否掉因为 PUT 全量是后端既有契约, 为一个页面扩 API 面收益不成比例, 前端写前重取已闭合窗口; 后端契约若日后调整再翻新此篇。
- **给配置查询加 refetchInterval 周期轮询** — 最强论据是实现一行; 否掉因为轮询只缩小不闭合窗口, 且常态空转徒增请求, 写前一次性重取才是确定性保证。

## 后果

- **收益**: 丢失更新与错删窗口闭合, 多管理员/多标签页并发编辑安全。
- **代价**: 每次写操作多一个 GET 往返; 写路径变 async, 事件回调加 `void` 前缀(仓内既有先例)。

## 验证

- `web-next/src/pages/Mask.test.tsx`: 切开关断言 GET→GET→PUT 顺序且 PUT 载荷合并新快照; refetch 失败断言 toast.error 且无 PUT; 模拟下标错位后删词断言按 value 正确删除。
