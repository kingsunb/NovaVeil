package model

import "time"

// DBDumpSensitiveNote 写入导出文件头部的敏感信息提示:
// 渠道 Key 与 API Key 是明文，导入后可直接还原调用。代理地址、自定义头值和请求头模板值仍是 "****"。
// auth_jwt_secret、proxy_url、proxy_pool 整行不出现。用户表(含 bcrypt 密码哈希)本来就不参与导出。
const DBDumpSensitiveNote = "PLAINTEXT KEYS: channel keys and API keys in this backup are plaintext and restore upstream calls. Proxy URLs, custom header values and header template values are redacted to \"****\". JWT secret, proxy_url and proxy_pool are omitted. The user table is not included. 注意: 此备份含渠道 Key 与 API Key 明文，可直接还原调用；代理地址、自定义头值和请求头模板值已脱敏为 ****；用户表不在导出范围内。请把文件当作生产凭据保管。"

// DBDump is a full-database JSON export format for NovaVeil.
// Import uses incremental semantics (insert new rows, and upsert on tables with natural keys).
//
// 敏感性: Channels.Key/Keys 与 APIKeys.APIKey 在导出时是明文(库内 nv1: 已解开)。
// ChannelProxy、自定义头值和请求头模板值仍脱敏为 "****"。这些精确 "****" 在导入预检中会被拒绝,
// 避免把掩码加密后当成真实凭据落库。auth_jwt_secret、proxy_url、proxy_pool 不出现在备份中。
// users 表不导出, 不含用户密码哈希。
// 实时请求日志(ErrorLog)不导出; 但用量汇总(UsageBuckets)与客户端调用统计
// (ClientStats)作为跨重启的持久数据参与导出, 换平台后趋势图与防滥用审计不丢。
type DBDump struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Note       string    `json:"note,omitempty"` // 导出文件头部的敏感信息提示, 导入侧忽略。

	Channels      []Channel      `json:"channels,omitempty"`       // 渠道数据。
	ChannelModels []ChannelModel `json:"channel_models,omitempty"` // 渠道模型数据。
	Groups        []Group        `json:"groups,omitempty"`         // 分组数据。
	GroupItems    []GroupItem    `json:"group_items,omitempty"`    // 分组成员数据。
	APIKeys       []APIKey       `json:"api_keys,omitempty"`       // API Key 数据。
	Settings      []Setting      `json:"settings,omitempty"`       // 系统设置数据。
	ClientStats   []ClientStat   `json:"client_stats,omitempty"`   // 客户端调用统计(IP 维度)。
	UsageBuckets  []UsageBucket  `json:"usage_buckets,omitempty"`  // 用量汇总(时间桶 × 模型)。
}

type DBImportResult struct {
	// RowsAffected contains the rows affected for each table operation (insert/upsert depending on table).
	RowsAffected map[string]int64 `json:"rows_affected"`
}

// DBImportPreview 描述导入预检(dry-run)结果: 不写入任何数据, 仅报告将新增、跳过、
// 冲突的行及校验问题, 供调用方确认后再执行实际导入。安全默认是 CanImport=false 时拒绝导入。
type DBImportPreview struct {
	Summary        DBImportSummary      `json:"summary"`
	Conflicts      []DBImportConflict   `json:"conflicts,omitempty"`       // 因主键/唯一键已存在将跳过的行。
	InvalidRefs    []DBImportInvalidRef `json:"invalid_refs,omitempty"`    // 引用了不存在或无效目标的行。
	SettingsIssues []string             `json:"settings_issues,omitempty"` // 设置值校验失败(与正常写接口同规则)。
	Cycles         []string             `json:"cycles,omitempty"`          // 分组引用链中检测到的循环。
	CanImport      bool                 `json:"can_import"`                // 全部校验通过时可安全导入。
}

// DBImportSummary 按表汇总导入预检的行数统计。
type DBImportSummary struct {
	New     map[string]int64 `json:"new"`     // 将新增的行数。
	Skipped map[string]int64 `json:"skipped"` // 因主键/唯一键冲突将跳过的行数(DO NOTHING)。
	Updated map[string]int64 `json:"updated"` // 将按自然键更新的行数(仅 settings)。
}

// DBImportConflict 描述一行因主键或唯一键已存在而将被跳过的冲突。
type DBImportConflict struct {
	Table string `json:"table"`
	ID    int    `json:"id"`
	Desc  string `json:"desc"`
}

// DBImportInvalidRef 描述一行引用了不存在或无效的目标。
type DBImportInvalidRef struct {
	Table string `json:"table"`
	ID    int    `json:"id"`
	Desc  string `json:"desc"`
}
