package migrate

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 28,
		Up:      deleteRetiredOpencodeFreeLegacyChannels,
	})
}

// 旧名渠道到保留渠道的映射。
var retiredOpencodeFreeLegacyChannels = []struct {
	Name   string
	Target string
}{
	{Name: "OpenCode Free", Target: "OpenCode Free V2"},
	{Name: "OpenCode Free Old", Target: "OpenCode Free V1"},
}

// 删除旧名渠道, 迁移账号并级联清理。
func deleteRetiredOpencodeFreeLegacyChannels(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") {
		return nil
	}
	names := make([]string, 0, len(retiredOpencodeFreeLegacyChannels))
	for _, retired := range retiredOpencodeFreeLegacyChannels {
		names = append(names, retired.Name)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		type channelRow struct {
			ID   int
			Name string
		}
		var rows []channelRow
		if err := tx.Model(&model.Channel{}).Select("id", "name").
			Where("builtin = ? AND name IN ?", true, names).
			Scan(&rows).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode 旧名渠道失败: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		channelIDs := make([]int, 0, len(rows))
		for _, row := range rows {
			channelIDs = append(channelIDs, row.ID)
			var targetName string
			for _, retired := range retiredOpencodeFreeLegacyChannels {
				if retired.Name == row.Name {
					targetName = retired.Target
					break
				}
			}
			if targetName == "" {
				continue
			}
			targetID, err := opencodeLegacyTargetChannelID(tx, targetName)
			if err != nil {
				return fmt.Errorf("查找 OpenCode 旧名渠道的迁移目标 %s 失败: %w", targetName, err)
			}
			if targetID == 0 {
				// 目标缺失时保留账号, 避免数据丢失。
				continue
			}
			if err := migrateOpencodeLegacyProxyAccounts(tx, row.ID, targetID); err != nil {
				return fmt.Errorf("迁移 OpenCode 旧名渠道 %s 的代理账号失败: %w", row.Name, err)
			}
		}
		var modelIDs []int
		if err := tx.Model(&model.ChannelModel{}).
			Where("channel_id IN ?", channelIDs).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("查找待下线的 OpenCode 旧名渠道模型失败: %w", err)
		}
		if len(modelIDs) > 0 {
			itemIDs := tx.Model(&model.GroupItem{}).Select("id").Where("channel_model_id IN ?", modelIDs)
			if err := tx.Model(&model.Group{}).Where("active_item_id IN (?)", itemIDs).
				Update("active_item_id", 0).Error; err != nil {
				return fmt.Errorf("清除 OpenCode 旧名渠道的生效成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.GroupItem{}).Error; err != nil {
				return fmt.Errorf("删除 OpenCode 旧名渠道的分组成员失败: %w", err)
			}
			if err := tx.Where("channel_model_id IN ?", modelIDs).Delete(&model.ModelEvalRank{}).Error; err != nil {
				return fmt.Errorf("删除 OpenCode 旧名渠道的评估排序失败: %w", err)
			}
		}
		if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.ChannelModel{}).Error; err != nil {
			return fmt.Errorf("删除 OpenCode 旧名渠道模型失败: %w", err)
		}
		if err := tx.Where("id IN ?", channelIDs).Delete(&model.Channel{}).Error; err != nil {
			return fmt.Errorf("删除 OpenCode 旧名渠道失败: %w", err)
		}
		return nil
	})
}

// 只匹配内置渠道。
func opencodeLegacyTargetChannelID(tx *gorm.DB, name string) (int, error) {
	var ids []int
	if err := tx.Model(&model.Channel{}).Where("builtin = ? AND name = ?", true, name).
		Order("id").Limit(1).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// 复制台账, 冲突时保留目标行。
func migrateOpencodeLegacyProxyAccounts(tx *gorm.DB, sourceID, targetID int) error {
	if !tx.Migrator().HasTable(&model.OpencodeProxyAccount{}) {
		return nil
	}
	var accounts []model.OpencodeProxyAccount
	if err := tx.Where("channel_id = ?", sourceID).Find(&accounts).Error; err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].ID = 0
		accounts[i].ChannelID = targetID
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&accounts[i]).Error; err != nil {
			return err
		}
	}
	return tx.Where("channel_id = ?", sourceID).Delete(&model.OpencodeProxyAccount{}).Error
}
