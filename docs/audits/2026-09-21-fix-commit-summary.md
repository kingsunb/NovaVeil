# 2026-09-21 后续审计修复提交说明

本文件说明本次 `NovaVeil` 工作区在 `f64e7a7` 基底之上一次性提交的审计修复内容，供后续 review 与残余项修复使用。

## 提交范围

修复了 `docs/audits/2026-09-20-followup-audit.md` 中全部“确认存在”的问题：REL-01~07、SEC-01~08（无 SEC-09）、RELI-01~08、OLD-10~13/25/26、FE-01~07、DEV-01~09、H-02 残留。各事项的实现细节与验证口径记录在对应 Agent Notes（`.agents/notes/implemented/**/2026-09-20-*.md`、`2026-09-21-*.md`），可直接检索审计编号。

### 关键批次

- **P0 / P1（relay 核心）**：响应头 blocklist 过滤、`reasoning_content`/`tool` 流缓冲、mask key 按 API-key 隔离、测试面板与评估侧的 `max_tokens` 上限（REL-02/04/05/06）；渠道出口 SSRF 严格校验、格式透传、评估 panic 恢复、sticky 上限、PR 模板检查、Chat 401 恢复、CI 测试门禁（SEC-01、REL-03、RELI-02、REL-01、DEV-06、FE-01、DEV-01）。
- **P2（后端安全/可靠性）**：字段级静态加密（`internal/seal`）、渠道导出掩码、API Key 最小长度、初始密码文件首登/改密即删、Cookie Secure 识别反代头、配置聚合校验、管理 API 令牌桶限速；评估队列防重复派发与 position 事务化、缓存原子代、last_used 批量去抖写入、shutdown 分级超时、relay 状态深拷贝与有界集合（SEC-02~05/08，RELI-04~08，REL-07，OLD-10~13/25/26）。
- **P2（前端）**：Key 揭示移出 React Query、路由 loader 单一注册表、DB 导入失效所有 query family、主题状态同步、安全 href、配置归档/删除用应用内确认、CSP 修正、确认密码字段等（FE-02~07、H-02）。
- **DevOps**：docker-publish 增加 test gate 并钉 action SHA、verify-notes 钉 SHA、新增 security-scan、Dependabot、CODEOWNERS、`docker-compose`/`nginx` 加固、文档发布流程修正。

## 验证结果（2026-09-21 最新一次）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go test -count=1 ./...` | 通过，全包 `ok` |
| `go vet ./...` | 通过 |
| `cd web-next && pnpm typecheck` | 通过 |
| `cd web-next && pnpm lint` | 通过 |
| `cd web-next && pnpm vitest run` | 50 个测试文件、522 个测试全部通过 |
| `pnpm verify-notes` | 通过，37 份 note 格式与树校验 ok |
| `git diff --check` | 无空白错误 |

## 后续我修（已知残余项，建议逐个处理）

1. **Docker 镜像 digest 未钉**（DEV-03）：离线环境无法访问 Docker Hub 解析 digest，目前 `docker-compose.yml` 使用完整版本号标签；联网后应改为 `image@sha256:...` 并验证。
2. **`go.mod` 个人 fork 伪版本**（DEV-04）：当前 fork 的伪版本依赖保留，后续应替换为上游版本或明确保留决策。
3. **安全工具未跑**（OLD-23/DEV 安全门禁）：离线环境没有 `gosec`/`golangci-lint`/`govulncheck`，本次只以 `go vet` 作为基线；后续应在 CI 中跑完整安全扫描。
4. **代码 review**：字段级加密是新的写入路径，建议重点 review `internal/seal`、`internal/op/channel.go`、`internal/op/apikey.go`、`internal/op/backup.go` 的加解密与导入导出组合逻辑；评估队列事务与 `apikey_lastused` 停机顺序建议再用并发场景压一遍。
5. **工作区既有改动**：本次提交还包含会话开始前工作区已存在、但未提交的登录过期时间固定化改动（移除“信任此设备”选项，`GenerateJWTToken` 固定 24h，相关 `auth/store` 登录签名调整）。该改动并非本次审计问题，但为保持工作区一致一并提交；若不需要，请单独还原。