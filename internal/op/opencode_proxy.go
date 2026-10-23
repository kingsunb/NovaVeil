package op

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
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
		Source:        model.OpencodeAccountSourceTraffic,
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
		Source:        model.OpencodeAccountSourceTraffic,
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

// UpsertOpencodeGeneratedAccounts 把体检页生成/粘贴的账号落账为未探测状态;
// 已存在的账号不覆盖任何字段。生成即落账, 支撑台账长期展示与筛选。
func UpsertOpencodeGeneratedAccounts(channelID int, accounts []string) error {
	conn := db.GetDB()
	if conn == nil || len(accounts) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]model.OpencodeProxyAccount, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, model.OpencodeProxyAccount{
			ChannelID: channelID,
			Account:   account,
			Source:    model.OpencodeAccountSourceGenerated,
			UpdatedAt: now,
		})
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "account"}},
		DoNothing: true,
	}).Create(&rows)
	if result.Error != nil {
		return fmt.Errorf("落账体检账号失败: %w", result.Error)
	}
	return nil
}

// SaveOpencodeProbeResult 记录账号最近一次体检结论; 不存在的账号按体检来源插入。
// 只更新探测字段, 成功时间与冷却由转发路径的回填逻辑维护。
func SaveOpencodeProbeResult(channelID int, account string, outcome string, status int, at time.Time) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	row := model.OpencodeProxyAccount{
		ChannelID:        channelID,
		Account:          account,
		Source:           model.OpencodeAccountSourceGenerated,
		LastProbeAt:      at,
		LastProbeOutcome: outcome,
		LastProbeStatus:  status,
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "account"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_probe_at", "last_probe_outcome", "last_probe_status", "updated_at"}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存账号体检结论失败: %w", result.Error)
	}
	return nil
}

// DeleteOpencodeProxyAccounts 删除台账中的指定账号, 返回删除行数。
func DeleteOpencodeProxyAccounts(channelID int, accounts []string) (int64, error) {
	conn := db.GetDB()
	if conn == nil || len(accounts) == 0 {
		return 0, nil
	}
	result := conn.Where("channel_id = ? AND account IN ?", channelID, accounts).
		Delete(&model.OpencodeProxyAccount{})
	if result.Error != nil {
		return 0, fmt.Errorf("删除代理账号失败: %w", result.Error)
	}
	return result.RowsAffected, nil
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

// EnsureOpencodeAccountAutoProbeSetting 保证自动体检开关的设置行存在并载入缓存。
// 该键不进 DefaultSettings, 随缓存初始化惰性补种默认值("1"开启; 台账为空时任务空转)。
func EnsureOpencodeAccountAutoProbeSetting(ctx context.Context) error {
	key := model.SettingKeyOpencodeAccountAutoProbe
	if _, ok := settingCache.Get(key); ok {
		return nil
	}
	gormDB := db.GetDB()
	if gormDB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB = gormDB.WithContext(ctx)

	var setting model.Setting
	err := gormDB.Where("key = ?", key).First(&setting).Error
	switch {
	case err == nil:
		settingCache.Set(setting.Key, setting.Value)
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("读取自动体检开关失败: %w", err)
	}
	setting = model.Setting{Key: key, Value: "1"}
	if err := gormDB.Create(&setting).Error; err != nil {
		// 并发补种时另一协程可能已插入成功, 重查一次兜底。
		var existing model.Setting
		if retryErr := gormDB.Where("key = ?", key).First(&existing).Error; retryErr == nil {
			settingCache.Set(existing.Key, existing.Value)
			return nil
		}
		return fmt.Errorf("补种自动体检开关失败: %w", err)
	}
	settingCache.Set(setting.Key, setting.Value)
	return nil
}
