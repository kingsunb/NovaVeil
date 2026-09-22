# NovaVeil 当前状态与下一阶段规划

> 更新：2026-09-22。基线：`b38e340`（已与 `origin/main` 快进对齐）。本轮核对源码和本地参考仓库，没有跑测试或 Compose。
> 本文是跨模块优先级。OpenCode 三项的实施细节分别在专项计划里，这里不复制任务清单。
> 2026-09-16 及更早的路线图条目已被本页取代。`docs/audits/` 里的报告仍是形成当时的快照，不因为本页更新而改写。

## 1. 状态口径

- **代码已有**：能定位实现。不等于测试通过、已部署或验收完成。
- **待修**：本轮核对里仍然存在的实现缺口。
- **待改文档**：代码已经如此，文档或界面文案还没对齐。
- **待实施**：专项计划里写明、本轮没有开工的产品项。

`docs/FEATURES.md` 的 REQ-024 已按现行契约改写：`ok` 与 `violation` 都可进排序，`priority` 从 1 递增且数值越小越优先，10 分钟是执行超时。管理台「加入排序」按钮仍只对 `ok` 可点。

## 2. 不要再排期

以下已经落地。新工作不要把它们当成未做功能重开。

- 脱敏命中摘要有界下发；脱敏映射在 `apiKeyID > 0` 时按 `apiKeyID + ":" + sessionKey` 隔离。MAC/USCC 规则默认关闭。
- 凭据列 `nv1:` 静态加密覆盖渠道 Key、多 Key、渠道代理和 API Key。列表接口仍打码。数据库备份导出的渠道 Key 与 API Key 是明文，代理和自定义头值仍打成 `****`。
- Cookie 在 `cookie_secure`、直接 TLS，或入站 `X-Forwarded-Proto: https` 时带 Secure。管理 API 令牌桶有开关，默认关闭。
- 评估队列有 panic 恢复、条件更新、running 租约和单实例互斥。路由骨架、滚动复位、导航预加载、命令面板入口已在前端。
- 渠道 `sort` 可以写成 0。完全透传不跳过分组路由、故障转移和 Key 轮询。
- OpenCode 免费渠道与官方渠道已经是两条内置渠道。自定义渠道的 `opencode_compat` 被强制关掉。Chat、Responses、Anthropic 的转换在现有 relay，不需要第二套协议栈。

参考 [gpt-load](https://github.com/tbphp/gpt-load) 和本地的 opencode2api 时：只借鉴行为，不复制源码，不引入第二套数据库、`auth.key` 或无库配置面。不移植订阅 OAuth、Responses WebSocket、匿名请求改写、CLI 指纹或双 Key 池。

## 3. 第一批：凭据、内存和部署

### 3.1 凭据

库内渠道 Key、多 Key、自定义头、头模板、JWT 密钥、全局代理和渠道代理都是 `nv1:`。列表只出掩码。数据库备份里的渠道 Key 与 API Key 是明文；代理和自定义头值仍是 `****`。`channel_proxy` 与 `BaseURL` 均不做目标地址范围限制，内网代理/内网上游都可直连。导入拒绝精确 `****`。

前端眼睛按钮不把密钥放进 `useQuery`。渠道保存和创建 API Key 的明文也不进 mutation state。渠道代理列表只显示 `****`，编辑器点眼睛才请求明文。

- [x] 上述列已加密。数据库备份的渠道 Key 与 API Key 是明文。渠道文本导出包含全部渠道（含内置渠道和没有 Key 的渠道），写出明文渠道名、明文 BaseURL 和明文 Key。列表接口仍只出掩码。
- [x] 保存和创建成功后清掉 mutation 里的明文，创建结果只留在组件状态。代理明文同样不进 React Query。
- [x] 数据库导入拒绝渠道 Key 和代理上的精确 `****`。文本导入已经拒绝。
- [x] `channel_proxy` 与渠道 `BaseURL` 均不做出口地址范围限制。内网代理/内网上游都可写，说明在 `docs/SECURE_DEPLOYMENT.md`。

### 3.2 请求体并发内存

单请求仍是压缩 16 MiB、解压 64 MiB。进程内总预算默认 128 MiB。超额返回 413，不进成员冷却。取消、解压失败、脱敏失败和 panic 都会归还额度；后两条把请求状态里的全文截到 64 KiB。

- [x] 读入和解压共用可取消的进程级预算，结束路径释放并截断。

### 3.3 评估崩溃重入与 Compose 插值

单实例 SQLite 启动立刻把遗留 `running` 退回 `queued`。停机只退回本进程未完成的任务。多实例仍只回收过期租约。`NOVAVEIL_WEB_NEXT_IMAGE` 只出现在 `web-next` profile 上，未设置时默认 Compose 解析不再失败。

- [x] 单实例启动立即回收本进程留下的 `running`；停机把未完成任务退回 `queued`。
- [x] 必填镜像插值已移出默认文件。未开 profile 不依赖该变量。

## 4. 第二批：relay 隔离与流式终态

这三项不改变 OpenCode 计划里的协议选择。会话计划只决定上送的头，不扩大下面这张表的键。

- [x] 粘合、上游会话号和脱敏共用 `sessionScopeKey`。有 API Key 时是 `id:sessionKey`，控制台是 `console:` 前缀。原文超过 256 字节视为无会话。粘合单组 4096，跨组 16384。
- [x] 脱敏会话只有写出占位符才入表，未命中在请求结束时丢掉。上限 4096，超限淘汰最久未访问。
- [x] 污染流静默截断，不再补成功的 `stop` 和 `[DONE]`。缺哨兵但终止原因合法的完整流仍合成正常收尾。

不列入：透传跳过路由、故障转移热旋。当前实现在无候选或结构性跳过超过成员数时会等待并清空排除，不是紧循环。

## 5. 第三批：OpenCode，用现有转换器

协议和会话头已在工作区落地，未提交。能力快照仍不做。

| 计划 | 状态 |
|---|---|
| [按模型、按档的原生协议](OPENCODE_NATIVE_PROTOCOL_PLAN.md) | 已落地。`upstream_protocol` 为空则按渠道类型。六个出厂免费模型是 `chat`。官方 Go 渠道出厂无模型。思考档位仍按渠道类型注入。 |
| [合法 ses_ 与 prompt_cache_key](OPENCODE_SESSION_CACHE_KEY_PLAN.md) | 已落地。合法 `ses_` 原样上送。`prompt_cache_key` 只在 Chat/Responses 转换后回写。 |
| [能力快照](OPENCODE_MODEL_CAPABILITY_PLAN.md) | 未做。不单独打目录，不计价。 |

## 6. 第四批：文档、界面和低优先级代码

代码契约已在本文和对应文档改正的，不再单列。界面项里还没单独决定的只有「加入排序」按钮。

- [x] 评估排序列表文案已与 `ok` / `violation` 都入列对齐。管理台「加入排序」按钮仍只对 `ok` 可点，比 `from-history` 接口更窄。不要在未单独决定前放宽这个按钮。
- [x] `legacy-path` 用 URL 解析，只允许 `http`、`https`、`mailto` 或同源路径，拒绝反斜杠。
- [x] 全库 JSON 导入先经应用内确认，写明覆盖渠道、分组、密钥和设置。
- [x] 启停、删除、文本导入、队列上移和停止、日志全停/恢复在写之前取消对应列表查询。评估队列的 SSE 推送仍直接写缓存。
- [x] 移动抽屉有焦点圈定和恢复。脱敏空态复用 `EmptyState`。强制改密页和回退提示使用 `BrandMark`。总览把统计失败和「暂无用量」分开。
- [x] 可选 `router.nginx.conf` 的本地 location 重复安全头。`style-src` 的 `unsafe-inline` 仍保留。
- [ ] Cookie 是否只在可信代理之后才承认 `X-Forwarded-Proto`，要改代码。当前任意该头都会把 Cookie 设得更严，不会放宽客户端 IP。未配置 `trusted_proxies` 时，反代后的登录限速桶会塌成代理地址。

明确不做，除非另开一项并写明客户端：Gemini 入站、Rerank 互转、订阅 OAuth、Responses WebSocket、提示词软亲和、水平扩展、models.dev 自动计价。gpt-load 里值得以后单独立项、但排在本页四批之后的，是凭据加模型的短冷却、API Key 协议白名单、`previous_response_id` 钉扎，以及上游没回成本时的手动单价。

## 7. 完成门槛

- [x] 已落地项有对应 Agent Note。能力快照和 Cookie 只信可信代理仍未做，本页保持未勾。
- [x] 2026-09-22 本地 `go build ./...` 与 `go test -count=1 ./...` 通过。`pnpm verify-notes` 通过（48 篇）。修完抽屉焦点空值、代理请求类型和创建密钥断言后，`pnpm typecheck` 通过，`pnpm lint` 仅余 `upstream-protocol.tsx` 的 react-refresh 警告，`pnpm exec vitest run` 为 51 个文件、533 个测试通过。未跑浏览器验收，也未执行 Compose。
- [ ] 部署、队列和流式改动有对应的失败路径测试，不只靠阅读。
