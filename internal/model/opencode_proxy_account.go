package model

import "time"

// OpenCode 免费档渠道账号台账的来源与探测结论。
const (
	// OpencodeAccountSourceGenerated 账号由体检页生成/粘贴录入。
	OpencodeAccountSourceGenerated = "generated"
	// OpencodeAccountSourceTraffic 账号由转发流量学习产生。
	OpencodeAccountSourceTraffic = "traffic"

	// OpencodeAccountOutcomeHealthy 最近一次探测健康(2xx)。
	OpencodeAccountOutcomeHealthy = "healthy"
	// OpencodeAccountOutcomeRejected 最近一次探测被上游拒绝(403/429)。
	OpencodeAccountOutcomeRejected = "rejected"
	// OpencodeAccountOutcomeError 最近一次探测失败(基础设施层, 不代表账号被封)。
	OpencodeAccountOutcomeError = "error"
)

// OpencodeProxyAccount 是 OpenCode 免费档渠道用过的代理账号(出口 IP)。
// 成功与 403/429 冷却是两件事, 被拒账号同样落库; 体检页生成的账号在探测前
// 即以未探测状态落账, 供页面长期展示与筛选。
type OpencodeProxyAccount struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	ChannelID     int       `gorm:"uniqueIndex:idx_oc_proxy_channel_account;not null" json:"channel_id"`
	Account       string    `gorm:"uniqueIndex:idx_oc_proxy_channel_account;size:128;not null" json:"account"`
	LastSuccessAt time.Time `gorm:"not null" json:"last_success_at"`
	CooldownUntil time.Time `json:"cooldown_until"`
	// Source 是账号来源: generated(体检录入)/traffic(转发学习); 空=历史数据, 视同 traffic。
	Source string `json:"source"`
	// LastProbeAt/LastProbeOutcome/LastProbeStatus 是最近一次体检结论; Outcome 为空表示未探测。
	LastProbeAt      time.Time `json:"last_probe_at"`
	LastProbeOutcome string    `json:"last_probe_outcome"`
	LastProbeStatus  int       `json:"last_probe_status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// TableName 固定表名, 避免 GORM 复数化差异。
func (OpencodeProxyAccount) TableName() string { return "opencode_proxy_accounts" }
