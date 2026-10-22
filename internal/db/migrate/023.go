package migrate

import (
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 23,
		Up:      disableRetiredUncloseAIFree,
	})
}

// 停用已下线的 UncloseAI 内置渠道。
func disableRetiredUncloseAIFree(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") || !hasPhysicalColumn(db, "channels", "enabled") {
		return nil
	}
	return db.Table("channels").
		Where("builtin = ? AND name = ? AND enabled = ?", true, "UncloseAI Free", true).
		Update("enabled", false).Error
}
