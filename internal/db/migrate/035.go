package migrate

import (
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 35,
		Up:      deleteRetiredOpencodeFreeContextWindowSetting,
	})
}

// deleteRetiredOpencodeFreeContextWindowSetting 删除已下线的免费档全局上下文窗口设置行。
func deleteRetiredOpencodeFreeContextWindowSetting(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&model.Setting{}) {
		return nil
	}
	return db.Where("key = ?", "opencode_free_context_window").Delete(&model.Setting{}).Error
}
