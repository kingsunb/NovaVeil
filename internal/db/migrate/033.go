package migrate

import (
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 33,
		Up:      deleteOrphanOpencodeProxyAccounts,
	})
}

// 删除无主账号池。
func deleteOrphanOpencodeProxyAccounts(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") || !db.Migrator().HasTable("opencode_proxy_accounts") {
		return nil
	}
	return db.Where("channel_id NOT IN (?)", db.Model(&model.Channel{}).Select("id")).
		Delete(&model.OpencodeProxyAccount{}).Error
}
