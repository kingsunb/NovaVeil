# NovaVeil 全面代码审计报告

> **归档横幅：** 本文形成于 2026-09-10 的历史快照，已归档、不再维护；其中标注的未修复项多数已在此后修复，当前审计事实以 `docs/audits/AUDIT_ISSUES_2026-09-13.md` 为准。

**审计日期**: 2026-09-10
**审计仓库**: kingsunb/NovaVeil
**项目版本**: v0.1.0
**审计团队**: NovaVeil 代码审计团队（4 名专业成员并行审计）

---

## 1. 项目概览

NovaVeil 是一个 LLM API 中转/聚合服务，基于 bestruirui/octopus 和 looplj/axonhub 构建。

**技术栈**:
- 后端: Go 1.26 + Gin + GORM（支持 SQLite/MySQL/PostgreSQL）
- 前端: React 18.3 + TypeScript 5.6 + Vite 5.4 + React Query 5.62 + Radix UI + Tailwind CSS
- 部署: Docker + Nginx + GitHub Actions CI/CD

**核心功能**: 多渠道聚合、协议转换（OpenAI/Anthropic/Gemini）、三态熔断器、自动故障转移、多 Key 轮换、会话粘合、实时请求可视化

**代码规模**: Go 186 文件(~19K LOC 业务 + ~17K LOC 测试), 前端 78 TS/TSX 文件(~12.5K LOC), 总计 356 文件

---

## 2. 审计范围与方法

| 维度 | 审计成员 | 审计方法 |
|------|---------|---------|
| 安全性 | security-auditor | grep/glob/read_file 系统性搜索，关注真实可利用安全问题 |
| 后端代码质量 | backend-auditor | 核心文件逐行审查 + grep 模式搜索 |
| 前端代码质量 | frontend-auditor | 全部源文件阅读 + 针对性 grep 搜索反模式 |
| 部署与运维 | devops-auditor | 配置文件审查 + 安全加固对比分析 |

---

## 3. 发现汇总

| 严重程度 | 安全 | 后端 | 前端 | 部署 | 合计 |
|---------|------|------|------|------|------|
| 严重 (Critical) | 0 | 1 | 0 | 0 | **1** |
| 高 (High) | 2 | 4 | 0 | 5 | **11** |
| 中 (Medium) | 4 | 7 | 3 | 13 | **27** |
| 低 (Low) | 3 | 4 | 7 | 6 | **20** |
| 信息 (Info) | 3 | 3 | 5 | 6 | **17** |
| **合计** | **12** | **19** | **15** | **30** | **76** |

---

## 4. 详细发现

### 4.1 严重 (Critical) — 1 项

#### C-1: unsafe.String 零拷贝转换存在 GC 安全隐患
- **来源**: 后端审计
- **文件**: internal/relay/handler.go:84
- **问题**: 使用 unsafe.String 将 []byte 零拷贝转为 string。raw.Body 在转发循环中被多次重新赋值。unsafe.String 创建的 string 与底层 byte slice 共享内存，如果后续代码路径对原始 raw.Body 底层数组进行修改，将导致 string 内容被静默篡改，违反 Go 字符串不可变契约。该 string 被存入 RequestState.body 并在锁外被异步读取。
- **修复建议**: 使用 string(raw.Body) 进行安全拷贝。对于 64MB 上限的请求体，每请求一次拷贝是可接受的开销。

---

### 4.2 高 (High) — 11 项

#### H-1: 多个 Handler 将内部错误直接返回给客户端
- **来源**: 安全审计
- **文件**: internal/server/handlers/channel.go:124,138,154,167,194,213,251,273,281,323; apikey.go:65,110,138,157; setting.go:52,101,115; group.go:40,197,225
- **问题**: 大量 handler 直接将 err.Error() 作为 HTTP 响应返回。GORM 错误可能包含表名、列名、SQL 片段等内部结构信息，帮助攻击者了解实现细节。
- **修复建议**: 对 500 错误统一返回通用消息，仅在 debug 模式下返回详细错误。

#### H-2: 数据库导出包含 API Key 与渠道密钥明文
- **来源**: 安全审计
- **文件**: internal/op/backup.go:47-49; internal/model/backup.go:7,19-24
- **问题**: DBExportAll 导出时 API Key 和渠道密钥未做任何过滤，明文暴露。导出文件一旦落盘或传输被截获，全部凭据暴露。
- **修复建议**: 导出时加密敏感字段，或用掩码替代明文，至少添加 Cache-Control: no-store。

#### H-3: time.After 在 wait 函数中未停止，timer 泄漏
- **来源**: 后端审计 | **文件**: internal/relay/state.go:414
- **问题**: time.After 创建的 timer 在 ctx.Done() 先触发时不会被 Stop，高频重试场景下未到期 timer 累积。
- **修复建议**: 改用 time.NewTimer 并在退出时显式 defer timer.Stop()。

#### H-4: task.RUN() 使用 select{} 永久阻塞，无法优雅退出
- **来源**: 后端审计 | **文件**: internal/task/task.go:124
- **问题**: RUN() 通过 select{} 永久阻塞调用方 goroutine，无法被 context 取消或信号中断。
- **修复建议**: 提供接受 context.Context 的变体，或将 select{} 替换为等待 stop 信号 channel。

#### H-5: sanitizeRequestBody 忽略 sjson 错误，请求体可能不一致
- **来源**: 后端审计 | **文件**: internal/relay/handler.go:1062, 1067, 1084
- **问题**: 三处忽略 sjson 操作返回错误，可能导致请求体被静默设为空或无效值。
- **修复建议**: 检查 sjson 返回错误，失败时保留原始 body 不变。

#### H-6: recoverExpiredItems 并行探测 goroutine 无 WaitGroup 跟踪
- **来源**: 后端审计 | **文件**: internal/relay/route.go:310-338
- **问题**: 并行探测 goroutine 未被 WaitGroup 跟踪，若底层 HTTP 请求不响应 context 取消，goroutine 将泄漏。
- **修复建议**: 添加 sync.WaitGroup 跟踪所有探测 goroutine。

#### H-7: 前端 Dockerfile 基础镜像未 pin 到 digest
- **来源**: 部署审计 | **文件**: web-next/Dockerfile:20,39
- **问题**: FROM node:22-alpine / FROM nginx:1.27-alpine 使用可变标签，存在供应链攻击风险。
- **修复建议**: 改为 digest-pinned 引用。

#### H-8: web-router 服务完全缺乏安全加固
- **来源**: 部署审计 | **文件**: docker-compose.yml:127-141
- **问题**: 无 user、无 read_only、无 cap_drop、无 security_opt、无资源限制。以 root 运行。
- **修复建议**: 向后端 novaveil 服务的安全加固看齐。

#### H-9: web-router 使用可变镜像标签
- **来源**: 部署审计 | **文件**: docker-compose.yml:128
- **问题**: image: nginx:1.27-alpine 直接用可变标签，可能引入未审查变更。
- **修复建议**: 使用 digest-pinned 引用。

#### H-10: release.yaml cache-dependency-path 引用已删除的 web/ 目录
- **来源**: 部署审计 | **文件**: .github/workflows/release.yaml:85
- **问题**: cache-dependency-path: web/pnpm-lock.yaml 引用已删除目录，导致 pnpm 缓存永远 miss。
- **修复建议**: 改为 web-next/pnpm-lock.yaml。（现状已改为 web-next/，web/ 目录已删除）

#### H-11: CSP 允许 unsafe-inline 脚本
- **来源**: 部署审计 | **文件**: web-next/nginx.conf:41
- **问题**: script-src 'self' 'unsafe-inline' 大幅削弱 CSP 对 XSS 的防护。
- **修复建议**: 移除 'unsafe-inline'，改用 CSP nonce 或 hash。


---

### 4.3 中 (Medium) — 27 项

#### 安全审计 (4 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| S-M1 | handlers/channel.go:353-376 | 渠道导出接口明文输出全部密钥，无 Cache-Control | 添加 Cache-Control: no-store；考虑加密导出 |
| S-M2 | conf/config.go:108 | Cookie Secure 属性默认 false，反代部署可能泄露 | 改默认 true 或检测 X-Forwarded-Proto 自动启用 |
| S-M3 | middleware/securityheaders.go:14-20 | 缺少 CSP 响应头，XSS 无纵深防御 | 使用 nonce-based CSP |
| S-M4 | middleware/securityheaders.go:14-20 | 缺少 HSTS 响应头，可能被 SSL Strip 降级攻击 | HTTPS 部署时动态添加 HSTS |

#### 后端审计 (7 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| B-M1 | relay/keyselect.go:24-25 | keyCursors map 无上限增长 | channelRefreshCache 时清理不存在渠道 ID |
| B-M2 | relay/keyselect.go:28-29 | keyCooldowns 过期条目仅惰性清理 | 添加定期清扫 goroutine |
| B-M3 | relay/sticky.go:17 | sessionStickies 无主动过期清理 | 后台定时任务全量清理 |
| B-M4 | relay/state.go:988-992 | clientIPSet 触顶时整体重置，统计频繁归零 | 使用 HyperLogLog 或 LRU 淘汰 |
| B-M5 | relay/state.go:552-567 | trimFinishedRequestsLocked 每次定稿 O(N) 遍历 | 维护独立 finished 计数器和环形缓冲 |
| B-M6 | relay/route.go:310 | recoverExpiredItems 使用 context.Background() | 全局停止时取消所有探测 |
| B-M7 | handlers/loginratelimit.go:65,89,96,113 | 登录限速器 DB 操作使用 context.Background() | 传入 server.baseCtx |

#### 前端审计 (3 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| F-M1 | pages/Settings.tsx:788 | 使用浏览器原生 confirm()，与 Radix Dialog 风格割裂 | 替换为 Dialog + ConfirmButton |
| F-M2 | pages/Keys.tsx:462-470 | 敏感密钥明文缓存在 React Query 中（60s/5min） | staleTime 降至 0 或极短值 |
| F-M3 | lib/api.ts:200 | undefined as T 返回值绕过类型系统 | 返回类型改为 T 或 undefined |

#### 部署审计 (13 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| D-M1 | docker-compose.yml:95-122 | web-next 服务缺少资源限制 | 添加 mem_limit/cpus/logging |
| D-M2 | docker-compose.yml:95-122 | web-next 服务未指定非 root 用户 | 配置 nginx 非 root 运行 |
| D-M3 | router.nginx.conf:36-42 | router 缺少安全响应头 | 补充与 nginx.conf 一致的安全头 |
| D-M4 | router.nginx.conf 多处 | 非 API 路由缺少 X-Forwarded-Proto | 所有 proxy location 统一设置 |
| D-M5 | build.yaml:49; release.yaml:70 | Trivy 扫描引用已删除的 web/node_modules | 改为 web-next/node_modules（现状已改为 web-next/，web/ 目录已删除） |
| D-M6 | web-next-ci.yaml:32,77,106 | checkout 未设 persist-credentials: false | 添加该配置 |
| D-M7 | web-next-ci.yaml | 缺少 Trivy 密钥扫描 | 添加 secret 扫描步骤 |
| D-M8 | build.yaml:219 | 发布 :latest 可变标签 | 停止发布或重命名为 :dev-latest |
| D-M9 | conf/config.go:66 | data 目录权限 0755 过宽 | 改为 0700 |
| D-M10 | conf/config.go:69 | config.json 写入未设安全权限 | 写入后 chmod 0600 |
| D-M11 | docker-compose.local.yml:58 | 默认数据目录在 /tmp | 改为 ./novaveil-data |
| D-M12 | web-next/nginx.conf:67 | /__flags/ 端点 CORS 设置为 * | 限制为已知前端域名 |
| D-M13 | SECURE_DEPLOYMENT.md:114-115 | 发布产物缺少签名验证 | 集成 Sigstore/cosign |


---

### 4.4 低 (Low) — 20 项

#### 安全审计 (3 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| S-L1 | model/apikey.go:9 | API Key 明文存储数据库 | 考虑存储 SHA-256 哈希或对称加密 |
| S-L2 | relay/handler.go:84 | unsafe.String 零拷贝（当前安全但未来变更有风险） | 添加约束注释或改用 string() |
| S-L3 | update/core.go:26-98 | 自更新仅 SHA256 校验无签名验证 | 增加 GPG 签名验证；生产默认禁用 |

#### 后端审计 (4 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| B-L1 | op/channel.go:383-389 | ChannelGetCore 返回缓存直接引用 | 文档标注调用方不得修改 |
| B-L2 | op/cache.go:13-16 等 | 错误包装混用 %w 和 %v | 统一使用 %w |
| B-L3 | op/channel.go:451-455 | statsColumnsOnce 永久缓存列存在性判定 | 补充维护提醒注释 |
| B-L4 | internal/ 整体 | 测试覆盖不均衡 | 补充关键模块单元测试 |

#### 前端审计 (7 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| F-L1 | channel-editor.tsx:80 | JSON.parse(JSON.stringify()) 深拷贝 | 改用 structuredClone() |
| F-L2 | pages/Groups.tsx:694 | document.getElementById 直接 DOM 查询 | 改用 useRef |
| F-L3 | TokenTrendChart.tsx:119-120 | 图表硬编码颜色不随主题切换 | 接入 CSS 变量 + Tailwind |
| F-L4 | pages/Groups.tsx:79 | 模块级可变状态用于 ID 生成 | 改用 useRef 或 crypto.randomUUID() |
| F-L5 | Channels.tsx:256; Groups.tsx:225 | select 值转型未做运行时校验 | 添加运行时校验函数 |
| F-L6 | channel-editor.tsx:1649-1657 | param_override JSON 文本框无前端校验 | onBlur 中 JSON.parse 校验 |
| F-L7 | 多文件 (13处) | 空 catch 块吞掉错误 | api.ts 中的 catch 至少 dev 模式 console.debug |

#### 部署审计 (6 项)

| 编号 | 文件 | 问题 | 修复建议 |
|------|------|------|---------|
| D-L1 | web-next/Dockerfile:25 | pnpm-lock.yaml 通配符允许缺失 | 去掉通配符 |
| D-L2 | router.nginx.conf:37 | 监听 80 端口（明文） | 注释标注 TLS 应在外层终止 |
| D-L3 | migrate/migrate.go:174 | fmt.Sprintf 拼接 SQL | 使用 GORM Migrator API |
| D-L4 | web-next/nginx.conf:91 | SSE proxy_read_timeout 24h | 考虑降至 1h-4h |
| D-L5 | release.yaml:7-8 | 仅 main.go 变更触发 release | 补充注释说明手动触发场景 |
| D-L6 | docker-compose.yml:73-74 | Cookie Secure 默认注释 | 文档强调必须启用 |

---

### 4.5 信息 (Info) — 17 项（良好实践）

#### 安全审计良好实践 (3 项)
- **I-S1**: 登录限速 fail-close + IP 级 + 多副本共享数据库计数，X-Forwarded-For 不可伪造
- **I-S2**: JWT 算法钉死 HS256、强制 exp、校验 issuer/audience、32 字节随机密钥+改密轮换
- **I-S3**: 错误日志全面脱敏（正则+JSON 结构化双路径，Bearer token/敏感字段替换为 [REDACTED]）

#### 后端审计良好实践 (3 项)
- **I-B1**: 并发安全实践良好（panic 兜底归还占用、atomic.Bool 热路径、sync.Once 幂等释放、分片缓存消除空窗口）
- **I-B2**: 资源管理到位（代理切换清理连接池、连接池按方言区分、ReadHeaderTimeout/IdleTimeout 防 Slowloris）
- **I-B3**: 错误处理与可观测性良好（错误日志按类限流、终态定稿分两阶段锁外处理、提前 EOF 免费重试、攽批落库+按类去重节流）

#### 前端审计良好实践 (5 项)
- **I-F1**: TypeScript 类型安全严格，零 any / 零 @ts-ignore
- **I-F2**: API 层封装完善（泛型包装 + 分类错误 + 信封式响应解析 + 防御性 normalize）
- **I-F3**: SSE 实现健壮（指数退避重连 1s->30s + 401 探活 + 命名事件）
- **I-F4**: 乐观更新 + 回滚模式一致
- **I-F5**: 可访问性实践优秀（217 处 aria/role 属性，Playwright + axe E2E 测试已配置）

#### 部署审计良好实践 (6 项)
- **I-D1**: 后端 Dockerfile 安全加固完善（digest pin、非 root、最小化包、HEALTHCHECK）
- **I-D2**: 后端 docker-compose 安全加固完善（强制镜像指定、非 root、read-only rootfs、cap_drop ALL、资源限制）
- **I-D3**: .dockerignore 配置严格（默认拒绝全部，显式 allowlist）
- **I-D4**: CI/CD 安全实践到位（Actions pin SHA、persist-credentials: false、最小权限、Trivy 扫描）
- **I-D5**: entrypoint.sh 安全实践良好（set -eu、umask 0077、exec 替换进程）
- **I-D6**: 数据库迁移设计合理（版本化、去重检查、状态记录、有限重试、幂等执行）


---

## 5. 整体评价与改进建议

### 5.1 整体评价

NovaVeil 项目整体代码质量**较高**，体现了专业的工程实践和安全意识。项目在核心安全机制（JWT、CORS、错误日志脱敏、登录限速）和后端容器加固方面表现出色，前端 TypeScript 类型安全和可访问性实践也值得肯定。

**主要亮点**:
1. **安全基础扎实**: JWT 算法钉死、bcrypt 密码存储、错误日志全面脱敏、CORS 严格配置、无 SQL/命令注入风险、无硬编码密钥
2. **后端架构成熟**: 三态熔断器+自动故障转移+多 Key 轮换设计精良，并发安全实践良好，资源管理到位
3. **前端工程规范**: 零 any 类型、API 层封装完善、SSE 指数退避重连、乐观更新+回滚一致、可访问性优秀
4. **DevOps 专业**: 后端容器全面加固（非 root、read-only、cap_drop ALL、digest pin）、CI/CD 安全实践到位

**主要不足**:
1. **1 个严重问题**: unsafe.String 零拷贝存在 GC 安全隐患，需优先修复
2. **信息泄露风险**: Handler 错误响应泄露内部信息、数据库导出含明文凭据
3. **前端/路由容器加固不足**: web-router 和 web-next 服务安全加固远落后于后端
4. **内存管理**: 多处 map 无主动清理机制，长期运行可能内存泄漏
5. **安全响应头缺失**: CSP/HSTS 未设置，cookie_secure 默认不安全

### 5.2 优先修复建议

按优先级排序的修复建议：

| 优先级 | 编号 | 问题 | 工作量 |
|--------|------|------|--------|
| P0 | C-1 | unsafe.String 替换为 string() | 小 |
| P0 | H-1 | Handler 500 错误统一返回通用消息 | 中 |
| P0 | H-2 | 数据库导出加密/掩码敏感字段 | 中 |
| P1 | H-3 | time.After 改用 time.NewTimer + defer Stop | 小 |
| P1 | H-4 | task.RUN() 添加 context 支持 | 小 |
| P1 | H-5 | sanitizeRequestBody 检查 sjson 错误 | 小 |
| P1 | H-6 | 添加 WaitGroup 跟踪探测 goroutine | 小 |
| P1 | H-7~H-9 | 前端/路由容器安全加固 | 中 |
| P1 | H-10 | 修复 release.yaml 缓存路径 | 小 |
| P1 | H-11 | CSP 移除 unsafe-inline | 中 |
| P2 | S-M1~S-M4 | 安全响应头补全 + cookie_secure 默认值 | 中 |
| P2 | B-M1~B-M3 | 内存清理机制（keyCursors/keyCooldowns/sessionStickies） | 中 |
| P2 | D-M5~D-M7 | CI/CD 遗留修复 | 小 |
| P3 | 其余 Low/Info | 按需逐步改进 | 小 |

### 5.3 总结

NovaVeil 是一个工程质量较高的 LLM API 中转服务。76 项审计发现中，仅 1 项严重、11 项高，且严重问题修复简单（替换 unsafe.String），高危问题多为信息泄露和容器加固不足，不涉及核心架构缺陷。项目在安全意识、并发安全、错误处理、DevOps 实践方面表现专业，建议按优先级逐步修复，优先处理 P0 级别的 3 项问题。

---

*报告生成时间: 2026-09-10 16:30*  
*审计团队: NovaVeil 代码审计团队*  
*报告路径: docs/audits/AUDIT_REPORT_FULL.md*
