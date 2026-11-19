package migrate

import (
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 34,
		Up:      deleteOrphanTokenDensities,
	})
}

// 删除无主密度。
func deleteOrphanTokenDensities(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") || !db.Migrator().HasTable("token_densities") {
		return nil
	}
	return db.Where("channel_id NOT IN (?)", db.Model(&model.Channel{}).Select("id")).
		Delete(&model.TokenDensity{}).Error
}
