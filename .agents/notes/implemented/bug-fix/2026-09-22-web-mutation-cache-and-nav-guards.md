# Agent Note: web 明文不进 mutation 缓存，并收紧导航防护

Status: implemented

## 问题

眼睛揭示已经不把密钥放进 `useQuery`，但渠道保存仍把含 `keys[].key` 的草稿当作 mutation variables，创建 API Key 的返回值留在 `mutation.data`。登出前这些明文可以从 React Query 的 mutation state 读回。`legacy-path` 的前缀判断放行了 `/\evil.com` 和 `///host`：前者浏览器把反斜杠当成斜杠，后者经 URL 解析落到别的源。全库 JSON 导入在选中文件时立刻覆盖数据。启停、删除、文本导入、队列上移和全停在写缓存之前没有取消对应列表查询，在途 refetch 会把旧列表写回来。

## 决定

- 渠道保存用一次性 ticket 取出草稿。`mutation.state.variables` 只保留数字，不保留 `keys[].key`。创建 API Key 的请求体放在提交瞬间的 ref 里，mutation 返回值去掉 `api_key`；明文只经 `onCreated` 进入「仅此一次」对话框的组件 state。全库导入的 `File` 同样不进 variables。
- 非空 `channel_proxy` 在列表里只显示 `****`。编辑器点眼睛时 `POST /channel/proxy/:id`，明文只留在高级页组件 state，不进 React Query。保存时字段仍是 `****` 就省略该字段，不当成新代理；复制渠道同样不把 `****` 写进新渠道。空串仍表示清除。（后续决定反转：渠道代理在管理台直接显示明文，移除眼睛揭示与 `****` 哨兵，见 [管理台直接显示渠道代理明文](../simplification/2026-09-24-show-channel-proxy-plaintext-in-admin-ui.md)。）
- `safeLegacyHref` 用 `URL` 解析。只放行 `http`、`https`、`mailto`，以及相对 `http://localhost` 解析后仍同源的 `/`、`./`、`../` 路径。原始值里的反斜杠或空白一律回退 `/legacy`。这收紧 [legacy-path 链接协议白名单](./2026-09-21-legacy-path-href-protocol-allowlist.md)，不恢复把 flags 原值直接写入 `href`。
- 全库 JSON 导入沿用归档清空的 `ConfirmButton`：选文件后先说明会覆盖渠道、分组、密钥和设置，再点两次才调用导入。不使用原生 `confirm`，也不新做对话框。
- 写之前 `cancelQueries` 对应列表：自定义模型启停/删除、API Key 删除/创建/更新、渠道文本导入取消 `["channels"]` 或 `["keys"]`。评估队列上移/停止、日志全停/恢复在 `setQueryData` 之前再取消一次对应 queryKey。渠道删除、分组保存、渠道与自定义模型主保存原有的 cancel 保持不动。
- 小屏抽屉在 `max-width: 767px` 时把 Tab 圈在侧栏内，关闭后焦点回到打开按钮；桌面常驻侧栏不圈定。脱敏内置规则空态用 `EmptyState`。强制改密页和灰度回退提示用 `BrandMark`。总览「模型用量 Top」在统计失败时显示加载失败，不用「暂无模型用量」。`router.nginx.conf` 的 `/healthz`、`/legacy`、`/assets/`、`/__flags/` 重复五条安全头，`style-src` 仍保留 `'unsafe-inline'`。

眼睛揭示仍不走 `useQuery`，见 [API Key / Channel Key 明文 reveal 不进入 React Query 缓存](./2026-09-21-api-key-channel-key-reveal-outside-react-query.md)。管理台「加入排序」仍只对 `ok` 可点。

## 备选方案

- **保存成功后 `mutation.reset()` 清掉 variables**：能抹掉已经写入的草稿，但 reset 与在途 mutation promise 抢状态，失败路径上明文会多留一轮。ticket 从一开始就不把草稿放进 state。
- **创建结果继续留在 `mutation.data`，只在登出时 `queryClient.clear()`**：登出前的窗口仍然可读，不符合「创建明文只放组件 state」。
- **全库导入用新的确认对话框**：归档清空已经是两段式 `ConfirmButton`，再造一套对话框会有两套确认交互。
- **队列 SSE 每次 `setQueryData` 都先 cancel**：上移/停止是用户发起的写；SSE 是连续推送，每条都 cancel 会打断 5 秒轮询，还可能让后到的事件先落缓存。SSE 推送保持直接写入。

## 后果

- **收益**：登出前不能从 query cache 或 mutation state 读回渠道保存草稿、创建返回的 `api_key`、用户输入的自定义密钥和导入文件正文。`/\evil.com`、`///evil.com` 和带空白的 legacy 链接回到 `/legacy`。导入不再因选错文件就覆盖全库。列表写与在途 refetch 不再互相覆盖。
- **代价与已知上限**：渠道保存进行中，ticket 对应的草稿只活在那一次 `mutationFn` 的局部变量里，不在 cache state。创建成功后的明文仍在对话框组件 state，关掉才丢，这是「仅此一次」展示所要求的。评估队列的 SSE 回调仍直接 `setQueryData`。`router.nginx.conf` 的安全头与 `nginx.conf` 一样是内联重复，改 CSP 要改两处文件。

## 验证

`web-next` 下相关 `pnpm exec vitest run` 通过：先是 flags、Keys、Channels、Settings、Dashboard、AppShell、CustomModels、Logs、App、ModelEval 共 10 个文件 132 个测试；补上渠道代理揭示后再跑 Channels、Keys、`api.endpoints.test.ts` 共 47 个测试。创建密钥与渠道保存断言缓存不含明文，导入断言选文件后不请求、两段确认后才失效缓存，模型 Top 断言统计失败与「暂无模型用量」分开，移动抽屉断言 Tab 圈定且 Escape 后焦点回到打开按钮，渠道代理断言眼睛揭示不进 query cache、未改的 `****` 不出现在更新请求里。
