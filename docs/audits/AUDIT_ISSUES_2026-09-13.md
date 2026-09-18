# NovaVeil 问题清单（静态代码审阅）

**日期**: 2026-09-13  
**范围**: 当前工作区源码（含未提交改动）  
**方法**: 只读审阅。未下载依赖、未执行二进制/脚本/测试、未开子代理。  
**对照**: 早前的归档审计报告已从 docs/ 移除；本文不重复抄写旧报告条目，文末单独标注哪些旧问题看起来已经修过。

> **2026-09-13 修复**: 本文列出的高/中/低项已在同日代码中处理（敏感导出改 POST、脱敏会话 TTL、对话页内部接口、JWT 登出轮换、CSP、默认绑定等）。下文保留当时的问题描述，便于对照。M-5 凭据静态加密曾同日实现，评审后按个人使用定位移除，见该项内说明。

> 结论先说：项目整体工程化程度高（鉴权、熔断、密钥掩码、限速、备份过滤 JWT 密钥都有认真做）。下面列的是**现在代码里仍能对上的问题**，不是「还可以更好」的愿望清单。

---

## 汇总

| 严重程度 | 数量 |
|---------|------|
| 高 | 5 |
| 中 | 12 |
| 低 | 8 |
| **合计** | **25** |

---

## 高

### H-1 GET 接口可下载全部上游密钥 / 整库备份（Cookie SameSite=Lax）

**位置**

- `internal/server/handlers/channel.go`：`GET /api/v1/channel/export`、`GET /api/v1/channel/keys/:id`
- `internal/server/handlers/setting.go`：`GET /api/v1/setting/export`
- `internal/server/handlers/apikey.go`：`GET /api/v1/apikey/secret/:id`

**问题**

这些接口用管理员 cookie 鉴权，且是 **GET**。`OriginProtection` 只拦 `/api/` 的写方法（POST/PUT/PATCH/DELETE），对 GET 放行。Cookie 为 `SameSite=Lax`，**跨站顶层导航**（管理员点链接、`window.location`）会带上 cookie。

结果：已登录管理员点一下  
`https://<面板>/api/v1/channel/export`  
浏览器就会下载含全部渠道明文凭据的文本。整库导出同样带 API Key 与渠道 Key（`filterSecretSettings` 只去掉 JWT 密钥）。

这些响应当前也没有 `Cache-Control: no-store`。

**建议**

- 密钥导出 / 明文查看改为 POST，并要求自定义头（例如 `X-Requested-With`），浏览器顶层导航带不走。
- 至少给敏感响应当 `Cache-Control: no-store, private`。
- 导出文件加密或二次确认。

---

### H-2 脱敏会话映射「每请求删除」，与设计承诺相反

**位置**

- `internal/relay/handler.go`：`defer cleanupMaskSession(sessionKey)`
- `internal/relay/mask_integration.go`：`cleanupMaskSession` 对非空 sessionKey **请求结束立即 Delete**
- `internal/relay/mask/session.go`：注释写明「同一长对话中同一敏感值在多轮间必须映射为同一占位符」

**问题**

设计要求带 `X-Session-Id` 的多轮对话复用同一张映射表。实现却在每个请求 `defer` 里把该会话映射删掉。下一轮同样的手机号/密钥会拿到**新的随机占位符**，模型上下文里同一实体变成不同 token，脱敏效果和还原稳定性都下降。

**建议**

有会话键时按粘合 TTL / 空闲超时回收，不要在单次请求结束时删。无会话键才用请求级临时映射并在结束时释放。

---

### H-3 无会话键时所有请求共用一张永不回收的映射表

**位置**

- `internal/relay/mask/session.go`：`GetOrCreate(sessionKey)`，空字符串也是合法键
- `internal/relay/mask_integration.go`：`cleanupMaskSession` **空键不回收**
- `internal/relay/handler.go`：无 `X-Session-Id` / `x-opencode-session` 时 `sessionKey == ""`

**问题**

注释写「无会话标识时用请求级临时键」。实现没有生成临时键，直接把 `""` 传进 `GetOrCreate`。于是：

1. 所有未带头的请求共享同一张 `sessions[""]`。
2. 这张表从不 Delete，映射只增不减，是进程级内存泄漏。
3. 不同客户端的敏感值进入同一张表；若响应里出现另一请求的占位符，可能被还原成别人的原文（概率取决于 token 碰撞/模型回显，但共享表本身就不该存在）。

**建议**

无会话键时用请求 UUID 作临时键，并在 `defer` 里只删这个临时键。空键不要进全局 store。

---

### H-4 脱敏配置 JSON 损坏会拒绝全部中转流量

**位置**

- `internal/op/mask.go`：`MaskConfigGet` 解析失败时 `return DefaultMaskConfig(), fmt.Errorf(...)`
- `internal/relay/mask_integration.go`：`applyRequestMask` 只要 `err != nil` 就 fail-closed，拒绝请求

**问题**

`SettingKeyMaskConfig` 一行坏 JSON（误编辑、导入脏数据、磁盘损坏）时，`MaskConfigGet` 其实已经返回了「全关」默认配置，但又带了 error。`applyRequestMask` 只看 error，于是**所有**走脱敏入口的请求被拒绝，包括本应「开关关闭、零开销放行」的流量。

这是可用性故障，不是安全增强：默认配置已经是关闭脱敏。

**建议**

键缺失 / 解析失败且默认配置为关闭时，按关闭短路，打日志，不要 拒绝全站 `/v1`。仅在「配置表明应该脱敏但引擎失败」时 fail-closed。

---

### H-5 对话页把密钥明文拉进浏览器，并为渠道模型创建真实分组

**位置**

- `web-next/src/pages/Chat.tsx`

**问题**

1. 选中 API Key 后调用 `getAPIKeySecret`，明文放进 React state，再以 `Authorization: Bearer` 打 `/v1/chat/completions`。管理端 XSS 或扩展即可拿到中转密钥。对话内容还写入 `localStorage`（`novaveil:chat:current`）。
2. 选择「渠道 / 模型」时会 `createGroup({ name: "__chat_" + Date.now() })` 并加入成员。这些是**真实分组**，会出现在分组列表和模型名空间里。刷新、关标签、请求失败时删除是 best-effort（`catch` 吞掉），容易留下 `__chat_*` 孤儿分组，污染路由。

管理端对话应走已登录的内部接口，而不是复用对外 API Key；渠道直连也不该靠新建分组。

---

## 中

### M-1 敏感 GET/错误响应缺少禁止缓存，handler 把 `err.Error()` 原样返回

大量 `internal/server/handlers/*.go` 的 500/404 使用 `resp.Error(..., err.Error())`。GORM / 网络错误可能带出 DSN 片段、表名、上游 URL。导出接口也没有 `no-store`。

500 应对客户端只返回稳定文案，细节写日志。

### M-2 自更新只校验 SHA256SUMS，清单与包来自同一 GitHub 源

`internal/update/core.go`：下载 zip 后再下 `SHA256SUMS`，比对哈希。没有独立签名（GPG/cosign）。GitHub 被钓鱼、账号被盗或中间人同时改两个文件时，校验仍通过。随后 `os.Rename` 替换自身二进制并重启。

建议校验发布签名，或默认关闭二进制自更新、只保留镜像升级（compose 已设 `NOVAVEIL_DISABLE_SELF_UPDATE`）。

### M-3 Origin 缺失时管理写操作不做 CSRF 检查

`internal/server/middleware/cors.go`：`OriginProtection` 在 Origin/Referer 都空时 `c.Next()`。注释说是为了兼容 CLI。Cookie 已是 Lax，现代浏览器跨站 POST 一般不带 cookie，但缺 Origin 的非浏览器客户端、部分旧环境、以及扩展发起的请求可以打写接口。

建议：带 cookie 的浏览器请求必须有 Origin；CLI 用非 cookie 方案。

### M-4 登出不让 JWT 失效

`VerifyJWTToken` 只验 HS256 + exp + iss/aud，无 `jti`、无服务端黑名单。`logout` 只清 cookie。令牌被偷后可用到过期（最长 30 天，见 `GenerateJWTToken`）。改密会轮换 JWT 密钥，这是目前唯一的全员失效手段。

### M-5 上游密钥与 API Key 明文落库、无静态加密

SQLite/MySQL/PostgreSQL 里渠道 Key、API Key 都是明文。备份导出同样带明文（有意为之，方便迁移）。磁盘、快照、错误备份文件一旦流出，凭据全丢。个人单机可接受，但不能当「已经安全」。

**决定不实施**：2026-09-13 曾实现 AES-256-GCM 静态加密（数据目录 `secrets.key`），评审后按个人使用定位移除，恢复明文落库；敏感导出侧的 POST 化、禁缓存与审计日志保留。

### M-6 渠道 `base_url` 后端不校验 scheme

前端 `URL_RULE` 要求 `http://` / `https://`。`op.ChannelCreate` / `ChannelUpdate` / `ChannelImportFromText` 不校验。导入文本或直接打 API 可写入任意 URL。出站 HTTP 客户端虽拒绝跨源重定向，但仍会向管理员指定的地址发带密钥的请求（管理端 SSRF，产品形态如此，但缺少最小 scheme 白名单）。

### M-7 按密钥测试可占用极长时间

`testChannelKeys` 整体超时 **30 分钟**，单 Key 300s、并发 4。已登录会话可反复触发，把上游探测与 goroutine 占满。管理端缺少更硬的全局限流。

### M-8 Docker `mem_limit: 512m` 与 64MB 解压后请求体不匹配

`internal/relay/bodylimit.go`：压缩体 16MB、解压后 64MB。compose 限制 512MB。几个大上下文并发请求就能把容器打到 OOM，熔断/等待中的请求会一起死。

### M-9 Key 冷却 map 只在读取或删渠道时清理

`internal/relay/keyselect.go`：`keyCooldowns` 过期条目惰性删除。某 Key 冷却后不再被选中，条目一直留到渠道删除。长期运行、频繁轮换 Key ID 时缓慢涨内存。粘合表 `sessionStickies` 的全量 prune 也只扫**当前分组**，长期无成功请求的分组过期条目会留着。

### M-10 半开并行探测仍无 WaitGroup

`internal/relay/route.go` `recoverExpiredItems`：对每个候选 `go probeChannelFunc`。`defer cancelAll()` 会取消 context，结果 channel 有缓冲所以发送不堵。若探测实现忽略 ctx（测试注入或未来改动），函数返回后 goroutine 仍跑。旧审计 H-4 仍在。

### M-11 设置页写错调试环境变量名

`web-next/src/pages/Settings.tsx` 与 `internal/conf/debug.go` 注释写成 `NOVAEIL_DEBUG`（少一个 V）。真实变量是 `strings.ToUpper("NovaVeil") + "_DEBUG"` → **`NOVAVEIL_DEBUG`**。按界面去设，debug 不会开。

### M-12 仪表盘「非永久」错误数被日志保留上限扭曲

`internal/server/handlers/update.go` 注释已承认：非 `forever` 的 `error_count` 来自 `error_logs`，受保留条数（默认很低）、按类去重、队满丢弃影响。KPI 会显示「最近几乎没错误」，和真实失败次数不是一回事。`tokens_by_model` 也不随分档，和四张 KPI 口径不一致。

---

## 低

### L-1 版本号三套并行

| 位置 | 值 |
|------|----|
| `main.go` 注释 / README Docker 镜像 | v0.1.0 |
| `CHANGELOG.md` 最新发布 | 0.2.0（2026-08-30） |
| `web-next/package.json` | 0.1.0 |
| `CHANGELOG.md` `[Unreleased]` | 「暂无面向用户的未发布变更」 |

0.1.0 相对 0.2.0 的协议转换、脱敏、对话页、熔断等用户可见变化没有记进 changelog。发版与排障会对不上。

### L-2 统计接口挂在 `/api/v1/update/`

`now-version`、`build-info`、`token-trends` 与「检查更新 / 执行更新」共用前缀。权限目前都是登录即可，语义混乱，也让前端版本看门狗看起来像在打更新 API。

### L-3 `GenerateAPIKey` 在 `crypto/rand` 失败时返回空串

`internal/server/auth/auth.go`。`createAPIKey` 把空串当成「请服务器生成」，可能写入空密钥或前缀残缺的 key。`rand` 失败极罕见，但失败路径应返回 error，而不是 `""`。

### L-4 无 CSP

`securityheaders.go` 明确不设 CSP（index.html 有内联主题脚本）。XSS 面比有 nonce CSP 时更大。对话页还把密钥放进 JS 内存。

### L-5 二进制默认 `0.0.0.0:8080` 且 `cookie_secure=false`

compose 默认绑 `127.0.0.1:8888`，这部分是对的。`go run` / `novaveil start` 默认全接口明文 HTTP。个人工具容易误暴露。

### L-6 单管理员、无 2FA、无操作审计

登录失败有 IP 窗口限速。没有第二因素，也没有「谁导出了密钥」的审计日志。适合个人，不适合多人共用一台面板。

### L-7 渠道导入丢字段

`ChannelImportFromText` 只恢复名称、地址、密钥。类型、模型、分组、限速、标签都没有。文档写了边界，但导入后再测连通很容易失败，界面也没有醒目说明。

### L-8 登录 `expire` 由客户端提交

服务端把上限钳在 30 天。前端仍可申请满 30 天 cookie。结合 M-4，被盗 cookie 窗口很长。

---

## 旧审计条目（对照现状，本次未重测）

只根据当前源码阅读判断，**没有跑测试**。

| 旧编号 | 题目 | 现状 |
|--------|------|------|
| C-1 | `unsafe.String` 零拷贝 | **已改**。`handler.go` 使用 `string(raw.Body)` 拷贝。 |
| H-3（旧 time.After） | `wait` timer 泄漏 | **已改**。`state.go` 使用 `time.NewTimer` + `defer Stop`。 |
| H-2（旧 task.RUN select{}） | 无法退出 | **已改**。`task.go` 用 `runStopCh`，`StopAll` 会关闭它。 |
| keyCursors 渠道删除残留 | 增删渠道泄漏 | **部分修**。`CleanupChannelKeyState` 在删渠道时清理；缓存刷新路径未见扫表。 |
| recoverExpiredItems 无 WaitGroup | 探测 goroutine | **仍在**（本次 M-10）。 |
| handler `err.Error()` | 内部错误外泄 | **仍在**（本次 M-1）。 |
| 备份含密钥明文 | 导出带 Key | **仍在**（有意设计 + 本次 H-1）。 |

`docs/FEATURES.md` 里 BUG-004/005/006 在工作区未提交改动中已标「已修复」；本次未运行 UI，不把它们再当打开缺陷。

---

## 建议处理顺序

1. **H-1**：敏感导出/明文改为 POST + 禁缓存。这是最容易被真实点到的洞。  
2. **H-2 / H-3**：脱敏会话生命周期按设计重做，否则 REQ-015 的多轮一致性不成立。  
3. **H-4**：坏配置不要把 `/v1` 全站打挂。  
4. **H-5**：对话页不要拉 API Key 明文、不要为聊天建真实分组。  
5. **M-2 / M-11 / L-1**：供应链校验、调试变量名、changelog 与版本号对齐。

---

## 本次未覆盖

- 未跑 `go test` / `pnpm test` / lint / 浏览器。  
- 未做依赖漏洞扫描。  
- 未验证未提交前端改动的交互是否已修好 BUG-004～006。  
- 未审计 `axonhub/llm` 等第三方协议转换库内部实现。
