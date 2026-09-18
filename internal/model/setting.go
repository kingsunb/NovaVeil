package model

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
)

type SettingKey string

const (
	SettingKeyProxyURL                  SettingKey = "proxy_url"                   // 全局出站代理地址
	SettingKeySyncLLMInterval           SettingKey = "sync_llm_interval"           // LLM 同步间隔(小时)
	SettingKeyCORSAllowOrigins          SettingKey = "cors_allow_origins"          // 跨域白名单(逗号分隔的精确 origin, 如 "https://example.com"). 为空不允许跨域, 禁止 "*" 与裸域名
	SettingKeyConversationLog           SettingKey = "conversation_log_enabled"    // 对话留存开关: "1"记录全部终态对话到 data/conversations 供本地审计使用, "0"(默认)关闭
	SettingKeyConversationRetentionDays SettingKey = "conversation_retention_days" // 对话留存归档保留天数(天), 默认 3, 受 ConversationRetentionDaysMin/Max 校验
	SettingKeyConversationDirMaxGB      SettingKey = "conversation_dir_max_gb"     // 对话留存目录硬预算(GB), 默认 5, 受 ConversationDirMaxGBMin/Max 校验
	SettingKeyAuthJWTSecret             SettingKey = "auth_jwt_secret"             // JWT 签名密钥(32字节随机数的hex, 64字符). 为空时首次使用自动生成并持久化; 轮换后所有已签发 token 失效. 仅内部管理, 禁止通过设置接口读写
	SettingKeyHeaderTemplates           SettingKey = "header_templates"            // 渠道自定义请求头模板(JSON 数组), 供渠道表单一键填充
	SettingKeyChannelRandomHeaders      SettingKey = "channel_random_headers"      // 按渠道指向头名的随机请求头规则(JSON 数组), 值由系统按会话自动生成并注入
	SettingKeyUsageRetentionDays        SettingKey = "usage_retention_days"        // 用量分桶保留天数(天); 0=永久保留, 否则必须 >= 365(至少覆盖 1 年)
	SettingKeyMaskConfig                SettingKey = "mask_config"                 // 脱敏功能配置(JSON): 全局开关/内置规则开关/自定义敏感词, 默认全关
	SettingKeyModelFilter               SettingKey = "model_filter"                // 渠道获取模型时的全局过滤表达式(ECMAScript 正则); 留空表示不过滤, 与渠道级 MatchRegex 取 AND
	SettingKeyClientStatMaxCount        SettingKey = "client_stat_max_count"       // 调用客户端统计最大保留条数, 默认 10000, 0=不限制
	SettingKeyProxyPool                 SettingKey = "proxy_pool"                  // 代理池(JSON 数组), 供设置页管理与测试多个可选代理
	SettingKeyConvTrace                 SettingKey = "conv_trace_enabled"          // 协议转换追踪开关: "1"开启后对跨协议转换记录耗时/大小/降级诊断等 Debug 日志, "0"(默认)关闭, 关闭时仅一次缓存查询零开销
)

// 用量数据保留时间设置项的默认值与下限。
// 0 表示永久保留; 非 0 值受 UsageRetentionDaysMin 约束, 至少保留 1 天。
// 档位: 1 天 / 7 天 / 30 天 / 90 天 / 180 天 / 365 天 / 1095 天 / 0(永久)。
const (
	DefaultUsageRetentionDays = 0 // 默认永久保留, 与历史行为零差异。
	UsageRetentionDaysMin     = 1 // 非 0 保留天数下限: 至少保留 1 天。
)

// 调用客户端统计最大保留条数的默认值与硬上限。
// 0 表示不限制; 非 0 值受 ClientStatMaxCountMin 约束。
const (
	DefaultClientStatMaxCount = 10000  // 默认保留 1 万条 IP 统计
	ClientStatMaxCountMin     = 0      // 0 = 不限制
	ClientStatMaxCountMax     = 100000 // 硬上限, 防止误配置撑爆内存
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

// HeaderTemplate 一键填充渠道自定义 Header 的命名模板。
type HeaderTemplate struct {
	Name    string         `json:"name"`    // 模板名称, 渠道表单中作为入口标题, 需唯一。
	Headers []CustomHeader `json:"headers"` // 模板包含的 Header 集合, 整体覆盖同名 Key。
}

// 请求头模板的防呆上限, 避免一次误配置把设置值撑到不可用。
const (
	MaxHeaderTemplateCount       = 32   // 模板数量上限。
	MaxHeaderTemplateNameLen     = 100  // 模板名称最大长度。
	MaxHeaderTemplateHeaderCount = 64   // 单个模板的 Header 条数上限。
	MaxHeaderTemplateValueLen    = 2048 // 单个 Header 值最大长度。
)

// DefaultHeaderTemplates 出厂内置模板, 目前含 codex 一项; 用户可在设置页增删改。
func DefaultHeaderTemplates() []HeaderTemplate {
	return []HeaderTemplate{
		{
			Name: "codex",
			Headers: []CustomHeader{
				{HeaderKey: "x-codex-beta-features", HeaderValue: "remote_compaction_v2"},
				{HeaderKey: "accept", HeaderValue: "text/event-stream"},
				{HeaderKey: "content-type", HeaderValue: "application/json"},
				{HeaderKey: "originator", HeaderValue: "codex-tui"},
				{HeaderKey: "user-agent", HeaderValue: "codex-tui/0.155.1 (Windows 10.0.26200; x86_64) WindowsTerminal (codex-tui; 0.155.1)"},
			},
		},
	}
}

// defaultHeaderTemplatesJSON 序列化出厂模板; 输入是静态数据, 序列化不会失败。
func defaultHeaderTemplatesJSON() string {
	value, err := json.Marshal(DefaultHeaderTemplates())
	if err != nil {
		return "[]"
	}
	return string(value)
}

// defaultMaskConfigJSON 序列化出厂全关的脱敏配置; 输入是静态数据, 序列化不会失败。
// 落库后即「键存在且全关」, 与「键缺失=关」语义叠加, 保证出厂状态恒为不脱敏。
func defaultMaskConfigJSON() string {
	value, err := json.Marshal(DefaultMaskConfig())
	if err != nil {
		return `{"enabled":false,"builtin_rule_switch":{},"custom_terms":null}`
	}
	return string(value)
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyCORSAllowOrigins, Value: ""},                                                      // CORS 默认不允许跨域, 仅接受显式 scheme+host 的精确 origin
		{Key: SettingKeySyncLLMInterval, Value: "24"},                                                     // 默认24小时同步一次LLM
		{Key: SettingKeyConversationLog, Value: "0"},                                                      // 对话留存默认关闭
		{Key: SettingKeyConversationRetentionDays, Value: strconv.Itoa(DefaultConversationRetentionDays)}, // 归档默认保留 3 天
		{Key: SettingKeyConversationDirMaxGB, Value: strconv.FormatInt(DefaultConversationDirMaxGB, 10)},  // 目录默认预算 5GB
		{Key: SettingKeyAuthJWTSecret, Value: ""},                                                         // JWT 签名密钥默认为空, 首次使用时自动生成并持久化
		{Key: SettingKeyHeaderTemplates, Value: defaultHeaderTemplatesJSON()},                             // 请求头模板默认内置 codex 一项
		{Key: SettingKeyChannelRandomHeaders, Value: "[]"},                                                // 随机请求头规则默认为空, 历史渠道转发行零差异
		{Key: SettingKeyUsageRetentionDays, Value: strconv.Itoa(DefaultUsageRetentionDays)},               // 用量分桶默认永久保留(0)
		{Key: SettingKeyMaskConfig, Value: defaultMaskConfigJSON()},                                       // 脱敏配置默认全关(全局关/规则全关/无自定义词)
		{Key: SettingKeyModelFilter, Value: ""},                                                           // 全局模型过滤默认为空, 表示不过滤
		{Key: SettingKeyClientStatMaxCount, Value: strconv.Itoa(DefaultClientStatMaxCount)},               // 调用客户端统计默认保留 1 万条
		{Key: SettingKeyProxyPool, Value: "[]"},                                                           // 代理池默认为空
		{Key: SettingKeyConvTrace, Value: "0"},                                                            // 协议转换追踪默认关闭
	}
}

func (s *Setting) Validate() error {
	if s.Key == "" {
		return fmt.Errorf("setting key must not be empty")
	}
	switch s.Key {
	case SettingKeyAuthJWTSecret:
		// JWT 密钥仅由内部生成与轮换, 禁止通过设置接口修改
		return fmt.Errorf("jwt secret is managed internally and cannot be modified")
	case SettingKeySyncLLMInterval:
		hours, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("同步 LLM 间隔必须是整数")
		}
		// 0/负值会在 task.Update 中把同步任务整个删除, 且运行期无法恢复(仅启动时注册),
		// 必须在入口拒绝, 避免一次错误配置永久关闭模型同步。
		if hours < 1 {
			return fmt.Errorf("同步 LLM 间隔必须是不小于 1 的整数(小时)")
		}
		return nil
	case SettingKeyConversationLog:
		if _, err := strconv.ParseBool(s.Value); err != nil {
			return fmt.Errorf("conversation log switch must be a boolean")
		}
		return nil
	case SettingKeyConvTrace:
		if _, err := strconv.ParseBool(s.Value); err != nil {
			return fmt.Errorf("conv trace switch must be a boolean")
		}
		return nil
	case SettingKeyConversationRetentionDays:
		days, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("对话留存保留天数必须是不小于 %d 的整数", ConversationRetentionDaysMin)
		}
		if days < ConversationRetentionDaysMin || days > ConversationRetentionDaysMax {
			return fmt.Errorf("对话留存保留天数必须在 %d-%d 之间", ConversationRetentionDaysMin, ConversationRetentionDaysMax)
		}
		return nil
	case SettingKeyConversationDirMaxGB:
		gb, err := strconv.ParseInt(s.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("对话留存目录预算必须是不小于 %d 的整数(GB)", ConversationDirMaxGBMin)
		}
		if gb < ConversationDirMaxGBMin || gb > ConversationDirMaxGBMax {
			return fmt.Errorf("对话留存目录预算必须在 %d-%d GB 之间", ConversationDirMaxGBMin, ConversationDirMaxGBMax)
		}
		return nil
	case SettingKeyCORSAllowOrigins:
		for _, origin := range strings.Split(s.Value, ",") {
			origin = strings.TrimSpace(origin)
			if origin == "" {
				continue
			}
			if origin == "*" {
				return fmt.Errorf("CORS wildcard origin is not allowed with credentials")
			}
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
				return fmt.Errorf("CORS origin must be an exact http(s) origin with scheme and host")
			}
		}
		return nil
	case SettingKeyErrorRetentionDays, SettingKeyErrorRetentionMaxCount:
		n, err := strconv.Atoi(s.Value)
		if err != nil || n < 0 {
			return fmt.Errorf("%s must be a non-negative integer", s.Key)
		}
		// 保留天数 >0 时受 DefaultErrorRetentionDays 语义兜底(默认 3, 上限不限天);
		// 最大条数 0 表示不限制, 大于 0 时受 ErrorRetentionMaxCountMax 硬上限约束。
		if s.Key == SettingKeyErrorRetentionMaxCount && n > 0 && n > ErrorRetentionMaxCountMax {
			return fmt.Errorf("error_retention_max_count must not exceed %d", ErrorRetentionMaxCountMax)
		}
		return nil
	case SettingKeyHeaderTemplates:
		return validateHeaderTemplates(s.Value)
	case SettingKeyChannelRandomHeaders:
		return validateChannelRandomHeaders(s.Value)
	case SettingKeyUsageRetentionDays:
		days, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("用量保留天数必须是整数")
		}
		// 0=永久保留; 非 0 值至少保留 1 天(UsageRetentionDaysMin)。
		if days != 0 && days < UsageRetentionDaysMin {
			return fmt.Errorf("用量保留天数必须为 0(永久) 或不小于 %d", UsageRetentionDaysMin)
		}
		return nil
	case SettingKeyMaskConfig:
		// 脱敏配置以 JSON 存储, 解析后交由 MaskConfig.Validate 校验自定义敏感词的条数/长度/唯一性。
		var cfg MaskConfig
		if err := json.Unmarshal([]byte(s.Value), &cfg); err != nil {
			return fmt.Errorf("脱敏配置必须是合法的 JSON")
		}
		return cfg.Validate()
	case SettingKeyModelFilter:
		// 留空表示不过滤; 非空时用 ECMAScript 方言校验, 避免设置能存但拉取时编译失败。
		if s.Value == "" {
			return nil
		}
		if _, err := regexp2.Compile(s.Value, regexp2.ECMAScript); err != nil {
			return fmt.Errorf("全局模型过滤正则表达式无效: %w", err)
		}
		return nil
	case SettingKeyClientStatMaxCount:
		n, err := strconv.Atoi(s.Value)
		if err != nil || n < 0 {
			return fmt.Errorf("调用客户端最大保留条数必须是非负整数(0=不限制)")
		}
		if n > ClientStatMaxCountMax {
			return fmt.Errorf("调用客户端最大保留条数不能超过 %d", ClientStatMaxCountMax)
		}
		return nil
	case SettingKeyProxyPool:
		return validateProxyPool(s.Value)
	case SettingKeyProxyURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("proxy URL is invalid: %w", err)
		}
		if !validProxySchemes[parsedURL.Scheme] {
			return fmt.Errorf("proxy URL scheme must be http, https, socks, socks5, or socks5h")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("proxy URL must have a host")
		}
		return nil
	}

	return nil
}

// validateHeaderTemplates 校验请求头模板 JSON: 允许空值(清空全部), 其余必须是
// 合法且不超限的模板数组, 名称唯一非空, 每个 Header 的 Key 非空。
func validateHeaderTemplates(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var templates []HeaderTemplate
	if err := json.Unmarshal([]byte(value), &templates); err != nil {
		return fmt.Errorf("请求头模板必须是合法的 JSON 数组")
	}
	if len(templates) > MaxHeaderTemplateCount {
		return fmt.Errorf("请求头模板数量不能超过 %d 个", MaxHeaderTemplateCount)
	}
	seen := make(map[string]bool, len(templates))
	for _, tpl := range templates {
		name := strings.TrimSpace(tpl.Name)
		if name == "" {
			return fmt.Errorf("请求头模板名称不能为空")
		}
		if len(name) > MaxHeaderTemplateNameLen {
			return fmt.Errorf("请求头模板名称过长(最多 %d 字符)", MaxHeaderTemplateNameLen)
		}
		if seen[name] {
			return fmt.Errorf("请求头模板名称重复: %s", name)
		}
		seen[name] = true
		if len(tpl.Headers) == 0 {
			return fmt.Errorf("请求头模板 %s 至少需要一条 Header", name)
		}
		if len(tpl.Headers) > MaxHeaderTemplateHeaderCount {
			return fmt.Errorf("请求头模板 %s 的 Header 数量不能超过 %d 条", name, MaxHeaderTemplateHeaderCount)
		}
		for _, header := range tpl.Headers {
			key := strings.TrimSpace(header.HeaderKey)
			if key == "" {
				return fmt.Errorf("请求头模板 %s 存在空的 Header 名称", name)
			}
			// 直接校验未 trim 的原值: 带首尾空白的头名传输层会按非法名静默丢弃,
			// 存储值与校验值必须一致, 否则出现保存成功却从不生效的缝隙。
			if key != header.HeaderKey || !isValidHeaderFieldName(key) {
				return fmt.Errorf("请求头模板 %s 的 Header 名称包含非法字符(仅允许字母、数字与 !#$%%&'*+-.^_`|~ 等标头字符)", name)
			}
			if len(header.HeaderValue) > MaxHeaderTemplateValueLen {
				return fmt.Errorf("请求头模板 %s 的 Header 值过长(最多 %d 字符)", name, MaxHeaderTemplateValueLen)
			}
		}
	}
	return nil
}

// isValidHeaderFieldName 按 RFC 9110 token 字符集校验标头名: 非法字符(空格、冒号、
// 控制字符等)在传输层会被丢弃或导致请求失败, 提前在配置入口拒绝,
// 避免模板保存成功却从不生效的静默失效。
func isValidHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '!' || c == '#' || c == '$' || c == '%' || c == '&' || c == '\'' || c == '*' ||
			c == '+' || c == '-' || c == '.' || c == '^' || c == '_' || c == '`' || c == '|' || c == '~':
		default:
			return false
		}
	}
	return true
}

// validProxySchemes 代理地址允许的协议集, 与 client.newHTTPClientCustomProxy 保持一致。
var validProxySchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks":   true,
	"socks5":  true,
	"socks5h": true,
}

// validateProxyPool 校验代理池 JSON: 允许空数组(含空字符串), 其余必须是合法且不超限的代理条目数组。
func validateProxyPool(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var entries []ProxyEntry
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		return fmt.Errorf("代理池必须是合法的 JSON 数组")
	}
	if len(entries) > MaxProxyPoolCount {
		return fmt.Errorf("代理池条目数量不能超过 %d 个", MaxProxyPoolCount)
	}
	seenIDs := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			return fmt.Errorf("代理条目 ID 不能为空")
		}
		if seenIDs[entry.ID] {
			return fmt.Errorf("代理条目 ID 重复: %s", entry.ID)
		}
		seenIDs[entry.ID] = true
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return fmt.Errorf("代理条目名称不能为空")
		}
		if len(name) > MaxProxyNameLen {
			return fmt.Errorf("代理条目名称过长(最多 %d 字符)", MaxProxyNameLen)
		}
		if entry.URL == "" {
			return fmt.Errorf("代理条目 %s 的地址不能为空", name)
		}
		if len(entry.URL) > MaxProxyURLLen {
			return fmt.Errorf("代理条目 %s 的地址过长(最多 %d 字符)", name, MaxProxyURLLen)
		}
		// 代理池条目最终作为渠道代理使用, 同样支持 {account} 占位符。{ 与 } 不在 net/url
		// 允许的 userinfo 字符集内, 需先按百分号转义占位符才能通过 url.Parse; 转义方式与
		// helper.ResolveProxyTemplate 一致, 校验只验可解析, 占位符替换仍在转发时完成。
		escaped := strings.ReplaceAll(entry.URL, AccountPlaceholder, AccountPlaceholderEscape)
		parsedURL, err := url.Parse(escaped)
		if err != nil {
			return fmt.Errorf("代理条目 %s 的地址无效: %w", name, err)
		}
		if !validProxySchemes[parsedURL.Scheme] {
			return fmt.Errorf("代理条目 %s 的协议必须为 http/https/socks/socks5/socks5h", name)
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("代理条目 %s 的地址缺少主机", name)
		}
	}
	return nil
}
