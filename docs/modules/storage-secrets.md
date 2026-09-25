# 数据与凭据（internal/db · op · model · seal）

存储层是 GORM 单库三方言，外加 `seal` 静态加密与进程内缓存。所有凭据类字段落库前统一走 `nv1:` 密文。

## 怎么做的

### 表与方言

- AutoMigrate 建 16 张表：User、Channel、ChannelModel、ModelEval / ModelEvalStats / ModelEvalRank / ModelEvalQueueTask（评估 4 张）、Group、GroupItem、APIKey、Setting、LoginAttempt、ErrorLog、ClientStat、UsageBucket、MigrationRecord。
- SQLite（glebarez 纯 Go）按 WAL 单写者收缩连接池并设 busy_timeout；MySQL / PostgreSQL 连接池 100；Postgres 在迁移完成后执行 `DEALLOCATE ALL` 清理陈旧预编译计划。

### 迁移

- `db/migrate/` 编号迁移（003 起）+ `migration_records` 幂等判重；`BeforeAutoMigrate` 与 `AfterAutoMigrate` 两阶段——需要先清重才能建唯一索引的迁移放 Before；失败记 FAILED 后中止启动，下次启动重跑收敛，成功记录写入带重试防抖。

### seal 静态加密

- AES-256-GCM，nonce 每次加密用 crypto/rand 生成 12 字节；密钥文件 0600、所在目录 0700。
- 密钥文件损坏（内容非法）启动即拒绝，不裸写明文；无 `nv1:` 前缀的存量明文兼容读、写路径恒为密文。
- 启动链保证 `seal.Configure` 先于内置渠道补建、缓存与任何业务读写。
- 覆盖范围：渠道 Key、多 Key、渠道代理、自定义头值、头模板、JWT 密钥、全局代理、API Key。

### 备份导入导出

- 数据库导出中**渠道 Key 与 API Key 是明文**（管理员自助迁移的有意决策），代理 / 自定义头值 / 头模板打成 `****`，`auth_jwt_secret` / `proxy_url` / `proxy_pool` 整行剔除；导入侧同规则过滤。
- 导入路径全仓最防御：dry-run 预检 + 128MB / 10 万对象双层上限 + 应用内二次确认 + 精确 `****` 哨兵拒绝（防止掩码数据回写毁掉真值）+ 密文解不开即中止 + 导入对象数与自然键冲突预检计数。
- 敏感导出一律 POST + NoStore + IP 告警。

### 进程内缓存

- `op.InitCache` 加载渠道 / 分组 / 密钥快照，读路径全部走内存；`refreshMu` 防止写入已退休的旧代缓存。

## 设计想法

- **落库密文、导出明文**：库被拖走时不泄漏凭据（静态加密），但备份必须能离机还原——把"明文备份"定位为管理员自担的凭据文件处理义务，文档反复强调备份文件视同生产凭据。见决策记录 [2026-09-21-credential-at-rest-encryption-and-export-redaction](../../.agents/notes/implemented/bug-fix/2026-09-21-credential-at-rest-encryption-and-export-redaction.md)。
- **导入拒绝精确 `****`**：掩码哨兵一旦被当成真值写回，对应的渠道密钥就永久损毁——哨兵拒绝是数据完整性防线，不是脱敏功能。
- **编号迁移而非全包 AutoMigrate**：历史 schema 变更包含 AutoMigrate 表达不了的数据修正（清重、列改型），编号 + 幂等判重让升级路径可审计可重跑。

## 已知边界

- 密钥文件缺失时生成新密钥并伴随启动 WARN 日志（首次部署属正常；已有 `nv1:` 密文数据时旧密文不可解，需从备份恢复密钥文件）——备份密钥文件是运维义务；轮换 / re-seal 工具仍缺，列为 ROADMAP 未来项。
- 多实例共库同时启动的迁移互斥明确不做：单管理员、单实例是支持的部署形态，多实例不受支持（ROADMAP「明确不做」）。
- `seal` 与 `db/migrate` 两个包目前没有单测文件，列为 ROADMAP 未来项。
- Postgres 的 `DEALLOCATE ALL` 只作用于池中单条连接，其余连接的陈旧计划不保证被清理。

## 深入阅读

- 操作手册：[BACKUP_RESTORE.md](../BACKUP_RESTORE.md)
- 加密决策：[2026-09-21-credential-at-rest-encryption-and-export-redaction](../../.agents/notes/implemented/bug-fix/2026-09-21-credential-at-rest-encryption-and-export-redaction.md)
