package model

import "time"

// ContextProbeRecord 是一次上下文容量探测的历史记录。
// 渠道与模型名做快照, 渠道删除或模型改名后历史仍可读。
type ContextProbeRecord struct {
	ID          int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	ChannelID   int       `json:"channel_id" gorm:"index"`
	ChannelName string    `json:"channel_name" gorm:"type:text"`
	ModelName   string    `json:"model_name" gorm:"size:512;index"`
	Tokens      int       `json:"tokens"`
	Accepted    bool      `json:"accepted"`
	Status      int       `json:"status"`
	Detail      string    `json:"detail" gorm:"type:text"`
	LatencyMS   int64     `json:"latency_ms"`
	CreatedAt   time.Time `json:"created_at" gorm:"index"`
}

// TableName 固定表名, 避免 GORM 复数化差异。
func (ContextProbeRecord) TableName() string { return "context_probe_records" }
