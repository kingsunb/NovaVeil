package model

import "time"

// TokenDensity 渠道模型密度观测。
type TokenDensity struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	ChannelID    int       `json:"channel_id" gorm:"uniqueIndex:idx_token_density_channel_model;not null"`
	Model        string    `json:"model" gorm:"uniqueIndex:idx_token_density_channel_model;size:191;not null"`
	Density      float64   `json:"density" gorm:"not null"`
	SampleCount  int64     `json:"sample_count" gorm:"not null;default:0"`
	DailySamples int       `json:"daily_samples" gorm:"not null;default:0"`
	SampleDay    string    `json:"sample_day" gorm:"size:10;not null;default:''"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 固定表名。
func (TokenDensity) TableName() string {
	return "token_densities"
}
