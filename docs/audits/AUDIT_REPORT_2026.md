# NovaVeil 完整测试与审计报告

> **归档横幅：** 本文形成于 2026-09-11 的历史快照，已归档、不再维护；其中标注的未修复项多数已在此后修复，当前审计事实以 `docs/audits/AUDIT_ISSUES_2026-09-13.md` 为准。

**审计日期**: 2026-09-11
**审计仓库**: kingsunb/NovaVeil (commit 86f25de)
**项目版本**: v0.1.0 (dev)
**架构**: linux/arm64 (aarch64)
**审计方法**: 全量测试 + 静态分析 + 依赖扫描 + 安全审计 + 代码质量审计 + 架构审计

---

## 1. 执行摘要

NovaVeil 是一个成熟的 LLM API 中转/聚合服务，整体安全性和代码质量**高于行业平均水平**。本次审计执行了全量测试、静态分析、依赖漏洞扫描、安全审计和代码质量审计。

### 审计结果总览

| 维度 | 结果 | 状态 |
|------|------|------|
| Go 后端测试 | 487 PASS, 0 FAIL, 1 SKIP | ✅ 全部通过 |
| Go 静态分析 (vet) | 0 问题 | ✅ 通过 |
| Go 漏洞扫描 (govulncheck) | 0 个影响代码的漏洞 | ✅ 通过 |
| Go 测试覆盖率 | 53.6% (整体) | ⚠️ 一般 |
| 前端单元测试 | 249 PASS, 0 FAIL | ✅ 全部通过 |
| 前端类型检查 (tsc) | 0 错误 | ✅ 通过 |
| 前端 Lint (eslint) | 0 错误 | ✅ 通过 |
| 前端依赖审计 | 2 critical, 8 high (仅 devDeps) | ⚠️ 需关注 |
| 构建验证 | 后端 42MB + 前端 900K | ✅ 构建成功 |
| 启动验证 | 正常启动/关闭 | ✅ 通过 |

### 发现汇总

| 严重程度 | 后端 | 前端 | 架构/DevOps | 合计 |
|---------|------|------|------------|------|
| 严重 (Critical) | 1 | 0 | 0 | **1** |
| 高 (High) | 4 | 0 | 2 | **6** |
| 中 (Medium) | 8 | 1 | 6 | **15** |
| 低 (Low) | 7 | 5 | 7 | **19** |
| 信息 (Info) | 3 | 4 | 5 | **12** |
| **合计** | **23** | **10** | **20** | **53** |

> 注：后端 19 项发现在 `AUDIT_BACKEND.md` 中已有记录，本次审计确认其仍然存在。

---

## 2. 项目概览

| 项目 | 详情 |
|------|------|
| **后端** | Go 1.26.7 + Gin + GORM (SQLite/MySQL/PostgreSQL) |
| **前端** | React 18.3 + TypeScript 5.6 + Vite 5.4 + React Query + Radix UI + Tailwind CSS |
| **部署** | Docker (非 root, 只读根文件系统, cap_drop ALL) + GitHub Actions CI/CD |
| **核心功能** | 多渠道聚合、协议转换 (OpenAI/Anthropic/Gemini)、三态熔断器、自动故障转移、多 Key 轮换、会话粘合、实时请求可视化 |

### 代码规模

| 类型 | 文件数 | 行数 |
|------|--------|------|
| Go 源码 | 119 | 21,259 |
| Go 测试 | 85 | 17,968 |
| 前端源码 (TS/TSX) | 74 | 13,825 |
| 前端测试 | 30 | — |
| **测试/源码比率** | Go: 0.84 | 前端: 0.40 |

---

## 3. 测试结果详情

### 3.1 Go 后端测试

```
487 PASS, 0 FAIL, 1 SKIP
执行时间: ~25s
```

**按包覆盖率**:

| 包 | 覆盖率 | 评价 |
|----|--------|------|
| internal/keylimit | 98.1% | ✅ 优秀 |
| internal/relay/mask | 89.9% | ✅ 优秀 |
| internal/server | 85.5% | ✅ 优秀 |
| internal/relay | 74.9% | ✅ 良好 |
| internal/server/middleware | 70.9% | ✅ 良好 |
| internal/utils/shutdown | 67.2% | ⚠️ 一般 |
| internal/op | 59.5% | ⚠️ 一般 |
| internal/db | 58.0% | ⚠️ 一般 |
| internal/server/auth | 58.3% | ⚠️ 一般 |
| internal/update | 37.8% | ⚠️ 偏低 |
| internal/client | 29.1% | ⚠️ 偏低 |
| internal/task | 22.9% | ❌ 低 |
| internal/model | 21.2% | ❌ 低 |
| internal/helper | 9.4% | ❌ 低 |
| internal/server/handlers | 9.1% | ❌ 低 |

**Benchmark (relay 核心)**:

| Benchmark | ns/op | B/op | allocs/op |
|-----------|-------|------|-----------|
| Passthrough_HeaderAndBody | 1,157 | 1,008 | 9 |
| Converted_RequestJSONRoundTrip | 6,664 | 1,946 | 53 |
| Converted_ResponseJSONRoundTrip | 12,306 | 3,661 | 89 |
| Passthrough_ResponseParseOnly | 4,592 | 376 | 11 |

### 3.2 前端测试

```
28 test files, 249 tests passed
执行时间: 6.96s
```

- ✅ 所有单元测试通过
- ✅ TypeScript 类型检查通过 (0 errors)
- ✅ ESLint 通过 (0 errors)
- ✅ 零 `any` 类型使用
- ✅ 无 `dangerouslySetInnerHTML` / `innerHTML`
- ✅ E2E 测试配置存在 (Playwright, 5 个核心页面 + a11y)

### 3.3 构建验证

| 产物 | 大小 | 说明 |
|------|------|------|
| Go 二进制 | 42 MB | 静态链接, 嵌入前端资源 |
| 前端产物 | 900 KB | 54 个文件 (含 .gz 预压缩) |
| 最大 JS chunk | 296 KB (gzip: 95 KB) | index-BZWHcnC-.js |

---

## 4. 安全审计详情

### 4.1 严重 (Critical) — 1 项

| ID | 文件 | 描述 | 状态 |
|----|------|------|------|
| C-1 | `internal/relay/handler.go:90` | **unsafe.String 零拷贝转换**: `unsafe.String(unsafe.SliceData(raw.Body), len(raw.Body))` 创建与 `raw.Body` 共享内存的 string。若底层 byte slice 被修改，string 内容将被静默篡改，违反 Go 字符串不可变契约。该 string 存入 `RequestState.body` 并被 `finish()` 异步读取。 | 🔴 未修复 |

**修复建议**: 使用 `string(raw.Body)` 进行安全拷贝。

### 4.2 高 (High) — 6 项

| ID | 文件 | 描述 | 状态 |
|----|------|------|------|
| H-1 | `internal/relay/state.go:414` | **time.After timer 泄漏**: `time.After` 创建的 timer 在 ctx.Done() 先触发时不会 Stop，高频重试场景下累积 timer 泄漏。 | 🔴 未修复 |
| H-2 | `internal/task/task.go:124` | **task.RUN() select{} 永久阻塞**: `RUN()` 通过 `select{}` 永久阻塞，无法优雅退出。 | 🔴 未修复 |
| H-3 | `internal/relay/handler.go:1062,1067,1084` | **sanitizeRequestBody 忽略 sjson 错误**: 三处 `raw.Body, _ = sjson.SetBytes(...)` 忽略错误，请求体可能处于不一致状态。 | 🔴 未修复 |
| H-4 | `internal/relay/route.go:310-338` | **recoverExpiredItems 并行 goroutine 无 WaitGroup**: 探测 goroutine 未被 WaitGroup 跟踪，可能泄漏。 | 🔴 未修复 |
| H-5 | `internal/relay/handler.go:120` | **第二处 unsafe.String**: mask 后的 body 同样使用 unsafe.String 零拷贝转换。 | 🔴 未修复 |
| H-6 | `internal/update/update.go:164,192` | **自更新解压文件权限不安全**: 使用 zip 文件自带的 mode bits 和 `os.ModePerm (0777)` 创建目录，恶意 zip 可设置可执行/可读位。 | 🔴 未修复 |

### 4.3 中 (Medium) — 14 项

| ID | 来源 | 描述 |
|----|------|------|
| M-1 | 后端 | unsafe.String 相关的内存模型风险 (C-1 的延伸) |
| M-2 | 后端 | context.Background() 在 loginratelimit 中使用，停机时可能 DB 已关闭 |
| M-3 | 后端 | O(N) 扫描在某些缓存路径中 |
| M-4 | 后端 | 内存泄漏在特定重试路径 |
| M-5 | 后端 | 其他 AUDIT_BACKEND.md 中记录的 Medium 项 |
| M-6 | 前端 | CSRF 依赖后端 Origin 保护，需确认 SameSite cookie + Origin 校验 |
| M-7 | DevOps | CI/CD 无 SAST 步骤 (无 gosec/staticcheck/golangci-lint) |
| M-8 | DevOps | 无 Dependabot 配置 |
| M-9 | DevOps | 配置 unmarshal 后无验证 |
| M-10 | DevOps | 管理 API 无限流 (仅登录有限流) |
| M-11 | DevOps | 自更新解压文件权限不安全 (同 H-6) |
| M-12 | DevOps | loginratelimit 使用 context.Background() (同 M-2) |
| M-13 | DevOps | .gitignore 缺少 .env / *.pem / *.key 等模式 |
| M-14 | DevOps | go.mod 中 axonhub/llm 为伪版本 (无 tag) |

### 4.3.1 Go 安全审计补充发现

Go 安全审计子代理额外发现以下问题 (均非 Critical/High):

| ID | 严重程度 | 文件 | 描述 |
|----|---------|------|------|
| GS-1 | Medium | 51 处 handlers | **内部错误细节泄露**: 51 个 handler 调用点直接将 `err.Error()` 返回给客户端，可能泄露数据库连接串、文件路径、网络错误等内部基础设施细节。均在 admin 认证之后，但被入侵的 admin 会话可利用。 |
| GS-2 | Low | `securityheaders.go` | **缺少 CSP 头**: 前端 index.html 含内联主题脚本，未实现 Content-Security-Policy。 |
| GS-3 | Low | `securityheaders.go` | **缺少 HSTS 头**: 未设置 Strict-Transport-Security，HTTPS 部署下存在降级攻击风险。 |
| GS-4 | Low | `go.mod` | **x/crypto v0.55.0 有 3 个未调用的漏洞**: SSH DoS (2个) + openpgp 未维护。代码仅使用 bcrypt，不受影响，但建议升级到 v0.56.0。 |

### 4.4 低 (Low) — 16 项

| ID | 来源 | 描述 |
|----|------|------|
| L-1~L-4 | 后端 | AUDIT_BACKEND.md 中记录的 Low 项 (缓存引用共享、错误包装等) |
| L-5 | 前端 | 修改密码表单无确认字段 (输错新密码可能锁定) |
| L-6 | 前端 | 允许 HTTP 上游 URL (可能明文传输 API key) |
| L-7 | 前端 | Settings 使用原生 confirm() 对话框 (不一致) |
| L-8 | 前端 | 未使用 devDep: depcheck |
| L-9 | 前端 | 7 个未使用导出 + 10 个未使用导出类型 |
| L-10 | DevOps | web-router 未 pin digest (nginx:1.27-alpine) |
| L-11 | DevOps | web-router 无资源限制/健康检查/安全加固 |
| L-12 | DevOps | web-next-ci.yaml checkout 未设 persist-credentials: false |
| L-13 | DevOps | 无 CODEOWNERS 文件 |
| L-14 | DevOps | data 目录创建权限 0755 (裸金属部署应为 0700) |
| L-15 | DevOps | cookie_secure 默认 false |
| L-16 | DevOps | update marker 文件权限 0o644 (应为 0o600) |

---

## 5. 安全亮点 (正面发现)

### 5.1 认证与授权 ✅

- **JWT**: HS256 签名 (拒绝 alg=none/RS256 等), 强制 exp 声明, 校验 issuer/audience, 有效期上限 30 天
- **Cookie**: SameSite=Lax, HttpOnly=true, Secure 基于 TLS 或配置自动启用
- **密码**: bcrypt (DefaultCost), 空密码拒绝, 用户名不匹配时执行等价 bcrypt (防时序侧信道)
- **API Key**: crypto/rand 生成 48 字符, sha256 存储 ID (非明文)
- **初始密码**: 写入 0600 权限文件, 从不日志输出
- **强制改密**: mustChangePassword 阻止除改密外所有路由

### 5.2 CSRF 防护 ✅

- OriginProtection 中间件: 不安全方法 (POST/PUT/DELETE 等) 校验 Origin/Referer
- 同源校验 + 显式 CORS 白名单 (禁止 "*" 通配)
- 不信任 X-Forwarded-* 头

### 5.3 SQL 注入防护 ✅

- 全部 GORM 查询使用参数化占位符 (`?`)
- 0 处 `fmt.Sprintf` 拼接 SQL
- 迁移 DDL 使用硬编码常量 (非用户输入)

### 5.4 XSS 防护 ✅

- 前端无 `dangerouslySetInnerHTML` / `innerHTML` / `eval()`
- React 自动转义所有文本内容
- 零 `any` 类型使用, API 响应使用 `unknown` + 运行时类型守卫

### 5.5 密码学 ✅

- 安全随机: 全部使用 `crypto/rand` (非 math/rand)
- 密码哈希: bcrypt with DefaultCost
- 文件完整性: SHA256 校验
- JWT: 拒绝非预期签名算法

### 5.6 Docker 安全 ✅

- 非 root 用户 (10001:10001)
- 只读根文件系统 + tmpfs
- cap_drop ALL + no-new-privileges
- 资源限制 (512MB 内存, 1 CPU, 512 PIDs)
- 健康检查 + 日志滚动
- umask 0077

### 5.7 CI/CD 安全 ✅

- GitHub Actions pin 到 commit SHA
- permissions: contents: read 最小权限
- Trivy 密钥扫描
- 并发控制 + 超时

### 5.8 请求安全 ✅

- Body 大小限制: API 8MB, 导入 128MB, relay 16MB 压缩/64MB 解压
- Slowloris 防护: ReadHeaderTimeout 10s
- 重定向保护: 最多 5 次, 阻止 HTTPS→HTTP 降级, 阻止跨域重定向
- 解压炸弹防护: LimitReader
- Zip slip 防护 + 符号链接跳过

### 5.9 日志安全 ✅

- API key 日志仅显示末 4 字符
- 代理 URL 凭据替换为 ****
- 错误日志递归脱敏 (authorization/apikey/token/password/secret)
- JWT 密钥从不通过 API 暴露
- gin.Logger() 仅 debug 模式启用

### 5.10 前端安全 ✅

- JWT httpOnly cookie (非 JS 可访问存储)
- 401 全局监听 → 自动登出
- 登出清理 React Query 缓存
- 密钥默认掩码显示
- ErrorBoundary 仅 DEV 模式显示详细信息
- 193 个 aria-* 属性, 强可访问性

---

## 6. 依赖漏洞扫描

### 6.1 Go 依赖 (govulncheck)

```
No vulnerabilities found.
代码受 0 个漏洞影响。
模块中存在 3 个漏洞但代码未调用。
```

✅ **无影响代码的漏洞**

### 6.2 前端依赖 (pnpm audit)

| 严重程度 | 包 | 版本 | 影响 | 说明 |
|---------|-----|------|------|------|
| Critical | vitest | >=2.0.0 <2.1.9 | RCE | ⚠️ devDep, 不影响生产 |
| Critical | vitest | <3.2.6 | 文件读取/执行 | ⚠️ devDep, 不影响生产 |
| High | vite | <=6.4.2 | fs.deny 绕过 | ⚠️ devDep, 不影响生产 |
| High | esbuild | <=0.24.2 | 开发服务器请求 | ⚠️ devDep, 不影响生产 |
| High | ini | <1.3.6 | 原型污染 | ⚠️ devDep (depcheck 间接) |
| High | nanoid | <5.1.16 | 死循环 | ⚠️ devDep (size-limit 间接) |
| High | extract-zip | <=2.0.1 | 符号链接遍历 | ⚠️ devDep (puppeteer 间接) |
| High | ansi-regex | >=5.0.0 <5.0.1 | ReDoS | ⚠️ devDep (testing-library 间接) |

> **注意**: 所有漏洞均在 devDependencies 中，不影响生产构建产物。但建议升级 vitest >= 2.1.9 和 vite >= 6.4.3。

---

## 7. 代码质量

### 7.1 Go 代码质量

| 指标 | 值 | 评价 |
|------|-----|------|
| go vet | 0 问题 | ✅ |
| TODO/FIXME | 0 | ✅ |
| err 忽略 (`_ =`) | 40 处 | ⚠️ 多为合理的清理操作 |
| panic 使用 | 3 处 | ⚠️ 均有 recover 保护 |
| unsafe 使用 | 2 处 | 🔴 见 C-1 |
| context.Background() | 15 处 | ⚠️ 部分应使用生命周期 context |
| go func | 8 处 | ⚠️ 4 处无 WaitGroup 跟踪 |
| 最大文件 | handler.go 1129 行 | ⚠️ 偏大 |

### 7.2 前端代码质量

| 指标 | 值 | 评价 |
|------|-----|------|
| TypeScript 严格模式 | ✅ | 零 any |
| ESLint | 0 错误 | ✅ |
| TODO/FIXME | 0 | ✅ |
| 未使用导出 | 7 个 | ⚠️ |
| 未使用导出类型 | 10 个 | ⚠️ |
| 未使用 devDep | 2 个 (depcheck, axe-core*) | ⚠️ |
| 最大文件 | channel-editor.tsx 1792 行 | ⚠️ 偏大 |
| dangerouslySetInnerHTML | 0 | ✅ |
| localStorage 存储敏感信息 | 0 | ✅ |

> *axe-core 为 knip 误报 (e2e/ 目录不在 tsconfig include 中)

---

## 8. 优先修复建议

### P0 — 立即修复

1. **替换 `unsafe.String`** → `string(raw.Body)` (`handler.go:90,120`)
   - 风险: 数据竞争, 字符串内容篡改
   - 修复: `requestBodyString := string(raw.Body)`

### P1 — 尽快修复

2. **修复 `sanitizeRequestBody` sjson 错误处理** (`handler.go:1062,1067,1084`)
   - 风险: 请求体不一致, 不必要重试
   - 修复: 检查返回错误, 失败时保留原始 body

3. **修复 `task.RUN()` select{} 阻塞** (`task.go:124`)
   - 风险: 无法优雅退出
   - 修复: 使用 `<-stopAllCh` 替代 `select{}`

4. **修复自更新解压文件权限** (`update.go:164,192`)
   - 风险: 恶意 zip 设置可执行/可读位
   - 修复: 固定 `0o755` (目录) / `0o644` (文件), 不使用 zip mode

### P2 — 计划修复

5. **替换 `err.Error()` 为通用错误常量** — 51 处 500 类响应使用通用消息，详细错误仅日志记录
6. **添加 SAST/SCA 到 CI** — golangci-lint (含 gosec) + trivy --scanners vuln
7. **添加 Dependabot 配置** — .github/dependabot.yml
8. **添加配置验证** — Validate() after viper.Unmarshal
9. **管理 API 限流** — 超越登录端点的全局限流
10. **升级前端 devDeps** — vitest >= 2.1.9, vite >= 6.4.3
11. **升级 x/crypto 到 v0.56.0** — 修复 3 个未调用漏洞
12. **添加 HSTS 头** — CookieSecure=true 时条件设置
13. **修复 loginratelimit context** — 使用服务器生命周期 context

### P3 — 改进建议

14. **扩展 .gitignore** — 添加 .env, *.pem, *.key, *.db 模式
15. **添加 CODEOWNERS** — 安全关键路径自动分配审查者
16. **添加 CSP 头** — 使用 nonce 或 hash 覆盖内联脚本
17. **清理未使用导出** — knip 报告的 7 个导出 + 10 个类型
18. **添加密码确认字段** — ChangePasswordForm 防止输错锁定
19. **替换原生 confirm()** — Settings.tsx 使用 ConfirmButton
20. **提高低覆盖率包测试** — handlers (9.1%), helper (9.4%), model (21.2%)

---

## 9. 结论

NovaVeil 是一个**安全意识强、工程质量高**的项目:

- **测试覆盖全面**: 736 个测试 (487 Go + 249 前端) 全部通过
- **安全实践优秀**: JWT/CSRF/CORS/SQL注入/XSS/密码学/日志脱敏 均有完善防护
- **Docker 加固到位**: 非 root/只读/cap_drop/资源限制/健康检查
- **依赖安全**: Go 0 漏洞, 前端漏洞仅限 devDeps
- **代码整洁**: 0 TODO/FIXME, 0 any 类型, 0 lint 错误

**主要风险**集中在 1 个 Critical (unsafe.String) 和 4 个 High (已有审计记录), 均为已知问题且有明确修复方案。新发现的问题多为 Medium/Low 级别的加固建议。

**建议优先修复 P0/P1 项**, 其余可按计划逐步改进。
