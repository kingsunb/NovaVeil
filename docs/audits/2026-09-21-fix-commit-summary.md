# 2026-09-21 后续审计修复提交说明

本文件说明本次 `NovaVeil` 工作区以 `f64e7a7` 基底为基础，分两轮完成的审计修复内容，供后续 review 与部署核对。

## 提交范围

修复了 `docs/audits/2026-09-20-followup-audit.md` 中全部“确认存在”的问题：REL-01~07、SEC-01~08（无 SEC-09）、RELI-01~08、OLD-10~13/25/26、FE-01~07、DEV-01~09、H-02 残留。各事项的实现细节与验证口径记录在对应 Agent Notes（`.agents/notes/implemented/**/2026-09-20-*.md`、`2026-09-21-*.md`），可直接检索审计编号。

### 关键批次

- **P0 / P1（relay 核心）**：响应头 blocklist 过滤、`reasoning_content`/`tool` 流缓冲、mask key 按 API-key 隔离、测试面板与评估侧的 `max_tokens` 上限（REL-02/04/05/06）；渠道出口 SSRF 严格校验、格式透传、评估 panic 恢复、sticky 上限、PR 模板检查、Chat 401 恢复、CI 测试门禁（SEC-01、REL-03、RELI-02、REL-01、DEV-06、FE-01、DEV-01）。
- **P2（后端安全/可靠性）**：字段级静态加密（`internal/seal`）、渠道导出掩码、API Key 最小长度、初始密码文件首登/改密即删、Cookie Secure 识别反代头、配置聚合校验、管理 API 令牌桶限速；评估队列防重复派发与 position 事务化、缓存原子代、last_used 批量去抖写入、shutdown 分级超时、relay 状态深拷贝与有界集合（SEC-02~05/08，RELI-04~08，REL-07，OLD-10~13/25/26）。
- **P2（前端）**：Key 揭示移出 React Query、路由 loader 单一注册表、DB 导入失效所有 query family、主题状态同步、安全 href、配置归档/删除用应用内确认、CSP 修正、确认密码字段等（FE-02~07、H-02）。
- **DevOps**：docker-publish 增加 test gate 并钉 action SHA、verify-notes 钉 SHA、新增 security-scan、Dependabot、CODEOWNERS、`docker-compose`/`nginx` 加固、文档发布流程修正。

## 第二轮：残余清单闭合

上一版总结中的 5 个残余项已全部处理；随后两路代码 review（加密/备份面 + 并发面，`docs/audits` 已纳入）发现的 P1/P2 修复也已合入：

1. **Docker 镜像 digest 已钉（DEV-03）**
   - `web-next/Dockerfile` builder 钉 `node:22.19.0-alpine@sha256:d2166de198f26e17e5a442f537754dd616ab069c47cc57b889310a717e0abbf9`；runtime 与 `docker-compose.yml` 的 `web-router` 钉 `nginx:1.27.5-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10`。digest 来自两个独立公共镜像代理核对一致。
2. **`go.mod` fork/伪版本决策闭环（DEV-04）**
   - `gin-contrib/sse` 上游已发布 `v1.1.2`，已切回官方版本，fork replace 移除；`tmaxmax/go-sse` 因上游缺少 `Stream`/`NewStreamWithConfig`（`looplj/axonhub/llm httpclient/decoder.go` 依赖），保留 `replace => github.com/looplj/go-sse v0.0.0-20250909130008-e74a1155bc3b`，分支决策见 `.agents/notes/implemented/process/2026-09-21-remove-gin-contrib-sse-fork-keep-go-sse-fork.md`。
3. **安全工具已跑（OLD-23/DEV 安全门禁）**
   - `gosec -quiet -severity=high -exclude=G115 ./...` 通过（v2.29.0）；`govulncheck ./...` 通过（v1.8.0，0 个可达漏洞）。
4. **代码 review 修复已合入**
   - **加密/备份面**：备份脱敏哨兵精确匹配、导入拒绝脱敏 API Key 并校验 Key 长度、导入渠道 BaseURL 做完整 egress 校验、`validateEgressIP` 拒绝 IPv4-compatible IPv6 字面量与 `100.64.0.0/10`/`192.0.2.0/24`/`198.18.0.0/15`/`240.0.0.0/4` 等保留段、内置渠道 Key 密文落库、导入对象数限制补上 `client_stats`/`usage_buckets`、`channel_models`/`usage_buckets` 的 natural-key 冲突在预检中计数、文本渠道导入拒绝 `****` 掩码。
   - **并发面**：评估队列入队多实例互斥（MySQL `GET_LOCK`/PostgreSQL advisory lock）、`Stop`/`MoveUp` 条件更新、running 租约 15 分钟 + scheduler 回收；cache `refreshMu` 防写入退休旧代；`apikey_lastused` 停机排干 pending；`error_log` 停机 flush 带 35s 等待与同步直写兜底；`client_stat` 淘汰行转 orphaned 防丢失；shutdown `hookWG` 等待所有钩子 goroutine + finalizer 顺序；eval scheduler `stateMu` 幂等 Start/Stop。
5. **工作区既有改动归档说明**：登录过期时间固定化改动（移除“信任此设备”选项，`GenerateJWTToken` 固定 24h，相关 `auth/store` 登录签名调整）并非审计问题，但随工作区一致性一并纳入提交，见 `.agents/notes/implemented/simplification/2026-09-21-remove-client-configurable-login-expire.md`。

## 验证结果（2026-09-21 最终）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go test -count=1 ./...` | 通过，全包 `ok` |
| `go vet ./...` | 通过 |
| `cd web-next && pnpm typecheck` | 通过 |
| `cd web-next && pnpm lint` | 通过 |
| `cd web-next && pnpm vitest run` | 50 个测试文件、522 个测试全部通过 |
| `pnpm verify-notes` | 通过，38 份 note 格式与树校验 ok |
| `gosec -quiet -severity=high -exclude=G115 ./...` | 通过 |
| `govulncheck ./...` | 0 个可达漏洞 |
| `git diff --check` | 无空白错误 |
