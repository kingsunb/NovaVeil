package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 29,
		Up:      deleteRetiredOpencodeFreeV1Channels,
	})
}

// retiredOpencodeFreeV1Names 是已停止支持的 1.x 免费档内置渠道。
var retiredOpencodeFreeV1Names = []string{
	"OpenCode Free V1",
	"OpenCode Free V1 账号池",
	"OpenCode Free V1 备用1",
	"OpenCode Free V1 备用2",
}

// DeleteRetiredOpencodeFreeV1Channels 幂等清理被旧版本重新补回的 V1 内置渠道。
// 迁移 29/32 只在升级当次执行, 旧实例或备份可能在记录成功后把行补回;
// 启动时复用同一清理逻辑可消除该残留。
func DeleteRetiredOpencodeFreeV1Channels(db *gorm.DB) error {
	return deleteRetiredOpencodeFreeV1Channels(db)
}

// 删除 v1 内置渠道, 并按 op.ChannelDel 的口径级联清理派生数据。
func deleteRetiredOpencodeFreeV1Channels(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var channelIDs []int
		if err := tx.Model(&model.Channel{}).
			Where("builtin = ? AND name IN ?", true, retiredOpencodeFreeV1Names).
			Pluck("id", &channelIDs).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode v1 渠道失败: %w", err)
		}
		if len(channelIDs) == 0 {
			return nil
		}
		var modelIDs []int
		if err := tx.Model(&model.ChannelModel{}).
			Where("channel_id IN ?", channelIDs).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode v1 渠道模型失败: %w", err)
		}
		if len(modelIDs) > 0 {
			itemIDs := tx.Model(&model.GroupItem{}).Select("id").Where("channel_model_id IN ?", modelIDs)
			if err := tx.Model(&model.Group{}).Where("active_item_id IN (?)", itemIDs).
				Update("active_item_id", 0).Error; err != nil {
				return fmt.Errorf("清除 v1 渠道的生效成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.GroupItem{}).Error; err != nil {
				return fmt.Errorf("删除 v1 渠道的分组成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.ModelEvalRank{}).Error; err != nil {
				return fmt.Errorf("删除 v1 渠道的评估排序失败: %w", err)
			}
		}
		if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.ChannelModel{}).Error; err != nil {
			return fmt.Errorf("删除 v1 渠道模型失败: %w", err)
		}
		if tx.Migrator().HasTable("token_densities") {
			if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.TokenDensity{}).Error; err != nil {
				return fmt.Errorf("删除 v1 渠道密度失败: %w", err)
			}
		}
		if tx.Migrator().HasTable("opencode_proxy_accounts") {
			if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.OpencodeProxyAccount{}).Error; err != nil {
				return fmt.Errorf("删除 v1 渠道账号失败: %w", err)
			}
		}
		if err := tx.Where("id IN ?", channelIDs).Delete(&model.Channel{}).Error; err != nil {
			return fmt.Errorf("删除 v1 渠道失败: %w", err)
		}
		return nil
	})
}
