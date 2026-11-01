# 静态审计报告（2026-11-01）

全仓静态阅读，覆盖鉴权、凭据、转发、脱敏、评估、前端与部署面。不改代码，不跑编译与测试。事实以当天工作区代码为准；本文是一次审计快照，不替代各模块文档的「已知边界」。

结论：没有发现可以从外部未授权直接打穿的高危问题。新问题集中在管理台凭据暴露面、已登录会话的口令校验，以及错误文本的脱敏缺口。

## 范围与方法

- 后端：`internal/server`、`internal/op`、`internal/relay`、`internal/seal`、`internal/client`、`internal/keylimit`、`cmd`。
- 前端：`web-next/src` 的鉴权、渠道编辑、对话页与安全头。
- 依赖抽查：`github.com/looplj/axonhub/llm`（`go.mod` 锁定 `v0.0.0-20261021081413-cbdf9a6d4a3d`）的 HTTP 客户端与 Gemini 出站。
- 对照：`docs/modules/` 已记录的边界不重复列为新问题。

## 中等

### 1. 自定义头与头模板在管理接口返回明文

渠道列表、创建、更新都走 `channelAdminSummary`（`internal/server/handlers/channel.go`）。该函数只清空 `Key` 与 `Keys[].Key` 并写掩码，`CustomHeader` 原样返回。

这些值落库时经 `seal.Seal` 加密（`internal/op/crypto.go`），但缓存读取会解密后再交给列表接口，所以接口拿到的是明文。设置列表只跳过 `auth_jwt_secret`（`internal/op/setting.go`），`header_templates`、`proxy_url`、`proxy_pool` 同样明文返回。

前端把 `custom_header` 拷进渠道编辑草稿，查询缓存保留 30 秒（`web-next/src/main.tsx`）。备份导出反而把代理、自定义头和头模板打成 `****`（`internal/op/backup.go` 的 `redactNonKeyCredentialsForExport`）。

`docs/modules/web-next.md` 只声明渠道 Key 不进缓存，并把渠道代理明文列为例外。`header_value` 从未被列入列表掩码，也没有被写成例外。

影响：能读渠道列表或设置列表的人（包括能读浏览器内存的扩展）可以看到自定义头和头模板里的凭据。转发侧有黑名单，客户端不能把这些头再抄出去，所以这是管理台泄漏，不是对外泄漏。

### 2. 已登录会话改密、改用户名不进登录限速

`internal/server/handlers/user.go` 的 `changePassword`、`changeUsername` 在旧密码错误时直接返回 400，不调用 `recordFailure`。登录接口本身是 15 分钟 5 次、数据库故障即拒绝。管理 API 令牌桶默认关闭（`internal/conf/config.go` 的 `AdminAPIRateLimitEnabled=false`）。`docs/modules/server-auth.md` 只写「管理令牌桶默认关闭」，没有点名改密可以离线撞旧密码。

前提是已经持有有效的管理员 cookie。拿到 cookie 之后，改密接口不受登录那道限速约束。

状态：已修复（2026-11-01），改密与改用户名共用登录失败计数。

### 3. 请求状态、SSE、失败环与对话留存的错误文本不脱敏

`finishRound`（`internal/relay/state.go`）把完整错误文本写入 `RequestState.Error` 并 `publishRequestLocked`。对话留存的 `ErrBrief` 和失败环只做截断，不做脱敏。只有 `error_logs` 走 `sanitizeErrorLog`（`internal/op/error_log.go`，覆盖 `Bearer` 和一批字段名）。上游错误体会嵌入最多 8KB（`internal/relay/upstream.go` 的 `errBodySnippetLimit`）。

本仓库自己的出站请求不把 Key 放进 query：OpenCode 只追加 `beta=true`，Gemini 出站使用 `x-goog-api-key` 请求头。因此这条不是必现，取决于上游错误体或 URL 里是否本身带凭据。一旦带上，管理台 SSE 和对话记录会原样保存。

## 低

### 4. 非流式成功响应没有体积上限

axonhub `httpclient/client.go` 在状态码小于 400 时对响应体 `io.ReadAll`，不封顶；大于等于 400 才用 `LimitReader`。`internal/relay/handler.go` 的成功路径把整段 body 写回客户端。同主机同端口的 3xx 会跟到最终的 2xx。跨源 3xx 被 `upstreamCheckRedirect`（`internal/client/http.go`）拒绝，不会把内网响应体跟回来。恶意或异常上游可以靠一个 200 撑大进程内存。

状态：已修复（2026-11-01），非流式响应体上限 64 MiB。

### 5. 分拆部署的 nginx 安全头比 Go 弱

`web-next/nginx.conf` 只有 `X-Frame-Options: SAMEORIGIN`，没有 `frame-ancestors`，也没有 HSTS。Go 侧 `internal/server/middleware/securityheaders.go` 是 `X-Frame-Options: DENY` 加 `frame-ancestors 'none'`，HSTS 只在 `CookieSecure` 时加。`/api` 仍走 Go 的头。嵌入面限于 nginx 同源页面。

状态：已修复（2026-11-01），两份 nginx 模板对齐 Go 侧安全头并加 HSTS。

### 6. 写路径的 `****` 哨兵不一致

创建和导入会拒绝渠道 Key 恰好等于 `****`。更新渠道时，代理和自定义头不拒绝这个哨兵。头模板只有「全部非空值都是 `****`」才跳过；一份混合了明文和 `****` 的模板导入会整行覆盖成打码值。

### 7. 自定义头保存时不拒绝敏感头名

`blockedCustomHeader`（`internal/relay/channel.go`）在转发时跳过 `authorization`、`cookie`、`host` 等名字。保存渠道不拒绝这些名字，配了等于静默无效。随机头在保存时会拒绝敏感名，两边不一致。

### 8. 密钥派生与文件权限

`internal/seal/seal.go` 的 `deriveKey` 是一次 SHA-256，无盐、无迭代。注释写「hex 或任意字符串」，实现不解码 hex：把密钥文件内容抄进 `encryption_key` 会得到另一把密钥。密钥文件 `0600`、目录 `0700` 只在新建时设置，之后改权限不会被纠正。

### 9. Cookie Path 为 `/`

gin 把空 path 写成 `Path=/`，同主机其它应用的请求会带上这个 cookie。`SameSite=Lax`。

状态：已修复（2026-11-01），Cookie Path 改为 `/api`。

### 10. 非标准名的凭据头在 debug 日志里完整打印

`internal/relay/channel.go` 只对 axonhub `IsSensitiveHeader` 命中的头截断。`X-Token` 这类名字会原文出现。默认日志级别是 info（`internal/conf/config.go`），只有显式 debug 才打。

## 已核对、不作为新问题

- 登录限速、JWT（HS256，校验过期、签发者、受众，24 小时上限，登出和改密轮换密钥）、管理面写请求的来源校验。
- 出站重定向最多 5 跳，拒绝 HTTPS 降级，拒绝跨主机或跨端口。
- `file://`、`gopher://` 在保存 BaseURL 时拒绝；代理 scheme 在拨号层限制。
- 上游响应头丢弃 `Set-Cookie`、`Location`、`WWW-Authenticate` 和逐跳头。
- 客户端的 `Host`、`Authorization`、`X-Forwarded-*` 不会原样打到上游。
- API Key 不从 query 读取；请求状态里只留 Key 尾缀。
- 前端没有 `dangerouslySetInnerHTML`，没有开放重定向。
- 用量桶按冲突累加，不互相吞量。并发与每分钟请求数限速的归还是幂等的。

## 不重复列出的已知边界

以下条目已写在 `docs/modules/`，本次核对与文档一致，不升格为新问题：

- 备份导出渠道 Key 与 API Key 明文；代理和自定义头在备份里打码。
- 内网 BaseURL 与代理直连是部署边界，保存时不拦截私网地址。
- 入站 body 预算按两份口径，峰值约 1.5 倍。
- 多实例后台任务会重复执行。
- 还原后的明文进会话留档与请求状态；命中原文进 `error_logs`。
- 对话页把正文放进 `localStorage`，登出时清除。
- CI 的 `go test` 不带 `-race`；Docker 发布推可变 tag。
