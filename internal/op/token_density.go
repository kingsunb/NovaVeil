package op

import (
	"context"
	"errors"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TokenDensityView 密度展示行。
type TokenDensityView struct {
	ChannelID   int       `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	Model       string    `json:"model"`
	Density     float64   `json:"density"`
	SampleCount int64     `json:"sample_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TokenDensityGet 读取密度。
func TokenDensityGet(ctx context.Context, channelID int, modelName string) (model.TokenDensity, bool, error) {
	if db.GetDB() == nil {
		return model.TokenDensity{}, false, nil
	}
	var row model.TokenDensity
	err := db.GetDB().WithContext(ctx).
		Where("channel_id = ? AND model = ?", channelID, modelName).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.TokenDensity{}, false, nil
	}
	if err != nil {
		return model.TokenDensity{}, false, err
	}
	return row, true, nil
}

// TokenDensitySave 写入密度。
func TokenDensitySave(ctx context.Context, channelID int, modelName string, density float64, sampleCount int64, dailySamples int, sampleDay string) error {
	if db.GetDB() == nil {
		return nil
	}
	row := model.TokenDensity{
		ChannelID:    channelID,
		Model:        modelName,
		Density:      density,
		SampleCount:  sampleCount,
		DailySamples: dailySamples,
		SampleDay:    sampleDay,
		UpdatedAt:    time.Now(),
	}
	return db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"density":       density,
			"sample_count":  sampleCount,
			"daily_samples": dailySamples,
			"sample_day":    sampleDay,
			"updated_at":    row.UpdatedAt,
		}),
	}).Create(&row).Error
}

// TokenDensityList 列出密度。
func TokenDensityList(ctx context.Context) ([]TokenDensityView, error) {
	if db.GetDB() == nil {
		return []TokenDensityView{}, nil
	}
	rows := make([]TokenDensityView, 0)
	err := db.GetDB().WithContext(ctx).
		Table("token_densities").
		Select("token_densities.channel_id, channels.name AS channel_name, token_densities.model, token_densities.density, token_densities.sample_count, token_densities.updated_at").
		Joins("LEFT JOIN channels ON channels.id = token_densities.channel_id").
		Order("token_densities.channel_id, token_densities.model").
		Scan(&rows).Error
	return rows, err
}

// TokenDensityDelete 删除密度。
func TokenDensityDelete(ctx context.Context, channelID int, modelName string) error {
	if db.GetDB() == nil {
		return nil
	}
	return db.GetDB().WithContext(ctx).
		Where("channel_id = ? AND model = ?", channelID, modelName).
		Delete(&model.TokenDensity{}).Error
}

// TokenDensityClear 清空密度。
func TokenDensityClear(ctx context.Context) error {
	if db.GetDB() == nil {
		return nil
	}
	return db.GetDB().WithContext(ctx).
		Where("1 = 1").
		Delete(&model.TokenDensity{}).Error
}
