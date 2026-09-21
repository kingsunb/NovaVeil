# Agent Note: API Key / Channel Key 明文 reveal 不进入 React Query 缓存

Status: implemented

## 问题

审计 FE-02/F-M2/OLD-22 确认：点「显示密钥」后，明文 API Key 与 Channel Key 通过 `useQuery` 拉取并留在 React Query cache
中（`staleTime` 分别为 60s 与 5min）。即使编辑器关闭、眼睛隐藏，只要 query 未 GC，浏览器内存里就持续保留
明文；编辑态草稿在「取消」关闭后也不会被清理，只是靠父级 `channel=null` 的滞后 effect 间接复位。

## 决定

- `Keys.tsx` 的 `KeyEditor` 不再使用 `useQuery` 拉取 `/apikey/secret/:id`，改为组件内直接
  `api.getAPIKeySecret(id)`；`secret`、`secretLoading` 只存组件 state，隐藏密钥或关闭编辑器即清空，
  并以 `secretFetchGenRef` 使过期请求作废。
- `channel-editor.tsx` 的 `CredTab` 不再使用 `useQuery` 拉取 `/channel/keys/:id`，改为组件内直接
  `api.getChannelKeys(channelId)`；`secrets` 只存组件 state，切换渠道或关闭标签即清空。
- `ChannelEditor` 增加 `closeEditor()`：关闭（含 backdrop/Escape）时显式把 draft 复位为 `toDraft(null)`、
  标签页复位为 `cred`，并中止批量模型测试；footer「取消」也走同一条路径，不再只依赖父级把 `channel`
  置 null。

## 备选方案

- **保留 useQuery，显示后立即 invalidate/queries 移除**：改动点少，但删除缓存存在时序窗口，且此后眼睛
  切换会重新请求；直接 fetch 最符合「明文只在可见期间存在」的安全语义。
- **缩短 staleTime/gcTime 到 0**：仍会把明文写入 cache 的序列化状态；不解决持久保留问题。
- **只在关闭编辑器 effect 里清 draft**：依赖父级 state 与 effect 时序，保留「取消后短暂残留」窗口；选择显式 `closeEditor()`。

## 后果

- **收益**：眼睛揭示的明文不落 query cache；编辑器关闭后 draft 与眼睛状态立即清零。渠道保存草稿与创建 API Key 的返回值也不留在 mutation state，见 [web mutation 缓存与导航防护](./2026-09-22-web-mutation-cache-and-nav-guards.md)。
- **代价与已知上限**：每次首次点眼睛都需重新请求明文（复用此前行为被牺牲）；若 product 需要长会话内记忆
  明文，必须重访本决定并补用户交互（例如指纹/为什么）。

## 验证

- `src/pages/Keys.test.tsx`：点眼睛直接 fetch 明文，断言 `qc.getQueryData(["apikeys","secret",7])` 为
  `undefined`，隐藏后仍无缓存。
- 前端 `pnpm typecheck`、`pnpm lint`、`pnpm vitest run` 通过（522 个测试）。
