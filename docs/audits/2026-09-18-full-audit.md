# NovaVeil 全面审计报告（2026-09-18/19）

- **审计对象**: `/workspace/NovaVeil` @ HEAD `7a8b6e0756a42def2bbe0f58640b1162085ff776`（`main` 7a8b6e0，working tree 干净）
- **审计方式**: 只读审计。未修改任何产品代码；新增本报告文件；过程产物在 `/tmp/novaveil-audit/`
- **审计方法**: 基线实测（构建/测试/类型检查/lint/覆盖）+ 5 路并行深度审计（后端安全、后端可靠性/架构、前端、依赖/DevOps/文档、历史问题清单核对）
- **历史基线**: 与 `docs/audits/AUDIT_ISSUES_2026-09-13.md` 及早前审计报告（已从 docs/ 移除）合并核对 61 条

---

## 0. 执行摘要（TL;DR）

项目整体工程化水平较高：鉴权 JWT 设计、登录限速、CSRF/Origin 防护、CSP、GORM 参数化、错误分级、自更新默认关闭、进程内内存容器多有界、测试覆盖较充分。**没有发现 Critical 级漏洞**（0 个），也**没有发现 SQL 注入、命令注入、InsecureSkipVerify、任意文件读写这类常规高危问题**。

但有三类“硬伤”需要尽快处理：

1. **交付链断裂**：全新克隆无法直接 `go build ./...`（`static/out` 未跟踪）；`web-next/Dockerfile` COPY 了不存在的 `tailwind.config.ts`；`docker-publish` 每天 push main 时把未跑测试/漏洞扫描的镜像推向与文档不一致的镜像名；三处版本号不一致会让手工 release 发布成错误的 v0.1.0。
2. **会毁数据的 bug**：渠道自动同步把上游非 2xx 当“空模型列表”，`SyncModelsTask` 会据此删除该渠道全部自动模型及其在分组中的引用和评估排名（High）。
3. **管理面板的隐私/会话问题**：Chat 完整对话存 `localStorage` 且登出不清理，换人登录会看到上一任管理员对话（High）。

其余集中在中低风险：渠道 SSRF、明文凭据落库/备份导出、CSP/安全头不完整、eval 调度器 panic 卡死、任务停止 WaitGroup 竞态、会话粘合无上限等。

---

## 1. 基线验证（本会话实测）

| 验证项 | 结果 |
|---|---|
| `go build ./...` | 通过（当前工作区已存在 `static/out/` 构建产物时成立；干净克隆会失败，见 H-C） |
| `go test ./... -cover` | 全部 ok；`internal/relay` 74.1%、`internal/op` 63.9%、`internal/server` 85.5%、`internal/keylimit` 98.1% |
| `go vet ./...` | 通过，0 告警 |
| `go mod verify` | `all modules verified` |
| `cd web-next && pnpm build` | 通过（2033 模块，产物 `static/out/`） |
| `pnpm typecheck` | 通过，0 错误 |
| `pnpm lint` | 0 错误，2 warnings（`Logs.tsx:760,782` 缺 `req` 依赖） |
| `pnpm vitest run` | 46 文件 / 506 测试全部通过 |
| `pnpm size-limit` | 通过（但 JS gzip 245.6KB / 250KB=98.3%，CSS 13.1KB / 14KB=93.6%，余量不足） |
| `pnpm outdated` | 16 个前端依赖可更新，其中 3 个跨大版本 |
| `pnpm audit` | 本地失败：npmmirror 无 audit 端点（CI 中也无 audit 门禁） |
| `bash -n scripts/*.sh scripts/dockerfile/entrypoint.sh` | 全部通过 |

---

## 2. 发现汇总

跨域原始发现 **65 条**；少量问题在多个领域重复出现（如明文凭据导出、cookie Secure 默认值、版本号不一致、管理 API 限流），正文已用“=”标注对应关系，不重复计算为两个独立缺陷：

| 领域 | Critical | High | Medium | Low | Info | 小计 |
|---|---|---|---|---|---|---|
| 后端安全 | 0 | 0 | 6 | 6 | 5 | 17 |
| 后端可靠性/架构 | 0 | 1 | 6 | 7 | 1 | 15 |
| 前端 web-next | 0 | 1 | 3 | 6 | 6 | 16 |
| 依赖/DevOps/文档 | 0 | 4 | 4 | 6 | 3 | 17 |
| **合计（原始）** | **0** | **6** | **19** | **25** | **15** | **65** |

历史问题清单核对（另计，不叠加进上表）：核对 61 条 → **已修复 33 / 部分修复 9 / 未修复-回归 19 / 无法核验 0**；其中多项与上表重复（如版本号、明文凭据、管理 API 限流等）。

---

## 3. 高优先级问题（全部 High）

### H-01 【可靠性·缺陷】渠道模型自动同步把上游非 2xx 或空列表当作成功，会删除该渠道全部自动模型及分组引用

- 位置：`internal/helper/fetch.go:89-124`（OpenAI）、`:126-181`（Gemini）、`:182-233`（Anthropic）；`internal/task/sync.go:50-113`；`internal/op/channel.go:631-700`
- 证据：三个 fetch 函数 `client.Do` 后**不检查 `resp.StatusCode`**，直接 JSON decode。401/403/429 的 JSON 错误体会被解码为 `Data: nil`，与 200 空列表无法区分并返回 `nil` error。`SyncModelsTask` 拿到空列表后视为成功同步，把全部旧自动模型计算为“待删除”，随后 `ChannelUpdate` 删除这些模型并级联清理分组成员/路由/评估排名。
- 影响：渠道 Key 失效、上游限流、代理错误等短暂故障，可能在一次自动同步中**不可逆地清空该渠道自动模型及其分组配置与评估排名**。
- 建议：fetch 函数解码前校验 `2xx`；`SyncModelsTask` 对空列表按失败/跳过处理（或仅在“明确成功且非空”时才允许删除）；补 401 JSON 错误体回归测试。
- 确认状态：已由代码事实确认。

### H-02 【前端·隐私】Chat 完整对话与 mask 会话 ID 存 `localStorage`，登出/登录均不清理

- 位置：`web-next/src/pages/Chat.tsx:19-20,47-54,149-151,179-187,262-265`；`web-next/src/store/auth.tsx:102-125`
- 证据：`novaveil:chat:current` 保存 `{groupName, messages:[{role, content, ...}]}`（含完整用户 prompt 与模型回复）；`logout()` 只清 React Query 缓存、不清 localStorage；“新会话”只删 `CHAT_SESSION_KEY`，不删 `novaveil:chat:mask-session`。
- 影响：共享浏览器或换人登录后，下一任管理员会看到上一任的完整对话，并可能沿用上一任的 `X-Session-Id` 脱敏/粘合会话。
- 建议：Chat 历史改用 `sessionStorage`，或在 `logout()`/`login()` 前清空全部 `novaveil:chat:*`；优先用服务端对话留存替代本地持久化。
- 确认状态：已由代码事实确认。

### H-03 【交付·构建】全新克隆直接 `go build`/`go run` 失败：`static/out` 未被跟踪

- 位置：`static/static.go:8`（`//go:embed all:out`）、`.gitignore:3`、`README.md:96-98`
- 证据：`git ls-files static` 只有 `static/static.go`，`static/out/*` 被忽略且无 `.gitkeep`；对照实验在无 `out` 目录时编译报 `pattern all:out: no matching files found`；README 的开发路径只写 `pnpm run dev` + `go run main.go start`，干净环境必失败。
- 影响：按文档从源码启动、或任何干净环境直接 `go test ./...`/`go build ./...` 都会在编译期失败（CI 通过只是因为先跑 `pnpm build`）。
- 建议：二选一：(1) 在 `static/out/` 跟踪一个最小 `index.html`（或生成前先占位）；(2) 把“先 `pnpm build`”明确写为源码启动前置步骤，并让 README 与脚本一致。
- 确认状态：已实测复现（临时模块对照实验）。

### H-04 【交付·构建】`web-next/Dockerfile` COPY 引用不存在的 `tailwind.config.ts`

- 位置：`web-next/Dockerfile:30`
- 证据：`COPY tsconfig*.json vite.config.ts tailwind.config.ts postcss.config.js components.json ./`，但 `web-next/tailwind.config.ts` 不存在（项目使用 `@tailwindcss/vite`，无该文件）。
- 影响：`docker compose --profile web-next` 所需的独立前端镜像无法构建，灰度部署路径整体不可用。
- 建议：从 COPY 行删除 `tailwind.config.ts`（或补配置文件），并在 CI 增加 Dockerfile COPY 源 dry-run 校验。
- 确认状态：已确认（文件确实不存在）。

### H-05 【交付·CI】`docker-publish` 每次 push main 自动推送未测试/未扫描的镜像，镜像名已统一为 `novaveil`

- 位置：`.github/workflows/docker-publish.yaml:28,77-82,96-103`
- 证据：工作流只跑构建没有 `go test`/`pnpm test`/Trivy；`push: true` 打 `:latest` 与 `:sha-*`。镜像名最终口径（2026-09-19 用户确认）：统一为 `ghcr.io/kingsunb/novaveil`，main 自动发布 `:latest` 属于预期行为。
- 影响：未审查镜像按 main 自动发布并打 mutable `latest`；`latest` 按用户确认为预期，残余风险是供应链门禁缺失。
- 建议：保留 `ghcr.io/kingsunb/novaveil` 与 main `:latest`；push 前补测试与漏洞门禁；文档/工作流已同步为 `novaveil`。
- 确认状态：已由 workflow 静态确认；2026-09-19 核查时按用户口径将镜像名保留为 `novaveil`。

### H-06 【交付·版本】三处版本号不一致，手工 release 会发布成 v0.1.0 而非 v0.2.0

- 位置：`main.go:5`（`// Version v0.1.0`）、`CHANGELOG.md:39`（`## [0.2.0]`）、`web-next/package.json:3`（`0.1.0`）、`.github/workflows/release.yaml:39,44`
- 证据：release 工作流从 `main.go` 注释 grep 版本并发布为 tag/资产/镜像；当前仓库无 tag；CHANGELOG 已到 0.2.0。
- 影响：手动 release 会把二进制、镜像与 tag 发布为 v0.1.0，与 0.2.0 语义错位。
- 建议：单一版本源，或发布前脚本强校验 `main.go` == `CHANGELOG` == `package.json`；先补 tag 再发版。
- 确认状态：已确认；与历史清单 L-1 相同，属未修复-回归。

---

## 4. 后端安全发现（Security，共 17 = 0H/6M/6L/5I）

### Medium

- **S-M1** 渠道 `BaseURL` 校验只有 http/https 白名单，未拒绝私网/环回/云 metadata 地址 → 管理员会话可成为 SSRF 跳板，且转发时会向上游透传认证头。`internal/op/channel.go:593-610`。
- **S-M2** TLS 反代部署下认证 Cookie 默认无 `Secure`（`security.cookie_secure=false`，`c.Request.TLS==nil` 时 `Set-Cookie` 不置 Secure）。`internal/conf/config.go:110`；`internal/server/middleware/auth.go:56`。
- **S-M3** `/api/v1/channel/fetch-model` 接受未保存渠道的任意 `BaseURL` 直接出站，绕过创建/更新路径的 `validateChannelBaseURL`。`internal/server/handlers/channel.go:176-213`。
- **S-M4** 渠道导出接口一键返回全部上游地址与明文渠道密钥，无二次确认/审计。`internal/server/handlers/channel.go:356-384`。
- **S-M5** 渠道 Key/API Key 明文落库；DB 备份/设置导出只过滤 `auth_jwt_secret`，含全部明文上游/下游凭据（代理凭据 `proxy_url`/`proxy_pool` 同样不过滤）。`internal/model/apikey.go:9`；`internal/op/backup.go:26,683-690`；`internal/model/setting.go:246-262`。= 历史 M-5（有意回退，仍建议在导出/备份处加密或掩码）。
- **S-M6** 错误日志/导出中的 `proxy_url`、`proxy_pool` 凭据未脱敏（校验允许 userinfo）。`internal/op/backup.go:65,683-690`。与 S-M5 同源。

### Low（简）

- **S-L1** API Key 允许管理员提交超短自定义 key（如 `test`），无最小长度/熵校强校验。`internal/server/handlers/apikey.go:46-64`。
- **S-L2** `Authorization` 头不校验 scheme，非 Bearer 值也按 API Key 尝试。`internal/server/middleware/auth.go:69-83`。
- **S-L3** 请求 ID 生成存在取模偏置（非安全凭据，风险极低）。`internal/server/resp/resp.go GenRequestID`。
- **S-L4** `model_filter` 回溯型正则依赖 regexp2 超时兜底；错误日志脱敏并非“格式保持”型全能掩码。`internal/helper/fetch.go:15-19`；`internal/model/setting.go:214-218`。
- **S-L5** `registerRoute` 未知方法字符串默认注册为 GET，未来方法拼错会静默出洞。`internal/server/router/router.go`。
- **S-L6** 初始管理员密码文件在改密前持续存在（0600，但备份/data 目录一起拖走时是明文登录凭据）。`internal/op/user.go:34-78`。

### Info / 已确认无问题

- 无 SQL 注入（GORM 参数绑定）；无命令注入；无 `InsecureSkipVerify`；无 pprof/template 注入；无任意文件读写。
- JWT 密钥 32B `crypto/rand` hex、算法钉死 HS256、24h 封顶、登出轮换 JWT 密钥。
- 登录限速 5 次/15 分钟 DB fail-close；API Key 生成使用 `crypto/rand.Int`。
- 敏感日志（Authorization/X-Api-Key）打印前 8 字符 + `***`；代理日志密码打码。
- 上传导入 128MB/10 万对象双层上限；自更新默认关闭且有 zip 路径穿越防护 + SHA256 校验。

---

## 5. 后端可靠性/架构发现（Reliability，共 15 = 1H/6M/7L/1I）

### High

- **R-H1** 即上文 H-01（模型同步非 2xx 清空自动模型）。

### Medium

- **R-M1** `cache.RefreshAll` 注释宣称“原子替换”，实际逐 shard `clear()` 后 `Set`，读者可观测空窗/混合代数据。`internal/utils/cache/cache.go:64,73-87,118-126`；调用点 `internal/op/channel.go:436-460`、`internal/op/group.go:519-535`。
- **R-M2** `task.StopAll` 的 `WaitGroup.Add`/`Wait` 存在竞态：ticker 与 stopCh 同时就绪时，停止返回后仍可能启动任务 goroutine，在 DB 关闭后写库。`internal/task/task.go:137-160,187-220`。
- **R-M3** eval worker 收尾 defer 是 LIFO：`Notify()` 在 `running.Add(-1)` 之前唤醒调度循环，造成无效唤醒，评估槽位平均多空转至 2s tick。`internal/eval/scheduler.go:139-166`。
- **R-M4** eval `executeTask` 若 panic，`runWorker.recover` 只记日志，`ModelEvalQueueMarkDone` 不会执行；队列行永久 `running`，且入队去重会永久拒收该渠道模型，直到重启。`internal/eval/scheduler.go:157-238`；`internal/op/model_eval_queue.go:149-157`。
- **R-M5** `APIKeyTouchLastUsed` 每个通过鉴权的请求启动一个 goroutine 写 DB：无去抖、无界、SQLite 写入放大。`internal/op/apikey.go:178-185`。
- **R-M6** `sessionStickies` 无条目上限（按客户端请求头可无限增长），且每 60s 在 `routeMu` 内做 O(N) 全量清理。`internal/relay/sticky.go:16-17,72-151`。

### Low（简）

- **R-L1** `ModelEvalQueuePopNext` 条件 UPDATE 后不检查 `RowsAffected`，多实例（MySQL/PG）可重复派发同一评估任务。`internal/op/model_eval_queue.go:104-145`。
- **R-L2** `ModelEvalQueueEnqueue` 并发入队用 `MAX(position)+N` 内存递增，可能产生重复 position，MoveUp/MoveDown 语义不稳。`internal/op/model_eval_queue.go:25-60`。
- **R-L3** `runAsyncProbe` 使用 `context.Background`，shutdown 不取消探测，晚到写回。`internal/relay/prober.go:66-97`。
- **R-L4** `backgroundProbeDueAt` 对已删除分组永不清理，随历史分组数增长。`internal/task/probe.go:19-38`。
- **R-L5** `RecordUsageBucket` 在请求定稿路径同步 UPSERT，无 `WithContext`/无批量，DB 卡住时 goroutine 堆积。`internal/op/usage_bucket.go:32-91`。
- **R-L6** `UserChangePassword` 在 `userMu` 写锁内 bcrypt + DB 事务，阻塞登录读路径。`internal/op/user.go:136-171`。
- **R-L7** 登录限速器 DB 操作全部 `context.Background` 无超时。`internal/server/handlers/loginratelimit.go:65,89,96,113`。= 历史 OLD-13。

### Info

- **R-I1** `cloneRouteState` 未克隆未导出的 `emergencyCounts/emergencyBlocks`，未来读取该快照会踩数据竞争。`internal/relay/route.go:853-870`。

### 已确认安全模式

- 未发现 `time.After` 泄漏（均 `time.NewTimer` 显式 Stop）。
- SQLite WAL/busy_timeout/池上限 4；错误日志队列有界（cap 512）且批量落库。
- 客户端 IP 集合/随机头缓存/自定义代理客户端有上限；shutdown 钩子 LIFO 顺序正确。

---

## 6. 前端 web-next 发现（Frontend，共 16 = 1H/3M/6L/6I）

### High

- **F-H1** 即上文 H-02（Chat 对话与 mask 会话 ID 存 localStorage，登出不清理）。

### Medium

- **F-M1** Chat 页 raw `fetch` 完全不广播 401/403 鉴权失败事件；JWT 过期时其他页面都跳登录，只有 Chat 页只显示“请求失败(401)”，保持已登录外壳。`src/pages/Chat.tsx:86-108`。
- **F-M2** 明文 API Key / 渠道 Key 在 React Query 内存缓存 60s/5min `staleTime`；管理员点一次“查看”，明文在 JS 堆里驻留数分钟。`src/pages/Keys.tsx:495-502`；`src/pages/channels/channel-editor.tsx:844-849`。
- **F-M3** 生产 CSP 保留 `script-src 'unsafe-inline'`；nginx `add_header` 继承语义致 `/assets/` 与 `/__flags/` 丢失 CSP/HSTS/XCTO 等安全头。`web-next/nginx.conf:36-41,65-70,97-101`。= 历史 OLD-22。

### Low（简）

- **F-L1** DB 全量导入后缓存失效列表漏掉 `mask-config/mask-rules/token-trends/usage-heatmap/recent-errors/log-errors`。`src/pages/Settings.tsx:1737-1751`。
- **F-L2** route-loader 双注册表（`route-loaders.ts` vs `page-loaders.ts`）易漂移。`src/lib/route-loaders.ts:15-47`；`src/lib/page-loaders.ts:20-30`。
- **F-L3** 两个 `react-hooks/exhaustive-deps` warnings（`Logs.tsx:760,782`）。
- **F-L4** ThemeProvider 系统主题变化后 context 值陈旧；localStorage theme 无校验。`src/components/layout/ThemeProvider.tsx`。
- **F-L5** RollbackNotice 把 `legacy-path` flag 直接放 href，缺协议白名单（仅运行时 flag 可触达）。`src/App.tsx:300`。
- **F-L6** PriorityInput `cancelQueries({queryKey:["channels"]})` 过宽，保存频繁时取消/刷新风暴。`src/components/ui/priority-input.tsx:59`。

### Info（简）

- Settings 导入文案写“最大 1 MB”，实际 DBDump 限制 128MB（`Settings.tsx:1813` vs `utils.ts:265`）。
- 测试覆盖缺口：无 Login/Chat/Mask/ModelEval 页面测试；coverage include 不含 pages/state。
- SSE 无限重连无上限（30s 封顶，可接受）。
- bundle 预算吃紧（JS 98.3%、CSS 93.6%）。
- 正向：无 `dangerouslySetInnerHTML`/XSS sink；JWT 仅 cookie；eval 沙箱 iframe `sandbox` + 严格 CSP 做得正确；生产无 sourcemap。

---

## 7. 依赖 / DevOps / 文档发现（17 = 4H/4M/6L/3I）

### High

- **D-H1 ~ D-H4** 即上文 H-03（fresh clone 编译失败）、H-04（Dockerfile COPY tailwind 不存在）、H-05（docker-publish 错误镜像名/裸奔上 latest）、H-06（三处版本号不一致）。

### Medium

- **D-M1** 自动触发的 CI 无 `pnpm audit`/`govulncheck` 依赖漏洞门禁；Trivy 只在手动 build/release。`.github/workflows/test.yaml`、`web-next-ci.yaml`。
- **D-M2** `verify-notes.yml` 用 mutable action（`@v4`）+ 无锁 `npx tsx`，Agent Notes 门禁自身不可重现。`.github/workflows/verify-notes.yml:17,22,32-34`。
- **D-M3** `docker/build-push-action@v6` 是发布链唯一未 SHA pin 且拥有 `packages:write` 的关键 action。`.github/workflows/docker-publish.yaml:97`。
- **D-M4** `go.mod` 两个 replace 指向个人 fork 伪版本，`looplj/axonhub/llm` 为单日伪版本；供应链风险集中单一账号。`go.mod:8-9,128,130`（与 Security F-14 信息叠加）。

### Low（简）

- **D-L1** `scripts/run-local.sh` SIGTERM 后 1 秒就 `kill -9`，绕过应用 80s 级优雅关停。`scripts/run-local.sh:39,41`。
- **D-L2** `web-next/Dockerfile` 用 pnpm@9，CI/README 要求 11.21+，且注释不实。`web-next/Dockerfile:24-26`。
- **D-L3** `docker-compose.yml` 可选 profile 的 `${NOVAVEIL_WEB_NEXT_IMAGE:?...}` 是否影响默认路径未实测（沙箱无 docker）。`docker-compose.yml:30`。
- **D-L4** `.gitignore:36` 全局忽略 `pnpm-lock.yaml`，根 TypeScript 工具链不可冻结。
- **D-L5** `scripts/gen_icons.py` 写入已删除的 `web/public/`，是孤儿脚本；`.dockerignore` 残留 `web` 条目。
- **D-L6** `scripts/build.sh` 无 tag 时静默退化为 `dev` 版本而不提示。`scripts/build.sh:7-8`。

### Info

- 前端依赖 16 个过期（3 个大版本）；Go 依赖可更新项多，含 deprecated GCP detector 模块。
- 仓库无 git tag/GitHub Release 记录。
- 正向：`docker-compose.yml` 主服务加固（read_only/cap_drop/no-new-privileges/UID）、`scripts/smoke-test-image.sh` 冒烟链路质量高；主 Dockerfile 固定 Alpine digest；主 compose `stop_grace_period:90s`。

---

## 8. 历史问题清单核对（61 条）

对照 `docs/audits/AUDIT_ISSUES_2026-09-13.md`（25 条）与早前审计报告合并（36 条），当前 HEAD 核对：

| 状态 | 数量 |
|---|---|
| 已修复 | 33 |
| 部分修复 | 9 |
| 未修复-回归 | 19 |
| 无法核验 | 0 |

### 未修复-回归（19 条，一列式）

1. M-5 渠道 Key/API Key 明文落库、无静态加密（有意回退）— `internal/model/apikey.go:9`
2. L-6 单管理员、无 2FA、无操作审计日志 — `internal/server/handlers/user.go`
3. L-7 渠道导入只恢复名称/地址/密钥，类型/模型/分组/限速/标签丢失 — `internal/op/channel.go:100-141`
4. OLD-18 前端 Dockerfile 基础镜像未 pin digest — `web-next/Dockerfile:20,39`
5. OLD-19 web-router 服务缺乏 read_only/cap_drop/security_opt/mem_limit/healthcheck — `docker-compose.yml:126-139`
6. OLD-20 web-router 使用可变镜像标签 nginx:1.27-alpine — `docker-compose.yml:126`
7. OLD-22 独立前端 nginx CSP 仍含 `script-src 'unsafe-inline'` — `web-next/nginx.conf:41`
8. OLD-10 `clientIPSet` 触顶 4096 整体重置、IP 统计归零 — `internal/relay/state.go:1061-1076`
9. OLD-11 `trimFinishedRequestsLocked` 每次定稿 O(N) 全量扫描 — `internal/relay/state.go:629-643`
10. OLD-12 `recoverExpiredItems` 用 `context.Background()`，半开探测不随请求/停机取消 — `internal/relay/route.go:313`
11. OLD-13 loginratelimit 用 `context.Background()` 操作 DB — `internal/server/handlers/loginratelimit.go`
12. OLD-23 CI/CD 无 SAST（gosec/golangci/staticcheck）— `.github/workflows/`
13. OLD-24 无 Dependabot 配置 — 缺 `.github/dependabot.yml`
14. OLD-25 配置 unmarshal 后无验证 — `internal/conf/config.go:79-99`
15. OLD-26 管理 API 无限流（仅登录有限流）— `loginratelimit.go`；相关测试接口最长 30min 可反复触发
16. OLD-27 `.gitignore` 缺 `.env`/`*.pem`/`*.key` — `.gitignore`
17. OLD-28 修改密码表单无二次确认 — `web-next/src/components/auth/ChangePasswordForm.tsx`
18. OLD-30 Settings 仍用原生 `confirm()` — `web-next/src/pages/Settings.tsx:1076`
19. OLD-32 无 CODEOWNERS — 仓库根

### 部分修复（9 条，简述）

- M-1 500 已掩码 + API no-store；4xx 仍透传 `err.Error()` — `resp/resp.go:28-32`
- M-7 单 Key 测试 300s→60s；整体 30min 预算与全局限流仍缺 — `handlers/channel.go`
- L-1 CHANGELOG 已补 `[Unreleased]`；版本号三处仍不齐（= 本报告 H-06）
- L-2 已新增 `/api/v1/stats`；旧 `/api/v1/update/*` 仍保留 — `handlers/update.go`
- OLD-07 keyCursors 删渠道时清理；`channelRefreshCache` 未扫表清理
- OLD-14 `ChannelGetCore` 返回缓存浅拷贝，slice/map 底层共享
- OLD-17 测试文件已补齐；覆盖均衡仍无结论（本次已跑 `go test -cover`，可更新结论：各包覆盖差异较大，`handlers` 仅 7.3%）
- OLD-29 后端已加 http/https scheme 白名单；HTTP 上游仍允许（产品内网形态可接受）
- OLD-31 多数 workflow 已 `persist-credentials:false`；`web-next-ci.yaml` 三处 checkout 仍漏

### 已修复 33 条

含：历史 H-1 敏感 GET 改 POST；H-2/H-3 脱敏会话隔离；H-4 坏 JSON fail-closed；H-5 聊天页不再持有 API Key；M-2 自更新默认关；M-3 Origin 缺省拒绝；M-4 JWT 24h 封顶 + 登出轮换；M-6 BaseURL 只允许 http/https；M-8 mem_limit→2g；M-9/M-10 冷却/粘合/半开探测 WaitGroup；M-11 调试环境变量名；M-12 统计口径解耦；`unsafe.String` 移除（OLD-05 / 历史 Critical C-1）；`time.After` 泄漏（OLD-02）；`sanitizeRequestBody` 三处错误忽略（OLD-01）；批零散低危项。完整证据见审计过程产物 `/tmp/novaveil-audit/05-historical-baseline.md`。

---

## 9. 优先级建议

### P0 —— 会造成交付失败或严重数据/隐私损坏（建议本周期修）

1. **H-01** 模型同步非 2xx 清空自动模型：三个 fetch 函数加上状态码检查，空列表不同步删除。
2. **H-02** Chat localStorage 隐私：登出/登录清 `novaveil:chat:*`，对话迁 `sessionStorage` 或服务端。
3. **H-03** fresh clone 编译失败：`static/out` 放占位文件或明确前置构建。
4. **H-04** `web-next/Dockerfile` 删除不存在的 `tailwind.config.ts`。
5. **H-05** `docker-publish` 保留 `novaveil` 镜像名与 main `:latest`（用户确认的预期行为），push 前补测试/漏洞门禁。
6. **H-06** 版本号三处对齐。

### P1 —— 高危安全问题与可靠性 bug（建议排期）

7. **S-M1/S-M3** SSRF：`validateChannelBaseURL` + 出站 `DialContext` 拒绝私网/环回/metadata；`fetch-model` 表单复用该校验。
8. **S-M2** TLS 反代部署默认开 Secure Cookie 或启动自检警告。
9. **S-M4/S-M5/S-M6** 明文凭据导出：备份/导出支持可选加密或至少对 `proxy_url` 脱敏，增加查看/导出审计日志。
10. **F-M3** 移除 `script-src 'unsafe-inline'`；nginx 每个 location 显式重发安全头。
11. **R-M1** cache RefreshAll 改快照 + 原子指针或读写锁。
12. **R-M2** task.StopAll 增 `loopWG` 修复 WaitGroup 竞态。
13. **R-M3/R-M4** eval 调度器：defer 顺序修正 + `executeTask` 兜底 MarkDone/ResetRunning，避免队列行永久 running。
14. **F-M1** Chat 页 401/403 接入统一鉴权失败广播。

### P2 —— 加固与工程化（可批量做，部分已有历史结论）

15. **R-M5** APIKeyTouchLastUsed 去抖/批量；**R-M6** sessionStickies 加上限 + 增量清理；**D-M1** CI 加 `pnpm audit`/`govulncheck`；**D-M2/M3** action SHA pin；**D-M4** fork 依赖固化为可追溯 tag 或 vendor。
16. 补 SAST（gosec/golangci-lint）、Dependabot、CODEOWNERS、`.gitignore` 敏感文件模式。
17. 管理 API 全局限流（登录以外的重活如测 Key/同步）；配置文件 unmarshal 后校验。
18. 前端补 Login/Chat/Mask/ModelEval 页面测试；清理 route-loader 双注册表；修 2 个 lint warnings。
19. 修 mulit-instance eval 队列 `RowsAffected` 与 enqueue 并发 position。
20. 清理孤儿脚本/旧路由/旧统计接口；发布周期化 `pnpm outdated`/`go list -m -u all`。

---

## 10. 审计局限

- 沙箱无 docker：Compose/镜像仅静态确认；`docker compose config`、镜像构建、冒烟未执行。
- 本地 npm 镜像（npmmirror）无 audit 端点：前端依赖 CVE 无法与官方公告核对，只做静态与可更新性判断。
- 未运行 `go test -race`（编译期竞态点仅静态确认）。
- 后端共 247 个 Go 文件、4.7 万行；深度审计覆盖核心链路 + 全库反模式 grep，非逐行全部阅读，可能存在未覆盖冷路径。
- 供/需链风险判断建立在“当前 HEAD 与 go.sum 完整”的基础上，未做供应商仓库内容核对。

---

## 附录 A：执行命令记录

```text
cd /workspace/NovaVeil
git status --short                 # 干净，HEAD 7a8b6e0
go build ./...                     # 通过（依赖 static/out 存在）
go test ./... -cover               # 全 ok
go vet ./...                       # 通过
go mod verify                      # all modules verified
cd web-next
pnpm build                         # 通过
pnpm typecheck                     # 通过
pnpm lint                          # 0 errors / 2 warnings
pnpm vitest run                    # 46 files / 506 tests passed
pnpm size-limit                    # pass（余量小）
pnpm outdated                      # 16 个可更新
pnpm audit                         # npmmirror 无 audit 端点，失败
bash -n scripts/*.sh scripts/dockerfile/entrypoint.sh  # 全部通过
```

## 附录 B：过程产物

- `/tmp/novaveil-audit/01-backend-security.md`
- `/tmp/novaveil-audit/02-backend-reliability.md`
- `/tmp/novaveil-audit/03-frontend.md`
- `/tmp/novaveil-audit/04-devops-deps-docs.md`
- `/tmp/novaveil-audit/05-historical-baseline.md`
- `/tmp/novaveil-audit/go-list-all.txt`、`go-vet.txt`、`vitest.log` 等证据文件