package model

import "time"

// OpencodeProxyAccount 是 OpenCode 免费档渠道用过的代理账号(出口 IP)。
// 成功与 403/429 冷却是两件事, 被拒账号同样落库。
type OpencodeProxyAccount struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	ChannelID     int       `gorm:"uniqueIndex:idx_oc_proxy_channel_account;not null" json:"channel_id"`
	Account       string    `gorm:"uniqueIndex:idx_oc_proxy_channel_account;size:128;not null" json:"account"`
	LastSuccessAt time.Time `gorm:"not null" json:"last_success_at"`
	CooldownUntil time.Time `json:"cooldown_until"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 固定表名, 避免 GORM 复数化差异。
func (OpencodeProxyAccount) TableName() string { return "opencode_proxy_accounts" }
