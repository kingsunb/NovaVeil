package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 27,
		Up:      scopeOpencodeProxyAccountsByKey,
	})
}

// scopeOpencodeProxyAccountsByKey 把代理账号台账的唯一键从
// (channel_id, account) 扩为 (channel_id, key_id, account)，让 keyed 免费渠道
// 的每个 Key 拥有独立账号池；存量行 key_id 留空，仍归旧单 Key/公共 Key 渠道。
func scopeOpencodeProxyAccountsByKey(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&model.OpencodeProxyAccount{}) {
		return nil
	}
	if !hasPhysicalColumn(db, "opencode_proxy_accounts", "key_id") {
		return nil
	}
	if err := db.Exec(`UPDATE opencode_proxy_accounts SET key_id = '' WHERE key_id IS NULL`).Error; err != nil {
		return fmt.Errorf("回填代理账号 KeyID 失败: %w", err)
	}
	const oldIndex = "idx_oc_proxy_channel_account"
	if db.Migrator().HasIndex(&model.OpencodeProxyAccount{}, oldIndex) {
		if err := db.Migrator().DropIndex(&model.OpencodeProxyAccount{}, oldIndex); err != nil {
			return fmt.Errorf("删除旧代理账号唯一索引失败: %w", err)
		}
	}
	const newIndex = "idx_oc_proxy_channel_key_account"
	if !db.Migrator().HasIndex(&model.OpencodeProxyAccount{}, newIndex) {
		if err := db.Migrator().CreateIndex(&model.OpencodeProxyAccount{}, newIndex); err != nil {
			return fmt.Errorf("创建代理账号 KeyID 唯一索引失败: %w", err)
		}
	}
	return nil
}
