package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 18,
		Up:      migrateDropChannelModelEvalFatalFailures,
	})
}

// migrateDropChannelModelEvalFatalFailures 删除 channel_models.eval_fatal_failures 列。
// 该列服务于「免费渠道模型评估确定性失败累计自动删除」功能，该功能已整体移除，
// 列成为孤儿列，需显式 drop（GORM AutoMigrate 从不删列）。
func migrateDropChannelModelEvalFatalFailures(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channel_models") {
		return nil
	}
	if err := dropColumnIfExists(db, &model.ChannelModel{}, "channel_models", "eval_fatal_failures"); err != nil {
		return fmt.Errorf("drop channel_models.eval_fatal_failures: %w", err)
	}
	return nil
}