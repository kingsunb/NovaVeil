package migrate

import (
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 21,
		Up:      enableFreeBuiltinAutoSync,
	})
}

// enableFreeBuiltinAutoSync 把已落库的内置免费渠道打开自动同步。
//
// 背景：免费渠道出厂改为跟随上游模型列表（AutoSync=true、种子模型标 auto），
// 但 EnsureBuiltinChannels 只补建缺失渠道、不覆盖既有记录，存量库里的内置免费
// 渠道仍停留在 AutoSync=false，升级后不会拉取真实模型。这里统一打开；
// 管理员后续手动关闭不会被再次覆盖（迁移只跑一次）。
func enableFreeBuiltinAutoSync(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return db.Exec(
		"UPDATE channels SET auto_sync = ? WHERE builtin = ? AND is_free = ?",
		true, true, true,
	).Error
}
