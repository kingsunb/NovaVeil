package op

import (
	"fmt"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm/clause"
)

// LoadOpencodeProxyAccounts 读取某渠道已记录的代理账号, 供进程内池懒加载。
func LoadOpencodeProxyAccounts(channelID int) ([]model.OpencodeProxyAccount, error) {
	conn := db.GetDB()
	if conn == nil {
		return nil, nil
	}
	var rows []model.OpencodeProxyAccount
	if err := conn.Where("channel_id = ?", channelID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取代理账号失败: %w", err)
	}
	return rows, nil
}

// SaveOpencodeProxySuccess 记录账号最近一次成功时间, 不存在则插入。
func SaveOpencodeProxySuccess(channelID int, account string, at time.Time) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	row := model.OpencodeProxyAccount{
		ChannelID:     channelID,
		Account:       account,
		LastSuccessAt: at,
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "account"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_success_at", "updated_at"}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存代理账号成功记录失败: %w", result.Error)
	}
	return nil
}

// SaveOpencodeProxyCooldown 记录账号冷却截止时间, 不存在则插入。
func SaveOpencodeProxyCooldown(channelID int, account string, until time.Time) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	row := model.OpencodeProxyAccount{
		ChannelID:     channelID,
		Account:       account,
		LastSuccessAt: until.Add(-7 * 24 * time.Hour),
		CooldownUntil: until,
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "account"}},
		DoUpdates: clause.AssignmentColumns([]string{"cooldown_until", "updated_at"}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存代理账号冷却记录失败: %w", result.Error)
	}
	return nil
}

// PruneOpencodeProxyAccounts 删除某渠道最旧的记录直到不超过 keep 条。
func PruneOpencodeProxyAccounts(channelID int, keep int) error {
	conn := db.GetDB()
	if conn == nil || keep <= 0 {
		return nil
	}
	var stale []uint
	err := conn.Model(&model.OpencodeProxyAccount{}).
		Where("channel_id = ?", channelID).
		Order("last_success_at DESC").
		Offset(keep).
		Pluck("id", &stale).Error
	if err != nil {
		return fmt.Errorf("查询待清理代理账号失败: %w", err)
	}
	if len(stale) == 0 {
		return nil
	}
	if err := conn.Delete(&model.OpencodeProxyAccount{}, stale).Error; err != nil {
		return fmt.Errorf("清理代理账号失败: %w", err)
	}
	return nil
}
