package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 24,
		Up:      deleteRetiredOpencodeFreeDuplicates,
	})
}

// retiredOpencodeFreeDuplicateNames 是已下线的 OpenCode Free 内置渠道副本名称。
var retiredOpencodeFreeDuplicateNames = []string{"OpenCode Free 2", "OpenCode Free 3"}

// 删除 OpenCode Free 2/3 内置渠道行, 并按 op.ChannelDel 的口径级联清理派生数据。
func deleteRetiredOpencodeFreeDuplicates(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var channelIDs []int
		if err := tx.Model(&model.Channel{}).
			Where("builtin = ? AND name IN ?", true, retiredOpencodeFreeDuplicateNames).
			Pluck("id", &channelIDs).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode 副本渠道失败: %w", err)
		}
		if len(channelIDs) == 0 {
			return nil
		}
		var modelIDs []int
		if err := tx.Model(&model.ChannelModel{}).
			Where("channel_id IN ?", channelIDs).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode 副本模型失败: %w", err)
		}
		if len(modelIDs) > 0 {
			itemIDs := tx.Model(&model.GroupItem{}).Select("id").Where("channel_model_id IN ?", modelIDs)
			if err := tx.Model(&model.Group{}).Where("active_item_id IN (?)", itemIDs).
				Update("active_item_id", 0).Error; err != nil {
				return fmt.Errorf("清除副本渠道的生效成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.GroupItem{}).Error; err != nil {
				return fmt.Errorf("删除副本渠道的分组成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.ModelEvalRank{}).Error; err != nil {
				return fmt.Errorf("删除副本渠道的评估排序失败: %w", err)
			}
		}
		if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.ChannelModel{}).Error; err != nil {
			return fmt.Errorf("删除副本渠道模型失败: %w", err)
		}
		if err := tx.Where("id IN ?", channelIDs).Delete(&model.Channel{}).Error; err != nil {
			return fmt.Errorf("删除副本渠道失败: %w", err)
		}
		return nil
	})
}
