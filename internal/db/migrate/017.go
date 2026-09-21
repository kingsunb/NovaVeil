package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 17,
		Up:      migrateChannelModelUpstreamProtocol,
	})
}

// migrateChannelModelUpstreamProtocol 只给 channel_models 增加 upstream_protocol 列。
// 不改渠道类型、Key、启停、请求头或模型名单。列已存在时跳过加列。
// 空协议回填由 builtin 注册的钩子执行，且只填仍为空的值。
func migrateChannelModelUpstreamProtocol(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channel_models") {
		return nil
	}
	if !hasPhysicalColumn(db, "channel_models", "upstream_protocol") {
		if err := db.Migrator().AddColumn(&model.ChannelModel{}, "UpstreamProtocol"); err != nil {
			return fmt.Errorf("add channel_models.upstream_protocol: %w", err)
		}
	}
	if opencodeProtocolBackfill == nil {
		return nil
	}
	if err := opencodeProtocolBackfill(db); err != nil {
		return fmt.Errorf("backfill opencode upstream protocol: %w", err)
	}
	return nil
}
