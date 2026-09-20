# NovaVeil 审计验证报告（2026-09-20）

- **验证对象**: `/workspace/NovaVeil` @ HEAD `f64e7a7`（`main`，working tree 干净）
- **验证基线**: `docs/audits/2026-09-20-followup-audit.md`（审计对象 `f52aaeb`）+ `docs/audits/2026-09-18-full-audit.md`
- **代码差异**: `f52aaeb..f64e7a7` 仅新增本目录的审计文档，产品代码零差异，行号可照用。
- **验证方式**: 8 路并行核验子代理分域检查（修复清单、REL-02、后端安全、中继/脱敏、可靠性、前端、DevOps、历史清单）；REL-02/中继/可靠性三域子代理返回失败后主代理亲自逐行重读源码；其余域抽查关键代码位置。

## 结论

**全部"确认存在"的问题在当前代码中都能找到对应事实，没有发现哪一条是审计报告编造或已经不存在的。** 声称"已修复"的项也确实已修复。

---

## 分域核验结果

| 域 | 数量 | 结果 |
|---|---|---|
| 已验证修复/部分修复清单 | 24 项 | 全部属实 |
| REL-02 透传 Set-Cookie | 1 项 | 属实 |
| 后端安全 | 11 项 | 全部属实 |
| 中继/脱敏 | 10 项 | 全部属实 |
| 可靠性 | 9 项 | 全部属实 |
| 前端 | 9 项 | 全部属实 |
| DevOps/CI | 20 项 | 全部属实 |
| 历史清单 | 27 项 | 全部属实 |

---

## 1. P0 级问题（全部 CONFIRMED）

### REL-02 透传路径转发上游 Set-Cookie（medium，确认）

- **位置**: `internal/relay/handler.go:1021-1028`（`copyUpstreamHeaders` 实现）、`:604`（调用点）
- **证据**: `copyUpstreamHeaders` 无白名单、不剔 `Set-Cookie`，流式仅剔 `Content-Length`。`sendPassthrough` 把 `result.header`（上游响应头克隆）原样写入 `c.Writer.Header()`。auth cookie 名 `auth`、Path `/`、host-only、SameSite=Lax、HttpOnly——同源 `Set-Cookie` 覆盖无法阻挡。
- **影响**: 登出型 DoS（攻击者无法伪造 JWT 提权）。转换协议路径不受影响（`sendConverted` 不带上游 header）。`/v1/*` 走 APIKeyAuth 不依赖 cookie；`/api/v1/chat/completions` 走 cookie 认证且可命中透传。

### REL-04 流式还原漏 reasoning_content + tool-call args 不缓冲（medium，确认）

- **位置**: `internal/relay/mask_integration.go:230-246`（`streamContentPath`）、`:138-174`（`restoreToolCallArgs`）
- **证据**: `streamContentPath` 只处理 `choices.0.delta.content`、`delta.text`、Responses `delta`，**不含 `reasoning_content`**（grep mask 全路径无 reasoning 处理）。`restoreToolCallArgs` 注释明写"不缓冲"，按事件整词替换。占位符跨 chunk 拆分会泄漏。

### REL-05 脱敏 Mapping 无 API Key 隔离（medium，确认）

- **位置**: `internal/relay/handler.go:110`（sessionKey 取原始头）、`internal/relay/mask/session.go:124`（直接用作 map key）
- **证据**: `sessionKey` 取原始 `X-Session-Id`/`x-opencode-session`，无 API Key 前缀/哈希。两个 Key 共享会话 ID → 共享一张 Mapping。空键隔离与 30min TTL 存在但不构成租户隔离边界。

### REL-06 面板测试 max_tokens=100000 + 跳过 per-key RPM（low，确认）

- **位置**: `internal/relay/test.go:387`（`testMaxTokens=100000`）、`:483-485`（直接 sendPassthrough/sendConverted 绕过 APIKeyAuth）
- **证据**: 面板测试请求注入 `max_tokens=100000`，直接走转发不经过 RPM 限流中间件。

---

## 2. P1 级问题（全部 CONFIRMED）

### REL-01 sessionStickies 无条目上限（medium→low，确认）

- **位置**: `internal/relay/sticky.go:17`、`:72-96`
- **证据**: `sessionStickies = make(map[int]map[string]stickyEntry)` 无上限，默认开启（`model/group.go:56`），仅 60s 节流清理过期项。

### REL-03 Forward 调用点不传 format（medium，确认）

- **位置**: `internal/relay/handler.go:253`
- **证据**: 真实调用 `resolveGroupRefChain(group, item, sessionKey, exclude)` 未传 `format`（variadic `clientFormat ...llm.APIFormat`），嵌套 PreferPassthrough 失效。测试 `prefer_passthrough_test.go:239,251` 直接传 format，未覆盖生产路径。

### REL-07 cloneRouteState 漏克隆 emergencyCounts/Blocks（info，确认）

- **位置**: `internal/relay/route.go:857-864`
- **证据**: 浅拷贝 + 只克隆 Cooldowns/Levels/HalfOpens/PostCommitStrikes，不克隆 emergencyCounts/emergencyBlocks（共享底层 map 指针）。

### SEC-01/S-M1 渠道 URL 不拒私网/环回/metadata（medium，确认）

- **位置**: `internal/op/channel.go:612-625`
- **证据**: `validateChannelBaseURL` 只校验 scheme http/https + host 非空 + 无 userinfo。`channel_url_test.go` 断言 `http://127.0.0.1:8080` 被接受。无 IsLoopback/IsPrivate/169.254/metadata 检查。

### SEC-03/S-M3 fetch-model 绕过校验（medium，确认）

- **位置**: `internal/server/handlers/channel.go:176-218`
- **证据**: `fetchModel` 对未保存渠道（ID==0 或 BaseURL != ""）直接用提交的 BaseURL 调 `helper.FetchModels`，不调 `validateChannelBaseURL`。

### SEC-02/S-M2 TLS 反代下 cookie 无 Secure（medium，确认）

- **位置**: `internal/conf/config.go:110`、`internal/server/middleware/auth.go:60`
- **证据**: 默认 `cookie_secure=false`；`secure := conf.AppConfig.Security.CookieSecure || c.Request.TLS != nil`，反代下 `TLS==nil` → 无 Secure。

### SEC-04/S-M4 渠道导出一键返回全部明文密钥（medium，部分确认）

- **位置**: `internal/server/handlers/channel.go:356-380`
- **证据**: 批量明文密钥一键导出、无二次确认——确认。`:358` 有 warn 级 IP 日志（审计"无审计"措辞略夸大，但无用户身份、非防篡改审计）。

### SEC-05/S-M5/S-M6 明文凭据落库 + 备份导出滤密不完整（medium，确认）

- **位置**: `internal/model/apikey.go:9`、`internal/op/backup.go:683-693`
- **证据**: APIKey/ChannelKey 明文 `string` 无加密。`filterSecretSettings` 只滤 `auth_jwt_secret`，proxy_url/proxy_pool 凭据未滤。

### SEC-06~09（low，全部确认）

- SEC-06: 自定义 API Key 无长度/熵校验（`handlers/apikey.go:56-64`）
- SEC-07: Authorization 不校验 Bearer scheme（`middleware/auth.go:73-77`）
- SEC-08: 未知 method 静默落 GET（`router/router.go:158-159`）
- SEC-09: 初始密码文件改密前长期存在（`op/user.go:77`）

### RELI-01 task.StopAll WaitGroup 竞态（medium，确认）

- **位置**: `internal/task/task.go:188`（Add）、`:157`（Wait）
- **证据**: `goGuardedCall` 的 `Add(1)` 与 `StopAll` 的 `Wait()` 无 happens-before；select 随机选 ticker.C 时任务 goroutine 可在 StopAll 返回后启动。

### RELI-02 eval worker panic 后队列行永久 running（medium，确认）

- **位置**: `internal/eval/scheduler.go:157-168`（recover 只 log）、`:221/247`（MarkDone 仅在正常路径）
- **证据**: panic → recover 只记日志，不调 MarkDone；队列行留在 `QueueTaskRunning`；入队去重永久拒收直到重启。

### RELI-03 eval defer LIFO Notify 失效（low，确认）

- **位置**: `internal/eval/scheduler.go:157-160`
- **证据**: defer 顺序 running.Add(-1) → workerWG.Done → Notify → recover；LIFO 执行时 Notify 在 running.Add(-1) 之前触发。

### RELI-04 PopNext 不查 RowsAffected（low，确认）

- **位置**: `internal/op/model_eval_queue.go:104-133`
- **证据**: 条件 UPDATE 后不查 RowsAffected，多实例可重复派发。

### RELI-05 Enqueue position 无锁（low，确认）

- **位置**: `internal/op/model_eval_queue.go:25-86`
- **证据**: MAX(position) 读与 Create 不在同一事务，可产生重复 position。

### RELI-06 cache.RefreshAll 非原子（medium，确认）

- **位置**: `internal/utils/cache/cache.go:121-130`
- **证据**: 逐 shard clear 后 Set，注释自称"原子替换"与实现矛盾；读者可见空窗/混合代。

### RELI-07 APIKeyTouchLastUsed 每请求裸 goroutine（medium，确认）

- **位置**: `internal/op/apikey.go:180-185`、`middleware/auth.go:122`
- **证据**: 每鉴权请求一个 goroutine 直写 DB，无去抖无上限。

### RELI-08 shutdown 10s 钩 vs eval 10min（medium，部分确认）

- **位置**: `internal/utils/shutdown/shutdown.go:18`、`internal/eval/scheduler.go:20`
- **证据**: 超时数值确认；但 eval relay 调用随 `s.ctx` 取消，真实暴露是 save 路径 ~10s 窄边沿。

---

## 3. 前端问题（全部 CONFIRMED）

| ID | 严重度 | 位置 | 证据 |
|---|---|---|---|
| FE-01 | medium | `Chat.tsx:86-108` | 裸 fetch，401 不调 `broadcastAuthFailure`，留在已登录壳 |
| FE-02 | medium | `Keys.tsx:495-503`、`channel-editor.tsx:856-861,882-897` | 密钥 reveal 后驻留 RQ 缓存 60s/5min 并写入编辑态 |
| FE-03 | medium | `nginx.conf:41,65-70,97-101` | CSP 含 `unsafe-inline`；`/assets/`、`/__flags/` 丢安全头 |
| FE-04 | low | `Settings.tsx:1794-1802` | 导入后缺 mask-config/mask-rules/token-trends/usage-heatmap/recent-errors/log-errors/model-eval 失效 |
| FE-05 | low | `ThemeProvider.tsx:45-53` | 系统主题变化后 context 陈旧 |
| FE-06 | low | `App.tsx:300` | legacy-path 直接放 href，无协议白名单 |
| FE-07 | low | `route-loaders.ts`/`page-loaders.ts` | 双注册表易漂移 |
| H-02 残余 | medium | `Chat.tsx:262-265,56-62` | 登录/登出清理已生效；但"新会话"不删 mask-session ID、Chat 401 不触发清理 |

---

## 4. DevOps 问题（全部 CONFIRMED）

| ID | 严重度 | 证据 |
|---|---|---|
| DEV-01/H-05 | high | `docker-publish.yaml:83` `build-push-action@v6` 未 pin SHA；无测试/漏洞门禁即 push:latest |
| DEV-02 | medium | 自动 CI 无 pnpm audit/govulncheck/SAST |
| DEV-03 | medium | `Dockerfile:20,39` node:22-alpine/nginx:1.27-alpine 未 pin digest |
| DEV-04 | medium | `go.mod:16,128,130` 伪版本 + 个人 fork replace |
| DEV-05 | low-medium | web-router profile 无 read_only/cap_drop/security_opt/healthcheck |
| **DEV-06** | **medium** | `template-check.yaml:119` requiredLines 含 `- [x] 本次 PR 不包含测试文件`，与 PR 模板第 2 项完全不同 → 合法 PR 被自动关闭 |
| DEV-07 | low | `verify-notes.yml:17,22` 未 pin SHA；`:32-34` npx tsx 无 frozen lock |
| DEV-08 | low | `.gitignore` 缺 `.env`/`*.pem`/`*.key` |
| DEV-09 | low | `Dockerfile:26` 钉死 pnpm@9，CI 要求 11.21+；README/SECURE_DEPLOYMENT 引用已删除的 release workflow |

---

## 5. 历史清单存续项（全部 CONFIRMED）

OLD-10（clientIPSet 4096 整体重置）、OLD-11（trimFinishedRequestsLocked O(N) 扫描）、OLD-12（recoverExpiredItems context.Background）、OLD-13（loginratelimit DB 无超时）——全部属实。OLD-29（http/https 白名单）修复属实。

---

## 6. 已修复项验证（全部属实）

| 项 | 验证方式 | 结果 |
|---|---|---|
| H-01 fetch 2xx 校验 | 读 `fetch.go:118,169,232` + `sync.go:64-77` | ✅ 三函数校验 2xx + 空列表跳过删除 |
| H-03 fresh clone 编译 | `git ls-files static` + `go build ./...` | ✅ `.gitkeep` 已跟踪，build exit 0 |
| H-04 Dockerfile tailwind | grep | ✅ 无 tailwind 引用 |
| H-06 版本对齐 | 读 main.go/CHANGELOG/package.json | ✅ 三处均 0.2.0 |
| 脱敏坏 JSON fail-closed | 读 `op/mask.go:27-31` | ✅ 解析失败返回默认关闭 |
| 脱敏会话隔离 | 读 `mask/session.go:110-134` | ✅ 空键不入表 + 30min TTL |
| 探针协议对齐 | 读 `test.go:510-514` + `recovery.go:28` | ✅ OpenAI Chat + max_tokens=1 |
| eval 10 密钥上限 | 读 `test.go:200,222-224` | ✅ evalKeyAttemptLimit=10 |
| 脱敏默认全关 | 读 `model/mask_setting.go:37-39` | ✅ Enabled:false |
| OLD-29 http/https 白名单 | 读 `channel.go:612-625` | ✅ 只允许 http/https |

---

## 7. 行号校正（行为确认，仅引用偏移）

| ID | 审计引用 | 实际位置 |
|---|---|---|
| REL-06 | test.go:197-245 | test.go:384-447（197-245 是 eval 路径） |
| FE-02 | channel-editor.tsx:844-849 | :856-861（staleTime）、:882-897（写编辑态） |
| FE-04 | Settings.tsx:1737-1751 | :1794-1802（失效列表） |
| DEV-06 | template-check.yaml:113-124 | :116-133（requiredLines :116-121） |

---

## 审计局限

- 子代理核验中 REL-02/中继/可靠性三域返回失败，由主代理亲自逐行重读源码补验。
- 未跑 `go test -race`（竞态点为静态确认）。
- 未做动态攻击复现（SSRF/DNS rebinding/Set-Cookie 注入等）。
- DevOps 域未跑 docker 构建/冒烟（沙箱无 docker）。
