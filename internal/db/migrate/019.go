package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 19,
		Up:      reBackfillOpencodeProtocols,
	})
}

// reBackfillOpencodeProtocols 重跑一次 OpenCode 空协议回填。
// 迁移 017 只在首次加列时回填过一次；之后新增进 OpencodeZenSeedProtocols 的模型
// （如 muse-spark contributor 免费模型 → responses）在存量库上仍是空协议，会回退成
// Chat 出站并触发上游 tools[0] missing required field name。这里复用 builtin 注册的
// 回填钩子再跑一轮，幂等：只写仍为空的 upstream_protocol，不覆盖管理员显式值。
func reBackfillOpencodeProtocols(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channel_models") {
		return nil
	}
	if !hasPhysicalColumn(db, "channel_models", "upstream_protocol") {
		return nil
	}
	if opencodeProtocolBackfill == nil {
		return nil
	}
	if err := opencodeProtocolBackfill(db); err != nil {
		return fmt.Errorf("re-backfill opencode upstream protocol: %w", err)
	}
	return nil
}
