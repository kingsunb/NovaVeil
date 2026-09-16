package model

import (
	"encoding/json"
	"time"
)

// SettingKeyErrorRetentionDays 错误日志保留天数的设置键(默认 3, 0=永久保留)。
// 遵循 setting.go 的 KV 键枚举模式; 因本文件为错误日志功能的自包含定义,
// 默认值的落库由 op 层惰性补种(见 op.ErrorLogEnsureRetentionSetting), 不进 DefaultSettings。
const SettingKeyErrorRetentionDays SettingKey = "error_retention_days"

// DefaultErrorRetentionDays 错误日志保留天数默认值。
const DefaultErrorRetentionDays = 3

// SettingKeyErrorRetentionMaxCount 错误日志最大保留条数的设置键(0=不按条数限制)。
const SettingKeyErrorRetentionMaxCount SettingKey = "error_retention_max_count"

// DefaultErrorRetentionMaxCount 是缺失设置时的默认最大条数, 默认 50 条, 受
// ErrorRetentionMaxCountMax 上限约束。已有数据库中显式保存的 0 仍按"不限制"
// 解释, 避免升级时改变现有运维语义。
const DefaultErrorRetentionMaxCount = 50

// ErrorRetentionMaxCountMax 错误日志最大保留条数的硬上限。设置值超过该上限的
// 保存请求由 Validate 拒绝, 防止配置被误配置到无法执行的超大值。
const ErrorRetentionMaxCountMax = 1000

// MaxRequestBodyLogBytes 是错误日志请求体的最大保留字节数。字段注释与实际落库行为保持一致。
// 64KB 与 MaxErrDetailBytes 对齐: 典型 chat 请求体(含系统提示)可完整保留, 超限时走 JSON 感知
// 截断(缩短过长字符串值而非切断 JSON 结构), 保证前端可格式化展示。配合保留策略(默认 ≤50 条/3 天)
// 磁盘占用有界(≤50×64KB≈3.2MB)。
const MaxRequestBodyLogBytes = 64 * 1024

// MaxErrDetailBytes 错误详情的最大保留字节数: 完整错误文本(含上游响应体)超过该上限时按 UTF-8 边界截断。
// 真实上游错误正文几乎都在数 KB 以内, 上限仅用于拦截异常超大载荷, 兼顾慢速磁盘的写入量。
const MaxErrDetailBytes = 64 * 1024

// ErrorLog 终态失败的请求在数据库中的持久化记录:
// 进程内失败环形缓冲仅保留最近 200 条且重启即失, 本表用于跨重启的面板检索与统计。
// idx_error_logs_class_created 服务于按 err_class 的过滤/计数/按类裁剪(去重清扫),
// 避免故障风暴下三条语句各自全表扫描; AutoMigrate 启动时自动补建。
type ErrorLog struct {
	ID              int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	CreatedAt       time.Time `json:"created_at" gorm:"index:idx_error_logs_created_at;index:idx_error_logs_class_created,priority:2"`
	Model           string    `json:"model"`        // 客户端请求的分组名
	ChannelName     string    `json:"channel_name"` // 最后尝试的渠道名
	TargetModel     string    `json:"target_model"` // 最终实际请求的上游模型名
	ClientIP        string    `json:"client_ip"`
	APIKeyName      string    `json:"api_key_name,omitempty"`      // 密钥名称
	APIKeySuffix    string    `json:"api_key_suffix,omitempty"`    // 密钥尾缀(如 "...KD6S")
	ClientFormat    string    `json:"client_format,omitempty"`     // 下游客户端协议: openai_chat / anthropic 等
	UpstreamType    string    `json:"upstream_type,omitempty"`     // 上游渠道协议类型
	RelayMode       string    `json:"relay_mode,omitempty"`        // 转发方式: passthrough / converted
	ProxyAddr       string    `json:"proxy_addr,omitempty"`        // 最终尝试使用的渠道代理完整地址(密码打码, 含 {account} 解析出的别名); 未走渠道代理为空。
	ChannelKeyLabel string    `json:"channel_key_label,omitempty"` // 最终尝试使用的渠道 Key 标签("#序号(备注)"), 多 Key 渠道用于定位具体凭据; 旧式单 Key 为空。
	ErrClass        string    `json:"err_class" gorm:"index:idx_error_logs_class_created,priority:1"`
	ErrBrief        string    `json:"err_brief" gorm:"size:512"`               // ≤256 字节截断
	RequestBody     string    `json:"request_body,omitempty" gorm:"type:text"` // 原始请求体截断(64KB, JSON 感知截断保留合法结构)
	ErrDetail       string    `json:"err_detail,omitempty" gorm:"type:text"`   // 完整错误文本(含上游响应体), ≤64KB 截断; 面板按需展开
	MaskMatches     json.RawMessage `json:"mask_matches,omitempty" gorm:"type:text"` // 脱敏命中明细(JSON 数组 [{label,original,placeholder}]), 仅在保留完整请求体的条目上附带; 旧记录为空天然兼容(文档 07 §3.2). 用 json.RawMessage 以便 API 响应输出为 JSON 数组而非字符串.
}
