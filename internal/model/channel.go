package model

// 渠道使用的上游服务提供方。
type ChannelProvider string

const (
	ChannelProviderOpenAI          ChannelProvider = "openai"
	ChannelProviderOpenAIResponses ChannelProvider = "openai_responses"
	ChannelProviderAnthropic       ChannelProvider = "anthropic"
	ChannelProviderGemini          ChannelProvider = "gemini"
	ChannelProviderVolcengine      ChannelProvider = "volcengine"
	// ChannelProviderCustom 自定义固定回复渠道: 不对接任何上游, 命中即以渠道上
	// 配置的 FixedReply 文案合成响应; 经固定回复通道的转换管线天然支持全部客户端协议。
	ChannelProviderCustom ChannelProvider = "custom"
)

// 渠道模型的来源类型。
type ChannelModelSource string

const (
	ChannelModelSourceAuto   ChannelModelSource = "auto"   // 通过上游接口自动获取。
	ChannelModelSourceManual ChannelModelSource = "manual" // 管理员手动配置。
)

// ChannelKey 单渠道多 Key 模式下的单个上游凭据。
type ChannelKey struct {
	ID        string `json:"id"`                            // 稳定标识: 后端生成 sha256(Key值) 前 8 位 hex; 前端可不传由后端补齐。
	Key       string `json:"key,omitempty"`                 // 密钥明文。管理列表默认清空，仅创建/显式导出返回。
	KeyMasked string `json:"key_masked,omitempty" gorm:"-"` // 展示用固定掩码，不参与持久化与转发。
	Remark    string `json:"remark,omitempty"`              // 备注: 纯展示/管理用途。
}

// PrimaryKey 返回辅助路径(测试/模型同步/合成探测)实际应使用的凭据:
// 配置了多 Key 时取第一把, 否则回退旧单 Key 字段。
func (c Channel) PrimaryKey() string {
	if len(c.Keys) > 0 && c.Keys[0].Key != "" {
		return c.Keys[0].Key
	}
	return c.Key
}

// 单个上游渠道的连接和转发配置。
type Channel struct {
	ID                     int                          `json:"id" gorm:"primaryKey"`                                           // 渠道主键。
	Name                   string                       `json:"name" gorm:"unique;not null"`                                    // 渠道名称。
	Type                   ChannelProvider              `json:"type"`                                                           // 上游服务提供方。
	Enabled                bool                         `json:"enabled"`                                                        // 渠道是否可用。注意不可加 default 标签, 否则插入 false 会被数据库默认值覆盖为 true。
	IsFree                 bool                         `json:"is_free" gorm:"not null;default:false"`                          // 免费渠道分类: 内置免费渠道恒为 true, 自定义/用户渠道为 false; 免费渠道在故障转移中优先选择。
	Builtin                bool                         `json:"builtin" gorm:"not null;default:false"`                          // 是否内置固定渠道: 由代码维护, 启动时自动补建; 用户渠道恒为 false.
	BaseURL                string                       `json:"base_url"`                                                       // 唯一的上游基础地址。
	Key                    string                       `json:"key,omitempty"`                                                  // 旧式单一上游访问凭据; 管理列表默认清空。
	KeyMasked              string                       `json:"key_masked,omitempty" gorm:"-"`                                  // 旧式单 Key 的展示掩码, 不持久化。
	Keys                   []ChannelKey                 `json:"keys" gorm:"serializer:json"`                                    // 多 Key 集合; 显式输出 [] 让前端可预测。
	Models                 []ChannelModel               `json:"models" gorm:"foreignKey:ChannelID;constraint:OnDelete:CASCADE"` // 渠道提供的模型; 显式输出 [] 让前端可预测。
	FixedReply             string                       `json:"fixed_reply"`                                                    // 自定义固定回复文案: 仅 type=custom 渠道生效, 命中即以该文案合成响应。
	Proxy                  bool                         `json:"proxy" gorm:"default:false"`                                     // 是否使用代理。
	AutoSync               bool                         `json:"auto_sync" gorm:"default:false"`                                 // 是否自动同步模型。
	OpencodeCompat         bool                         `json:"opencode_compat" gorm:"not null;default:false"`                  // 是否注入 opencode 兼容请求头(x-opencode-session, 会话级稳定 UUID)。
	CustomHeader           []CustomHeader               `json:"custom_header" gorm:"serializer:json"`                           // 追加到上游请求的 Header。
	ParamOverride          *string                      `json:"param_override"`                                                 // 请求参数覆盖配置。
	ChannelProxy           *string                      `json:"channel_proxy"`                                                  // 渠道专用代理地址。
	MatchRegex             *string                      `json:"match_regex"`                                                    // 模型同步过滤表达式。
	ModelLimits            map[string]ChannelModelLimit `json:"model_limits" gorm:"serializer:json"`                            // 按模型名配置的限制, 仅对渠道内同名模型生效。
	Tags                   []string                     `json:"tags" gorm:"serializer:json"`                                    // 渠道自由标签集合, 如 ["free","稳定"]; 显式输出 [] 让前端可预测（避免 omitempty 吞掉后 channel.tags === undefined 触发 .map 崩溃）。
	Sort                   int                          `json:"sort" gorm:"default:0"`                                          // 列表优先级, 越大越靠前。
	RateLimitRPM           int                          `json:"rate_limit_rpm,omitempty"`                                       // 单把 Key 每分钟请求上限; 0 表示不限制。
	MaxConcurrent          int                          `json:"max_concurrent,omitempty"`                                       // 渠道整体最大并发请求数; 0 表示不限制。
	PassThroughBodyEnabled bool                         `json:"pass_through_body_enabled" gorm:"not null;default:false"`        // 完全渠道透传: 启用后任意客户端协议均原样透传至上游, 不经协议转换。
}

// ChannelModelLimit 渠道内单个模型的限制配置。
type ChannelModelLimit struct {
	MaxOutput     *int   `json:"max_output,omitempty"`     // 注入上游请求的最大输出 token 数, 未配置表示不限制。
	ThinkingLevel string `json:"thinking_level,omitempty"` // 思考等级: off/minimal/low/medium/high/xhigh/max, 未配置表示不注入。
}

// 渠道提供的单个上游模型。
type ChannelModel struct {
	ID        int                `json:"id" gorm:"primaryKey"`                                           // 渠道模型主键。
	ChannelID int                `json:"channel_id" gorm:"not null;index:idx_channel_model_name,unique"` // 所属渠道 ID。
	Name      string             `json:"name" gorm:"not null;index:idx_channel_model_name,unique"`       // 上游模型名称。
	Source    ChannelModelSource `json:"source" gorm:"not null;default:auto"`                            // 模型来源。
}

// 追加到上游请求的单个 Header。
type CustomHeader struct {
	HeaderKey   string `json:"header_key"`   // Header 名称。
	HeaderValue string `json:"header_value"` // Header 值。
}

// ChannelUpdateRequest 渠道更新请求 - 仅包含变更的数据。
type ChannelUpdateRequest struct {
	ID                     int                           `json:"id" binding:"required"`               // 待更新渠道的主键。
	Name                   *string                       `json:"name,omitempty"`                      // 新的渠道名称。
	Type                   *ChannelProvider              `json:"type,omitempty"`                      // 新的上游服务提供方。
	Enabled                *bool                         `json:"enabled,omitempty"`                   // 新的启用状态。
	BaseURL                *string                       `json:"base_url,omitempty"`                  // 新的上游基础地址。
	Key                    *string                       `json:"key,omitempty"`                       // 新的上游访问凭据。
	Keys                   *[]ChannelKey                 `json:"keys,omitempty"`                      // 新的多 Key 集合, 整体替换; 空数组表示清除并回退旧 Key 字段。
	Models                 *[]ChannelModel               `json:"models,omitempty"`                    // 新的渠道模型集合。
	FixedReply             *string                       `json:"fixed_reply,omitempty"`               // 新的固定回复文案, 仅 type=custom 渠道。
	Proxy                  *bool                         `json:"proxy,omitempty"`                     // 新的代理开关。
	AutoSync               *bool                         `json:"auto_sync,omitempty"`                 // 新的自动同步开关。
	CustomHeader           *[]CustomHeader               `json:"custom_header,omitempty"`             // 新的自定义 Header。
	ChannelProxy           *string                       `json:"channel_proxy,omitempty"`             // 新的渠道代理地址。
	ParamOverride          *string                       `json:"param_override,omitempty"`            // 新的参数覆盖配置。
	MatchRegex             *string                       `json:"match_regex,omitempty"`               // 新的模型过滤表达式。
	ModelLimits            *map[string]ChannelModelLimit `json:"model_limits,omitempty"`              // 新的按模型限制配置, 整体替换。
	Tags                   *[]string                     `json:"tags,omitempty"`                      // 新的标签集合, 整体替换; nil 表示不修改。
	Sort                   *int                          `json:"sort,omitempty"`                      // 新的列表优先级, 越大越靠前。
	RateLimitRPM           *int                          `json:"rate_limit_rpm,omitempty"`            // 新的单把 Key 每分钟请求上限; nil 表示不修改, 负值归零。
	MaxConcurrent          *int                          `json:"max_concurrent,omitempty"`            // 新的渠道整体最大并发请求数; nil 表示不修改, 负值归零。
	PassThroughBodyEnabled *bool                         `json:"pass_through_body_enabled,omitempty"` // 新的完全渠道透传开关; nil 表示不修改。
}
