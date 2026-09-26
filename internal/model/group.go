package model

// 分组选择上游成员的模式。
type GroupMode string

const (
	GroupModeManual   GroupMode = "manual"   // 只使用人工选中的成员。
	GroupModeFailover GroupMode = "failover" // 按成员排序选择并在失败时切换。
)

// 分组 Relay 的持久化配置，数据库中以 JSON 存储。
type GroupRelayConfig struct {
	MemberMaxAttempts                     int     `json:"member_max_attempts" binding:"omitempty,min=1"`                        // 单个成员包含首次请求的总尝试次数，仅在故障转移模式生效。
	MemberInfraMaxRetries                 int     `json:"member_infra_max_retries" binding:"omitempty,min=0"`                   // 单个成员连续发生基础设施层错误(SOCKS/DNS/TLS/连接重置)的最大重试次数,达到后走正常冷却通道;0 表示与 MemberMaxAttempts 相同。
	MemberRetryIntervalSeconds            float64 `json:"member_retry_interval_seconds" binding:"omitempty,min=0"`              // 同一成员相邻两次尝试之间的等待秒数, 支持小数, 0 表示不等待直接重试。
	MemberNonStreamResponseTimeoutSeconds int     `json:"member_non_stream_response_timeout_seconds" binding:"omitempty,min=1"` // 单个成员返回完整非流式响应的超时秒数。
	MemberStreamFirstEventTimeoutSeconds  int     `json:"member_stream_first_event_timeout_seconds" binding:"omitempty,min=1"`  // 单个成员返回首个有效流事件的超时秒数。
	MemberStreamIdleTimeoutSeconds        int     `json:"member_stream_idle_timeout_seconds" binding:"omitempty,min=0"`         // 流式转发期相邻事件间的空闲超时秒数, 超时终止流并按失败定稿; 0 表示不限时。
	MemberStreamMaxBytes                  int     `json:"member_stream_max_bytes" binding:"omitempty,min=0"`                    // 单次流式转发累计事件字节数上限, 超过按失败定稿; 0 表示不限。
	MemberStreamMaxEvents                 int     `json:"member_stream_max_events" binding:"omitempty,min=0"`                   // 单次流式转发累计事件数上限, 超过按失败定稿; 0 表示不限。
	MemberCooldownSeconds                 int     `json:"member_cooldown_seconds" binding:"omitempty,min=1"`                    // 单个成员耗尽尝试后被跳过的秒数，仅在故障转移模式生效。
	MemberAffinitySeconds                 int     `json:"member_affinity_seconds" binding:"omitempty,min=0"`                    // 成员亲和时间:故障切换成功后继续保持当前成员的秒数;当前成员失败会立即结束亲和,0 表示不保持。
	MaxRequestRounds                      int     `json:"max_request_rounds" binding:"omitempty,min=1"`                         // 单个请求允许消耗的最大尝试轮次(含引用链结构性跳过),超过后请求以失败收尾,防止异常配置把请求钉成无限循环。
	MaxRequestSeconds                     int     `json:"max_request_seconds" binding:"omitempty,min=0"`                        // 单个请求的整体时长上限秒数,超过后不再发起新一轮尝试;0 表示不限时。

	SessionStickyEnabled           bool    `json:"session_sticky_enabled"`                                      // 是否启用会话粘合:同一会话的请求在粘合有效期内固定使用同一成员。
	SessionStickySeconds           int     `json:"session_sticky_seconds" binding:"omitempty,min=1"`            // 会话粘合时长秒数,粘合成员每次业务成功后滑动续期。
	CooldownBackoffMultiplier      float64 `json:"cooldown_backoff_multiplier" binding:"omitempty,min=1"`       // 半开探测失败后的冷却时间倍数,冷却等级每升一级乘一次,最小为 1 表示不退避。
	CooldownMaxSeconds             int     `json:"cooldown_max_seconds" binding:"omitempty,min=1"`              // 成员冷却时间上限秒数,退避后不超过该值。
	AllCooldownRetryBaseSeconds    int     `json:"all_cooldown_retry_base_seconds" binding:"omitempty,min=0"`   // 全部成员冷却中时自动清除冷却并依次重试的基础间隔秒数,每轮线性递增直至上限;0 表示不自动清除。
	AllCooldownRetryMaxSeconds     int     `json:"all_cooldown_retry_max_seconds" binding:"omitempty,min=1"`    // 全冷却自动重试的间隔上限秒数。
	BackgroundProbeEnabled         bool    `json:"background_probe_enabled"`                                    // 是否启用后台定时探测,对处于 OPEN 状态的成员周期性发起半开测试。
	BackgroundProbeIntervalSeconds int     `json:"background_probe_interval_seconds" binding:"omitempty,min=1"` // 后台定时探测的执行间隔秒数。
	EmergencyItemID                int     `json:"emergency_item_id" binding:"omitempty,min=0"`                 // 紧急兜底成员 ID:全部常规成员不可用时的最后放行目标,须指向同分组已有成员,0 表示关闭。
	PreferPassthrough              bool    `json:"prefer_passthrough"`                                          // 故障转移时优先选择与客户端协议相同的渠道直接透传, 默认关闭。
	MaskEnabled                    bool    `json:"mask_enabled"`                                                // 是否对本分组启用请求脱敏, 默认 false; 须同时全局 Enabled=true 才生效, 旧分组 JSON 反序列化自动得关, 无需数据迁移。
	AutoMatchModels                bool    `json:"auto_match_models"`                                           // 是否启用自动匹配: 以分组名称为关键词, 自动将所有名称包含该关键词的渠道模型加入分组成员, 默认关闭, 旧分组 JSON 反序列化自动得关, 无需数据迁移。
}

// DefaultGroupRelayConfig 返回新分组使用的 Relay 默认配置。
func DefaultGroupRelayConfig() GroupRelayConfig {
	return GroupRelayConfig{
		MemberMaxAttempts:                     3,
		MemberInfraMaxRetries:                 3,
		MemberRetryIntervalSeconds:            2,
		MemberNonStreamResponseTimeoutSeconds: 1200,
		MemberStreamFirstEventTimeoutSeconds:  60,
		MemberStreamIdleTimeoutSeconds:        120,
		MemberStreamMaxBytes:                  0,
		MemberStreamMaxEvents:                 0,
		MemberCooldownSeconds:                 60,
		MemberAffinitySeconds:                 300,
		MaxRequestRounds:                      600,
		MaxRequestSeconds:                     0,

		SessionStickyEnabled:           true,
		SessionStickySeconds:           300,
		CooldownBackoffMultiplier:      2,
		CooldownMaxSeconds:             1800,
		AllCooldownRetryBaseSeconds:    3,
		AllCooldownRetryMaxSeconds:     60,
		BackgroundProbeEnabled:         false,
		BackgroundProbeIntervalSeconds: 60,
		EmergencyItemID:                0,
		PreferPassthrough:              false,
	}
}

// NormalizeGroupRelayConfig 补齐分组 Relay 配置中的空值。
func NormalizeGroupRelayConfig(config *GroupRelayConfig) {
	defaults := DefaultGroupRelayConfig()
	if *config == (GroupRelayConfig{}) {
		*config = defaults
		return
	}
	if config.MemberMaxAttempts < 1 {
		config.MemberMaxAttempts = defaults.MemberMaxAttempts
	}
	// 0 是"跟随 MemberMaxAttempts"语义, 负数回填默认; 显式设的非零正数保留。
	if config.MemberInfraMaxRetries < 0 {
		config.MemberInfraMaxRetries = defaults.MemberInfraMaxRetries
	}
	// 0 是合法的"立即重试"取值, 只把负值回填默认。
	if config.MemberRetryIntervalSeconds < 0 {
		config.MemberRetryIntervalSeconds = defaults.MemberRetryIntervalSeconds
	}
	if config.MemberNonStreamResponseTimeoutSeconds < 1 {
		config.MemberNonStreamResponseTimeoutSeconds = defaults.MemberNonStreamResponseTimeoutSeconds
	}
	if config.MemberStreamFirstEventTimeoutSeconds < 1 {
		config.MemberStreamFirstEventTimeoutSeconds = defaults.MemberStreamFirstEventTimeoutSeconds
	}
	// 流空闲超时 0 是合法的"不限时"取值, 只把负值钳回默认, 不覆盖显式关闭。
	if config.MemberStreamIdleTimeoutSeconds < 0 {
		config.MemberStreamIdleTimeoutSeconds = defaults.MemberStreamIdleTimeoutSeconds
	}
	// 累计字节/事件预算 0 是合法的"不限"取值, 只把负值钳回默认。
	if config.MemberStreamMaxBytes < 0 {
		config.MemberStreamMaxBytes = defaults.MemberStreamMaxBytes
	}
	if config.MemberStreamMaxEvents < 0 {
		config.MemberStreamMaxEvents = defaults.MemberStreamMaxEvents
	}
	if config.MemberCooldownSeconds < 1 {
		config.MemberCooldownSeconds = defaults.MemberCooldownSeconds
	}
	if config.MemberAffinitySeconds < 0 {
		config.MemberAffinitySeconds = defaults.MemberAffinitySeconds
	}
	if config.MaxRequestRounds < 1 {
		config.MaxRequestRounds = defaults.MaxRequestRounds
	}
	// 整体时长上限 0 是合法的"不限时"取值, 只把负值钳回默认, 不覆盖显式关闭。
	if config.MaxRequestSeconds < 0 {
		config.MaxRequestSeconds = defaults.MaxRequestSeconds
	}
	if config.SessionStickySeconds < 1 {
		config.SessionStickySeconds = defaults.SessionStickySeconds
	}
	if config.CooldownBackoffMultiplier < 1 {
		config.CooldownBackoffMultiplier = defaults.CooldownBackoffMultiplier
	}
	if config.CooldownMaxSeconds < 1 {
		config.CooldownMaxSeconds = defaults.CooldownMaxSeconds
	}
	// 全冷却自动重试: 0 是合法的"关闭"取值, 只把负值钳回默认; 上限同理只钳非法值。
	if config.AllCooldownRetryBaseSeconds < 0 {
		config.AllCooldownRetryBaseSeconds = defaults.AllCooldownRetryBaseSeconds
	}
	if config.AllCooldownRetryMaxSeconds < 1 {
		config.AllCooldownRetryMaxSeconds = defaults.AllCooldownRetryMaxSeconds
	}
	if config.BackgroundProbeIntervalSeconds < 1 {
		config.BackgroundProbeIntervalSeconds = defaults.BackgroundProbeIntervalSeconds
	}
	// 紧急兜底默认关闭, 只归零非法负值, 不回填默认成员。
	if config.EmergencyItemID < 0 {
		config.EmergencyItemID = 0
	}
}

// 客户端模型名称及其可手动选择或故障转移的上游分组。
type Group struct {
	ID           int              `json:"id" gorm:"primaryKey"`                                                            // 分组主键。
	Name         string           `json:"name" gorm:"unique;not null"`                                                     // 客户端请求使用的模型名称。
	Mode         GroupMode        `json:"mode" gorm:"not null;default:failover" binding:"omitempty,oneof=manual failover"` // 选择成员的模式。
	ActiveItemID int              `json:"active_item_id" gorm:"not null;default:0"`                                        // 手动模式指定的成员，故障转移模式忽略该值，0 表示未指定。
	DisplayOrder int              `json:"display_order" gorm:"not null;default:0"`                                         // 管理台列表的自定义展示顺序；全部为 0 时列表回退按名称排序。
	RelayConfig  GroupRelayConfig `json:"relay_config" gorm:"serializer:json"`                                             // 该分组的 Relay 路由配置。
	Items        []GroupItem      `json:"items" gorm:"foreignKey:GroupID;constraint:OnDelete:CASCADE"`                     // 该分组可手动选择或故障转移的分组项; 空集合也显式输出 []。
}

// MaxGroupRefDepth 分组引用链允许的最大深度(链上分组数上限),
// 创建/更新的校验与转发时的运行期解析共用同一上限, 防止配置出超长链或环。
const MaxGroupRefDepth = 8

// 分组内一个可选择的成员, 双态二选一:
// 渠道成员: ChannelModelID 指向 channel_models 行, 经关联取得渠道与模型名;
// 引用成员: ChannelModelID 为 0 且 RefGroupName 存放被引用分组的名称,
// 客户端请求本分组时按优先级沿引用解析到目标分组内实际承载的成员与渠道, 实现跨分组故障转移。
type GroupItem struct {
	ID             int    `json:"id" gorm:"primaryKey"`                                                     // 分组项主键。
	GroupID        int    `json:"group_id" gorm:"not null;index:idx_group_channel_model"`                   // 所属分组 ID。引用成员的渠道模型 ID 为 0 会撞唯一索引, 故只建普通索引, 防重复由 op 层校验保证。
	ChannelModelID int    `json:"channel_model_id" gorm:"not null;default:0;index:idx_group_channel_model"` // 引用的渠道模型 ID, 0 表示引用成员。
	RefGroupName   string `json:"ref_group_name,omitempty"`                                                 // 仅引用成员使用: 被引用分组的名称。
	// 关联对象不参与持久化: 引用成员的渠道模型 ID 为 0, 若声明为关联会让 GORM 建出
	// channel_model_id -> channel_models(id) 外键并拒绝 0 值, 级联删除也由 op 层应用逻辑承担。
	ChannelModel *ChannelModel `json:"channel_model,omitempty" gorm:"-:all"` // 分组项引用的渠道模型; 仅在读取快照(op.groupSnapshot)与转发二跳取值时填充, 引用成员为 nil。
	Priority     int           `json:"priority" gorm:"not null"`             // Priority 决定界面展示和故障转移模式下的成员切换顺序。
}

// IsGroupRef 报告该成员是否为引用其他分组的引用成员:
// 渠道模型 ID 为 0 且携带被引用分组名即引用语义。
func (i GroupItem) IsGroupRef() bool { return i.ChannelModelID == 0 && i.RefGroupName != "" }

// 分组普通配置和成员变更请求。
type GroupUpdateRequest struct {
	ID            int                      `json:"id" binding:"required"`                                    // 待更新的分组主键。
	Name          *string                  `json:"name,omitempty"`                                           // Name 仅在名称变更时发送。
	Mode          *GroupMode               `json:"mode,omitempty" binding:"omitempty,oneof=manual failover"` // Mode 仅在选择模式变更时发送。
	DisplayOrder  *int                     `json:"display_order,omitempty"`                                  // DisplayOrder 仅在自定义展示顺序变更时发送；允许负值重排，前端负责归一化。
	RelayConfig   *GroupRelayConfig        `json:"relay_config,omitempty"`                                   // RelayConfig 仅在 Relay 配置变更时发送完整配置。
	ItemsToAdd    []GroupItemAddRequest    `json:"items_to_add,omitempty"`                                   // 待新增的分组项。
	ItemsToUpdate []GroupItemUpdateRequest `json:"items_to_update,omitempty"`                                // 待调整展示和故障转移顺序的分组项。
	ItemsToDelete []int                    `json:"items_to_delete,omitempty"`                                // 待删除的分组项 ID。
}

// 手动模式下切换或清空分组当前分组项的请求。
type GroupActiveItemUpdateRequest struct {
	ItemID *int `json:"item_id"` // 待设为当前分组项的 ID，空值或 0 表示取消选择。
}

// 新增分组项请求, 双态二选一: 渠道成员携带 channel_model_id;
// 引用成员 channel_model_id 为 0 且 ref_group_name 存放被引用分组名。
type GroupItemAddRequest struct {
	ChannelModelID int    `json:"channel_model_id"`   // 待引用的渠道模型 ID，0 表示引用成员。
	RefGroupName   string `json:"ref_group_name"`     // 引用成员的被引用分组名。
	Priority       int    `json:"priority,omitempty"` // 分组项的界面展示和故障转移顺序。
}

// 分组项展示和故障转移顺序更新请求。
type GroupItemUpdateRequest struct {
	ID       int `json:"id" binding:"required"` // 待更新的分组项主键。
	Priority int `json:"priority,omitempty"`    // 新的界面展示和故障转移顺序。
}
