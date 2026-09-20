# NovaVeil 后续审计报告（2026-09-20）

- **审计对象**: `/workspace/NovaVeil` @ HEAD `f52aaeb`（`main`，working tree 干净）
- **审计方式**: 只读审计。未修改任何产品代码；新增本报告文件。
- **审计方法**: 并行子代理分域审计（后端安全、中继/脱敏、可靠性、前端、DevOps）+ 高价值发现对抗复核。
- **上一份基线**: `docs/audits/2026-09-18-full-audit.md`（审计对象为 `7a8b6e0`）。

> 说明：本次为停电前快速归档。历史问题清单的 61 条逐条核对子代理未在截稿前返回；
> 历史部分仅记录已由代码事实确认的 High 项与主域复核过程中确认的条目。DevOps 域结论
> 来自该域子代理的中期回传 + 主代理本人对关键文件的核读。

---

## 0. 执行摘要

**没有新的 Critical。** 上次的 High 项大部分已修复：H-01（模型同步清库）在 fetch 层与 sync 层双防御；H-02（Chat localStorage）已在 login/logout 双侧清理；H-03（fresh clone 编译失败）已由 `static/out/.gitkeep` 修复；H-04（Dockerfile tailwind COPY）已修复；H-06（版本号不一致）已修复（`main.go` 与 `web-next/package.json` 均为 0.2.0，release workflow 已删除）。**H-05 部分保留（DevOps 复核记为 high，见 DEV-01）**：`docker-publish` 仍在不跑测试/漏洞门禁的情况下推 `:latest`，且 `docker/build-push-action@v6` 未 pin SHA；DEV-06 发现 PR 模板检查与 PR 模板不一致，按模板填写的合法 PR 会被自动关闭。

本次最值得关注的新发现：

- **REL-02【中继·新·对抗复核确认】透传路径转发上游 `Set-Cookie`，可驱逐管理员 auth cookie（登出型）**：`copyUpstreamHeaders` 对透传响应原样复制全部上游响应头（流式仅剔 `Content-Length`）。恶意/被攻破的透传渠道注入 `Set-Cookie: auth=...; Path=/` 会替换浏览器中的管理认证 cookie。对抗复核确认 medium：影响是**登出/拒绝服务**（攻击者无法伪造有效 JWT 直接提权），命中条件是“透传路径 + 浏览器带 auth cookie 访问 `/api/v1/chat` 等”；转换协议路径不复制上游响应头，不受影响（详见 2.1）。
- **REL-04【脱敏·确认】流式还原漏 `reasoning_content`，且 tool-call arguments 不做跨 chunk 缓冲**：脱敏开启时，DeepSeek/Qwen 等推理字段的占位符会原样发给客户端；tool 参数跨事件拆分时同样漏还原。对抗复核已确认 medium。
- **REL-05【脱敏·确认】脱敏映射表按客户端 `X-Session-Id` 命名空间，无 API Key 前缀**：两个 API Key 使用相同会话 ID 时共享一张 Mapping，存在条件性的跨租户还原泄漏。对抗复核已确认 medium（有前提：非空会话 ID 共享 + 占位符出现在他人响应中）。

其余后端安全、可靠性、前端发现大多为上一份报告已列问题的存续（SSRF、明文凭据、Secure cookie、eval/task/cache 竞态、Chat 401 不广播、CSP 等），阵容稳定，没有恶化迹象。

---

## 1. 已验证修复清单（截稿前确认）

| ID | 结论 | 证据（当前代码） |
|---|---|---|
| H-01 | **已修复** | `internal/helper/fetch.go:115-120,167-179,230-242` 三个 fetch 函数解码前校验 2xx；`internal/task/sync.go:60-77` 空列表且已有 auto 模型时跳过删除。 |
| H-02/F-H1 | **已修复** | `web-next/src/store/auth.tsx:39-50` `clearChatLocalStorage()` 删除所有 `novaveil:chat:*`；`login`（111-114）与 `logout`（143）均调用。 |
| H-03 | **已修复** | `static/out/.gitkeep` 已被跟踪（`git ls-files static` 可见），`static/static.go:8` 的 `//go:embed all:out` 有目录可嵌。 |
| H-04 | **已修复** | `web-next/Dockerfile:30` 的 COPY 行不再含 `tailwind.config.ts`。 |
| H-05 | **部分保留（复核 high）** | `docker-publish.yaml:82-89` 仍只构建即 `push: true`，无 `go test`/`pnpm test`/漏洞门禁；`:83` 仍用 `docker/build-push-action@v6` 未 pin。镜像名已统一为 `ghcr.io/kingsunb/novaveil`。 |
| H-06 | **已修复** | 产品版本源对齐：`main.go:5`、`CHANGELOG.md:39`、`web-next/package.json:4` 均为 `0.2.0`（根 `package.json:3` 的 `0.1.0` 是私有 agent-notes 工具，非产品版本）；`6d61562` 删除 release workflow 后，旧 release 误版本影响消失，当前 release 自动化缺席而非错版。 |
| 脱敏三层开关默认关 | **未退化** | `model/DefaultMaskConfig()` `Enabled:false`；组级 `MaskEnabled` 默认 false；`applyRequestMask` 任一关即短路。 |
| Bad mask JSON fail-closed | **已修复** | `internal/op/mask.go` 解析失败返回默认关闭配置，不拒绝中转。 |
| H-1/H-2/H-3 历史脱敏会话 | **已修复** | 空会话键请求级 Mapping 不入表（`mask/session.go:108-113`）；命名会话 30 分钟 TTL。 |
| 探针对齐真实转发协议 | **已修复** | `internal/relay/test.go` 测试/诊断路径走 OpenAI Chat `/v1/chat/completions`（f52aaeb 主验证）；熔断半开探测 `max_tokens=1` 主验证。 |
| 评估单渠道最多 10 密钥 | **已修复** | `internal/relay/test.go` `evalKeyAttemptLimit=10`（9eed29d）。 |

---

## 2. 打开的高优先级问题

### 2.1 REL-02 透传路径转发上游 Set-Cookie（新发现，对抗复核确认 medium）

- **位置**: `internal/relay/handler.go:600-604`（调用）、`:1018-1028`（实现）；透传响应头来源 `internal/relay/upstream.go:108-114`（非流）与 `:170-175`（流）。
- **事实**: `copyUpstreamHeaders` 对**透传（pass-through）响应**原样复制全部上游响应头到客户端响应；流式分支仅剔除 `Content-Length`。`sendPassthrough` 会上行 `result.header`；`sendConverted` 返回的上游响应不携带 header，因此**转换协议路径不复制上游响应头**，不受影响。补充澄清：`/v1/*` 由 `APIKeyAuth` 认证、不经 cookie（`middleware/auth.go:69-93`），普通 API-key 客户端功能上不受 `Set-Cookie` 影响；只有“浏览器携带 ambient auth cookie + 透传响应”的组合才会发生驱逐。
- **风险**: 恶意/被攻破的透传渠道返回 `Set-Cookie: auth=...; Path=/` 时，网关会原样转给同源浏览器。管理 cookie 名为 `auth`、Path 为 `/`、无 Domain（host-only）、SameSite=Lax、HttpOnly（`middleware/auth.go:17-22,55-61`），SameSite/HttpOnly/Secure 均不能阻止同源响应里的 Set-Cookie 覆盖，因此该 cookie 会被驱逐。**对抗复核确认影响为登出/拒绝服务**：攻击者拿不到 JWT 签名密钥，无法靠这个头直接伪造有效会话或提权。`/api/v1/chat/completions` 是 cookie 认证且走 `Forward`，当所选渠道为透传时命中；`/v1/*` 是 API Key 认证，API-key 客户端不依赖 auth cookie，但浏览器若携带 ambient auth cookie 访问相关端点仍可能被覆盖。
- **建议**: 响应头改 allowlist（Content-Type、Cache-Control、x-request-id、openai-*、anthropic-* 等）；永远丢弃 `Set-Cookie`/`Set-Cookie2`/`Location`/`WWW-Authenticate`/逐跳头；流式继续剔 `Content-Length`。修复优先级维持 P0（会话可用性），但不应营销为账户接管漏洞。

---

## 3. 后端安全（Security）

与 9-18 审计相比无新 Critical；六个 Medium 基本原样存续：

- **SEC-01 = S-M1（medium）**：渠道 URL 校验只查 http/https scheme 与 host 存在，未拒绝私网/环回/metadata（`internal/op/channel.go:612-624`）；`/api/v1/channel/fetch-model` 对未保存渠道使用提交的 `BaseURL` 且不调用该校验（`handlers/channel.go:176-214`）。管理后台 SSRF 仍存在。
- **SEC-02 = S-M2（medium）**：TLS 反代下 `c.Request.TLS == nil`，`cookie_secure` 默认 false 时不设 Secure（`middleware/auth.go:55-61`）。
- **SEC-03 = S-M4（medium）**：渠道导出接口仍一键返回全部明文上游密钥（`handlers/channel.go:353-380`）。
- **SEC-04 = S-M5/S-M6（medium）**：API Key/渠道 Key/代理凭据明文落库；备份导出只滤 `auth_jwt_secret`（`op/backup.go:683-692`）。
- **SEC-05 = S-L1（low）**：自定义 API Key 无最小长度/熵校验。
- **SEC-06 = S-L2（low）**：`Authorization` 不校验 scheme，非 Bearer 值也尝试按 key 查。
- **SEC-07 = S-L5（low）**：路由注册未知 method 静默落入 GET（`router/router.go:158-160`）。
- **SEC-08 = S-L6（low）**：初始管理员密码文件在改密前长期存在（`op/user.go`）。

---

## 4. 中继 / 脱敏（Relay & Mask）

除 REL-02 外：

- **REL-01 = R-M6（medium）**：`sessionStickies` 无条数上限，默认粘合开启，客户端可无限灌 `X-Session-Id` 增长内存（`relay/sticky.go:16-17,87-95`）。
- **REL-03（medium，新）**：`Forward` 顶层选出 `item` 后调用 `resolveGroupRefChain` 未传 `format`，嵌套 PreferPassthrough 失效；测试直接给 `resolveGroupRefChain` 传 format，未覆盖真实调用点。
- **REL-04（medium，对抗复核确认）**：流式还原只处理 `delta.content`/`delta.text`/Responses `delta`，漏 `choices.0.delta.reasoning_content`；`restoreToolCallArgs` 按事件整词替换不缓冲，跨 chunk 拆分的占位符泄漏。脱敏开启时触发。
- **REL-05（medium，对抗复核确认）**：脱敏 Mapping 全局按原始 `X-Session-Id`/`x-opencode-session` 命名空间，无 API Key 前缀/哈希；两个 Key 共享会话 ID 时共享 Mapping，条件性跨租户还原泄漏。空键已隔离，30min TTL 存在但不足以构成隔离边界。
- **REL-06（low）**：面板测试请求 `max_tokens=100000` 且跳过每 Key RPM，管理后台单次测试可产生大额计费（`relay/test.go:197-245`）。
- **REL-07 = R-I1（info）**：`cloneRouteState` 未克隆 `emergencyCounts/emergencyBlocks`，快照若被遍历存在潜在数据竞争。
- **正向确认**：`/v1` 路由有 APIKeyAuth；请求侧 `MergeInboundRequest`/上游库会剥 `Authorization`/`Host`/`TE`；完全透传不跳过路由、failover、Key 轮询的产品不变式仍成立。

---

## 5. 后端可靠性（Reliability）

- **RELI-01 = R-M2（medium）**：`task.StopAll` 的 WaitGroup 竞态仍在（`task/task.go:137-157,185-218`）。
- **RELI-02 = R-M4（medium）**：eval worker panic 只记日志，队列行永久 `running`（`eval/scheduler.go:157-168`）。
- **RELI-03 = R-M3（low）**：eval worker 的 defer LIFO 使 Notify 在 running 递减前触发，无效唤醒。
- **RELI-04 = R-L1（low）**：`ModelEvalQueuePopNext` 条件 UPDATE 不看 RowsAffected，多实例可重复派发。
- **RELI-05 = R-L2（low）**：eval 入队 position 由未加锁 `MAX(position)+N` 生成。
- **RELI-06 = R-M1（medium）**：`cache.RefreshAll` 逐 shard 清空重建，读者可见空窗/混合代。
- **RELI-07 = R-M5（medium）**：`APIKeyTouchLastUsed` 每请求起一个 goroutine 直写 DB，无去抖无上限。
- **RELI-08（medium）**：shutdown 每钩 10s 超时，eval 单个任务最长 10 分钟，DB 可能先关而后写。
- **正向确认**：H-01 的数据清库路径已双向堵住；`go test` 主验证相关包通过。

---

## 6. 前端（web-next）

- **FE-01 = F-M1（medium）**：`Chat.tsx:86-107` 仍用裸 `fetch`，401/403 不广播全局鉴权失败，过期会话留在已登录壳里。
- **FE-02 = F-M2（medium）**：明文 API Key/渠道 Key reveal 后仍在 React Query 缓存（staleTime 60s/5min）与编辑态残留。
- **FE-03 = F-M3/OLD-22（medium）**：独立 `web-next/nginx.conf` CSP 仍含 `script-src 'unsafe-inline'`；nginx `add_header` 继承语义致 `/assets/`、`/__flags/` 丢失安全头。
- **FE-04 = F-L1（low，扩展）**：DB 导入后仍未失效 mask/usage/heatmap/log-errors/model-eval 等 query family。
- **FE-05 = F-L4（low）**：系统主题变化后 context 陈旧。
- **FE-06 = F-L5（low）**：`legacy-path` flag 直接放入 `<a href>`，缺协议白名单。
- **FE-07 = F-L2（low）**：route-loader 双注册表仍易漂移。
- **正向确认**：F-L3 两个 lint warnings 已修复（`pnpm lint` 0 警告，提交 `33477ba`）；H-02 修复属实。

---

## 7. DevOps / CI / 版本

DevOps 域完整子代理复核已补录。DEV-01 与上期 H-05 同源、按 high 跟进；其余为交付链/供应链硬化。

- **DEV-01 = H-05（high）**：`docker-publish.yaml` main push 即构建并推 `:latest`/`:sha-*`，`push:true` 前无 `go test`/`pnpm test`/漏洞扫描门禁；`:83` 的 `docker/build-push-action@v6` 为 mutable tag 未 pin SHA。
- **DEV-02（medium）**：自动 CI 无 `pnpm audit`/`govulncheck`/SAST；Trivy/SBOM 仅手动 `build.yaml`（`workflow_dispatch`）。
- **DEV-03（medium）**：`web-next/Dockerfile:20,39` 基础镜像 `node:22-alpine`、`nginx:1.27-alpine` 未 pin digest。
- **DEV-04（medium）**：`go.mod:16` 直接依赖 `github.com/looplj/axonhub/llm` 伪版本；`:128`、`:130` 两个 `replace` 指向个人 fork 伪版本。
- **DEV-05（low～medium）**：可选 `web-router` profile（`docker-compose.yml:125-139`）缺少主服务的 `read_only`/`cap_drop`/`security_opt`/healthcheck，并以 `nginx:1.27-alpine` 作反代。
- **DEV-06（medium，新）**：`template-check.yaml:113-124` 的 requiredLines 含 `- [x] 本次 PR 不包含测试文件`，但 `.github/pull_request_template.md` 第 2 项是“契约/行为修复包含对应测试；纯文档或纯文案 PR 才可以不带测试文件”。按模板填写的合法 PR 会被 `pull_request_target` 检查误判为不合规并自动关闭。已核实现存代码。
- **DEV-07（low）**：`verify-notes.yml:17,22` 用 `actions/checkout@v4`/`actions/setup-node@v4` 未 pin SHA；`:32-34` 每次 `npx tsx` 现拉依赖、无 frozen lock，门禁结果可随网络/上游漂移。
- **DEV-08（low）**：`.gitignore` 未覆盖 `.env` / `*.pem` / `*.key`（`.dockerignore` 只挡镜像构建路径）；当前仓库未发现已跟踪密钥，建议补充。
- **文档漂移（low）**：`6d61562` 删除 release/deploy workflow 后，`README.md:64` 仍称可手动触发 `release` / `build` workflow，`docs/SECURE_DEPLOYMENT.md:40-43` 也仍引用不存在的 `release` workflow。
- **DEV-09（low，新）**：`web-next/Dockerfile:23-27` 写死并启用 pnpm@9，而 CI/docs 当前要求 pnpm 11.21+，独立前端镜像的工具链与 CI 不一致；`README.md:64-72` 与 `docs/SECURE_DEPLOYMENT.md:40-43` 仍引用已被 `6d61562` 删除的 release workflow。
- **正向确认**：`test.yaml` 在 PR/push 跑 Go 测试并 pin checkout（SHA）；`web-next-ci` 在 web-next 变更的 PR 上运行；`docker-publish` 镜像名正确为 `ghcr.io/kingsunb/novaveil`；checkout 等多数 action 已 pin。
- **残余风险（DevOps 子代理）**：生产 `novaveil` 服务硬化良好（UID 10001、read_only、cap_drop ALL、no-new-privileges、limits、healthcheck），但这些控制未覆盖 web-router；`docker-compose.yml:30` 的 `NOVAVEIL_WEB_NEXT_IMAGE` 在未启用 profile 的 Compose 上可能因插值语义导致默认 `up` 失败（未实测，INFERENCE）；`docker-compose.local.yml:10-12` 用 `NOVAVEIL_BIND` 而 `docs/SECURE_DEPLOYMENT.md:90-92` 用 `NOVAVEIL_BIND_ADDRESS`，是运维 footgun；release/update 完整性仍只靠 checksum、无 Sigstore 签名。

---

## 8. 历史清单核对状态（截稿前未完成完整核对）

已确认修复/存续的部分见第 1 节。以下上期“未修复-回归”项在本次主域复核中被再次观察到仍未修复（其余未逐一复核）：

- M-5 / L-6 / L-7（明文凭据、单管理员无 2FA、渠道导入信息丢失）— 存续。
- OLD-10、OLD-11、OLD-12、OLD-13（IP 集重置、定稿全量扫描、探测 context.Background、loginratelimit context.Background）— 观察存续。
- OLD-18、OLD-19、OLD-20、OLD-22（前端镜像 pin、web-router 硬化、nginx tag、CSP）— 观察存续。
- OLD-23、OLD-24、OLD-26、OLD-27、OLD-28、OLD-30、OLD-32（SAST、Dependabot、管理 API 限流、`.env` 忽略、改密二次确认、Settings `confirm()`、CODEOWNERS）— 未逐条复核，按本次观察多数存续。
- OLD-29（http/https 白名单）已修复。

---

## 9. 优先级建议

### P0（建议本周期）

1. **REL-02**：上游响应头 allowlist，禁止转发 `Set-Cookie`/`Location`/`WWW-Authenticate`/逐跳头。
2. **REL-04**：流式还原补 `reasoning_content`，tool-call arguments 按槽位缓冲还原。
3. **REL-05**：脱敏会话键加 API Key 隔离（`apiKeyID + ":" + sessionKey` 或 HMAC）；粘合键单独评估上限。
4. **REL-06**：面板测试请求降低 `max_tokens`，并给诊断路径加计费预算。

### P1

5. **SEC-01**：SSRF 统一出口校验（含 fetch-model、导入、测试、eval、relay 拨号）；解析后拒绝私网/环回/metadata。
6. **REL-01**：`sessionStickies` 加条目上限（LRU 或满时拒新）。
7. **RELI-02**：eval worker panic 兜底置队列失败态，避免永久 `running`。
8. **H-05**：docker-publish 增加 tests + 漏洞门禁，暂不推未测镜像。
9. **FE-01**：Chat 页接入统一 401/403 广播。

### P2

10. 明文凭据的导出/备份加固（脱敏导出、可选加密、审计日志）。
11. `cache.RefreshAll` 原子替换；`APIKeyTouchLastUsed` 去抖。
12. 前端口导入后 query invalidation 补齐；CSP 去除 `unsafe-inline` 与 nginx 子 location 头部继承修复。
13. eval 队列 `RowsAffected`、position 分配并发修复。

---

## 10. 审计局限

- DevOps 完整子代理复核与 REL-02 独立对抗复核已在本修订版补充（见 2.1 与第 7 节）；历史清单 61 条逐条核对子代理仍未返回。
- 未跑 docker 基线的镜像构建/冒烟；静态结论仍可能受环境差异影响。
- 后端全量文件并非逐行审阅，深度覆盖核心链路与全库反模式 grep。
- 所有“存续”结论基于当前代码片段与行号，未做动态攻击复现（SSRF、DNS rebinding、Set-Cookie 注入等）。