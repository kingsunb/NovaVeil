# Agent Note: migration 005 用 hasPhysicalColumn 物理列探查并对缺列降级

Status: implemented

## 问题

Migration 005 把多地址、多凭据渠道收敛为单 `base_url`/`key`，原实现用 `db.Migrator().HasColumn("channels","base_url")` 与 `HasColumn("channels","key")` 做前置检查，任一缺失即 `return fmt.Errorf(...)` 硬报错。两个问题叠加，阻断 SQLite 跨版本升级：

1. **模糊匹配假阳性**：GORM 的 `Migrator().HasColumn` 在 SQLite 下不查 `PRAGMA table_info`，而是解析建表 DDL 做子串匹配。`channels` 的 `"id integer PRIMARY KEY AUTOINCREMENT"` 里含 `key`，`HasColumn("channels","key")` 返回 true——列物理上不存在却判为存在，后续回填写到不存在的列上行为未定义。
2. **硬报错误杀合法状态**：`channels.base_url` 与 `channels.key` 在 Migration 11 后已合法消失（`base_url`→`base_urls` 数组、`key`→`channel_keys` 表）。把它们的缺失当错误，等于让一个本该幂等跳过的迁移在列已迁移走的状态下失败。

## 决定

移植自 octopus `1cdb163`。Migration 005 不再硬报错，改用 `hasPhysicalColumn`（`migrate.go:165`，走 `Migrator().ColumnTypes` —— SQLite 下即 `PRAGMA table_info`，MySQL/PG 下即 `information_schema`，物理列探查不依赖建表语句文本）探测列，按目标列是否存在决定对应回填是否执行：

- `hasBaseURL := hasPhysicalColumn(db,"channels","base_url")`、`hasKey := hasPhysicalColumn(db,"channels","key")`。
- `base_urls`→`base_url` 回填 gated on `hasBaseURL && hasPhysicalColumn(...,"base_urls")`。
- `channel_keys`→`channels.key` 回填 gated on `hasKey && HasTable("channel_keys") && hasPhysicalColumn(db,"channel_keys","channel_key")`。
- `base_urls` 列删除 gated on `hasPhysicalColumn(db,"channels","base_urls")`，SQLite 走 `ALTER TABLE ... DROP COLUMN`（`DropColumn` 在旧 SQLite 上不可靠）。

列不存在时回填整段跳过、不报错；`channels` 表不存在时迁移直接返回 nil。

## 备选方案

- **保留 `HasColumn` 但放宽为软告警** — 最强论据是改动最小；否掉因为模糊匹配的假阳性没修，`HasColumn("channels","key")` 仍会误判 true，回填仍会写到不存在的列上，告警掩盖了根因。
- **给 GORM Migrator 打补丁修模糊匹配** — 最强论据是从根上修所有调用点；否掉因为那是上游 GORM 行为，NovaVeil 不维护 GORM，且 `hasPhysicalColumn` 已是仓内既有 helper（`migrate.go` 多处复用），无需引入外部补丁。

## 后果

- **收益**：SQLite 跨版本升级不再被 Migration 005 阻断；列探测反映物理 schema 而非建表语句文本，假阳性消除；列已迁移走时迁移幂等跳过而非失败。
- **代价与已知上限**：`hasPhysicalColumn` 在 `ColumnTypes` 出错时返回 false（静默跳过回填），极端情况下可能漏回填——但出错本身意味着表不可读，后续 AutoMigrate 会先暴露。重访信号：若未来有迁移同样依赖列存在性判断，必须用 `hasPhysicalColumn` 而非 `Migrator().HasColumn`。

## 验证

- `internal/db/migrate/005.go` 全部列探测走 `hasPhysicalColumn`；`hasPhysicalColumn` 定义在 `internal/db/migrate/migrate.go:165`，`dropColumnIfExists` 等同文件其他迁移复用同一 helper。
- 全新 SQLite 库本地部署：迁移无报错跑通，`channels`/`channel_keys` 表结构正确；`go test ./...` 全绿。
