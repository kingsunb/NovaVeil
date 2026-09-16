/**
 * 与后端 struct json tag 对齐 —— 见 internal/model/ + internal/relay/state.go
 * 字段名一律走下划线；不要用驼峰。
 */

// ============================================================
// 用户
// ============================================================

export interface UserStatus {
  /** 探活/登录成功后回填 Topbar；旧后端可能缺省，调用方需兜底 null。 */
  username?: string;
  must_change_password: boolean;
}

export interface UserLoginRequest {
  username: string;
  password: string;
  /** cookie 过期秒数；0 = 默认 */
  expire: number;
}

// ============================================================
// 渠道
// ============================================================

export type ChannelProvider =
  | "openai"
  | "openai_responses"
  | "anthropic"
  | "gemini"
  | "volcengine"
  /** 自定义固定回复：命中即以 fixed_reply 文案合成响应，不对接上游 */
  | "custom";

export type ChannelModelSource = "auto" | "manual";

export interface ChannelKey {
  id: string;
  /** 仅前端编辑草稿保存原服务端 ID；不会提交给后端。 */
  original_id?: string;
  /** 明文仅创建/显式导出时返回，列表为空 */
  key?: string;
  /** 展示用掩码，不参与持久化 */
  key_masked?: string;
  /** 备注: 纯展示/管理用途（代理别名由后端按渠道生成，不再占用该字段） */
  remark?: string;
}

export interface ChannelModel {
  id: number;
  channel_id: number;
  name: string;
  source: ChannelModelSource;
}

export interface ChannelModelLimit {
  max_output?: number;
  thinking_level?: string;
}

export interface CustomHeader {
  header_key: string;
  header_value: string;
}

/**
 * 渠道管理快照（list 返回）：
 *  - key 与 keys[].key 始终为空字符串（明文不外泄）
 *  - key_masked / keys[].key_masked 必填（用于 UI 展示）
 *  - models 是已持久化的渠道模型
 */
export interface Channel {
  id: number;
  name: string;
  type: ChannelProvider;
  enabled: boolean;
  base_url: string;
  key: string;
  key_masked?: string;
  keys: ChannelKey[];
  models: ChannelModel[];
  /** 固定回复文案；仅 type=custom 渠道生效 */
  fixed_reply: string;
  proxy: boolean;
  auto_sync: boolean;
  /** 是否注入 opencode 兼容请求头（x-opencode-session，会话级稳定 UUID）。 */
  opencode_compat: boolean;
  custom_header: CustomHeader[];
  param_override?: string;
  channel_proxy?: string;
  match_regex?: string;
  /** key -> limits；后端 JSON 序列化 */
  model_limits: Record<string, ChannelModelLimit>;
  tags: string[];
  sort: number;
  rate_limit_rpm: number;
  max_concurrent: number;
  /** 完全渠道透传: 启用后任意客户端协议均原样透传至上游, 不经协议转换。 */
  pass_through_body_enabled: boolean;
}

export interface ChannelUpdateRequest {
  id: number;
  name?: string;
  type?: ChannelProvider;
  enabled?: boolean;
  base_url?: string;
  key?: string;
  keys?: ChannelKey[];
  models?: ChannelModel[];
  fixed_reply?: string;
  proxy?: boolean;
  auto_sync?: boolean;
  /** opencode 兼容请求头开关；nil 表示不修改。 */
  opencode_compat?: boolean;
  custom_header?: CustomHeader[];
  channel_proxy?: string;
  param_override?: string;
  match_regex?: string;
  model_limits?: Record<string, ChannelModelLimit>;
  tags?: string[];
  sort?: number;
  rate_limit_rpm?: number;
  max_concurrent?: number;
  /** 完全渠道透传开关; nil 表示不修改。 */
  pass_through_body_enabled?: boolean;
}

/** 前端统一的单模型测试结果；后端原始字段 elapsed_ms 会在 api 层归一化为 latency_ms。 */
/**
 * 单模型测试结果（与后端 relay.ChannelTestResult 严格对齐）。
 * 失败不走 200+ok:false，而是 HTTP 5xx + 信封 message，由调用方 catch 处理；
 * latency_ms 由后端 elapsed_ms 归一化而来。
 */
export interface ChannelTestResult {
  model: string;
  content: string;
  latency_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  /** 分组测试时标识分组成员来源（渠道名），单渠道测试为空。 */
  channel_name?: string;
  /** 分组测试时标识分组成员的模型名（可能与 model 不同），单渠道测试为空。 */
  target_model?: string;
  /** 分组测试时的中继方式：passthrough 透传 / converted 转换。 */
  relay_mode?: "passthrough" | "converted";
}

/** 分组测试结果条目（与后端 relay.GroupTestResult 对齐）。 */
export interface GroupTestResult {
  /** 分组成员引用的渠道名。 */
  channel_name: string;
  /** 分组成员引用的上游模型名。 */
  model: string;
  /** 测试状态：pending/ok/fail。 */
  status: "ok" | "fail";
  /** 模型回复文本（成功时）。 */
  content?: string;
  /** 端到端耗时毫秒。 */
  latency_ms: number;
  /** 失败原因。 */
  error?: string;
  /** 中继方式。 */
  relay_mode?: "passthrough" | "converted";
}

/** 渠道导入结果（与后端 channelImportResult 对齐）。 */
export interface ChannelImportResult {
  /** 成功创建的渠道数。 */
  success: number;
  /** 失败的渠道数。 */
  failed: number;
  /** 逐条失败原因（渠道名 + 错误描述）。 */
  errors: string[];
}

/** 逐密钥测试的单 Key 结果（与后端 relay.ChannelKeyTestResult 对齐）。 */
export interface ChannelKeyTestResult {
  /** 密钥稳定标识；旧式单 Key 渠道为空串。 */
  key_id: string;
  /** 面板展示标签："#序号(备注/ID)"。 */
  label: string;
  /** 上游是否成功返回回复。 */
  ok: boolean;
  /** 成功时的回复摘要。 */
  content?: string;
  /** 失败原因（上游错误原文/超时等）。 */
  error?: string;
  /** 端到端耗时毫秒。 */
  elapsed_ms: number;
}

export interface FetchedModel {
  name: string;
  capabilities?: string[];
}

// ============================================================
// 分组
// ============================================================

export type GroupMode = "manual" | "failover";

export interface GroupRelayConfig {
  member_max_attempts: number;
  member_infra_max_retries: number;
  member_retry_interval_seconds: number;
  member_non_stream_response_timeout_seconds: number;
  member_stream_first_event_timeout_seconds: number;
  member_cooldown_seconds: number;
  member_affinity_seconds: number;
  max_request_rounds: number;
  max_request_seconds: number;
  session_sticky_enabled: boolean;
  session_sticky_seconds: number;
  cooldown_backoff_multiplier: number;
  cooldown_max_seconds: number;
  all_cooldown_retry_base_seconds: number;
  all_cooldown_retry_max_seconds: number;
  background_probe_enabled: boolean;
  background_probe_interval_seconds: number;
  emergency_item_id: number;
  prefer_passthrough: boolean;
  /** 是否对本分组启用请求脱敏，默认 false；须同时全局 enabled=true 才生效。 */
  mask_enabled?: boolean;
  /** 是否启用自动匹配：以分组名称为关键词，自动将名称包含该关键词的渠道模型加入分组成员。 */
  auto_match_models: boolean;
}

/**
 * 新分组的 Relay 默认配置，与后端 internal/model/group.go 的
 * DefaultGroupRelayConfig 保持一致（failover-first 调优版：
 * member_retry_interval_seconds=2、member_stream_first_event_timeout_seconds=60）。
 * 后端调整默认值时需同步这里，否则新建分组与后端预期漂移。
 */
export const DEFAULT_GROUP_RELAY_CONFIG: GroupRelayConfig = {
  member_max_attempts: 3,
  member_infra_max_retries: 3,
  member_retry_interval_seconds: 2,
  member_non_stream_response_timeout_seconds: 1200,
  member_stream_first_event_timeout_seconds: 60,
  member_cooldown_seconds: 60,
  member_affinity_seconds: 300,
  max_request_rounds: 600,
  max_request_seconds: 0,
  session_sticky_enabled: true,
  session_sticky_seconds: 300,
  cooldown_backoff_multiplier: 2,
  cooldown_max_seconds: 1800,
  all_cooldown_retry_base_seconds: 3,
  all_cooldown_retry_max_seconds: 60,
  background_probe_enabled: false,
  background_probe_interval_seconds: 60,
  emergency_item_id: 0,
  prefer_passthrough: false,
  auto_match_models: false,
};

export interface GroupItem {
  id: number;
  /** 仅前端编辑草稿使用，不发送给后端。 */
  client_uid?: string;
  group_id: number;
  channel_model_id: number;
  ref_group_name: string;
  channel_model?: ChannelModel;
  priority: number;
}

export interface Group {
  id: number;
  name: string;
  mode: GroupMode;
  active_item_id: number;
  /** 管理台自定义展示顺序；0/缺省表示未自定义，列表回退按名称排序。 */
  display_order?: number;
  relay_config: GroupRelayConfig;
  /** 后端可省略空集合；API 层会归一化为 []。 */
  items?: GroupItem[];
}

export interface GroupItemAddRequest {
  channel_model_id: number;
  ref_group_name: string;
  priority: number;
}

export interface GroupUpdateRequest {
  id: number;
  name?: string;
  mode?: GroupMode;
  /** 自定义展示顺序；仅顺序调整时发送。 */
  display_order?: number;
  relay_config?: GroupRelayConfig;
  items_to_add?: GroupItemAddRequest[];
  items_to_update?: { id: number; priority: number }[];
  items_to_delete?: number[];
}

/**
 * 分组路由运行时快照 —— /api/v1/group/runtime/stream（event: "runtime"），
 * 与后端 relay.RouteState 对齐。时间戳一律 Unix 毫秒；冷却条目到期后由
 * 前端按当前时间忽略（后端不保证推送过期事件）。
 */
export interface GroupRouteState {
  group_id: number;
  /** 当前承载请求的成员 ID，0 = 尚未建立路由 */
  current_item_id: number;
  /** 处于半开探测的候选成员 ID，0 = 无 */
  probe_item_id: number;
  /** 当前路由的亲和截止 Unix 毫秒，0 = 无亲和 */
  affinity_until: number;
  /** 成员 ID → 冷却截止 Unix 毫秒 */
  cooldowns: Record<string, number>;
  /** 成员冷却等级（半开失败逐级退避） */
  levels: Record<string, number>;
  /** 处于 HALF_OPEN 探测的成员 ID → 进入半开的 Unix 毫秒 */
  half_opens: Record<string, number>;
  /** 成员提交后失败的连击计数 */
  post_commit_strikes: Record<string, number>;
  /** 紧急兜底当前承载的成员 ID，0 = 未进入 */
  emergency_item_id: number;
  /** 紧急兜底模式下进行中的请求数 */
  emergency_active: number;
}

// ============================================================
// API 密钥
// ============================================================

/**
 * 列表摘要：APIKey 字段会被后端替换为掩码后写入 api_key_masked
 *  - 列表 / 更新返回 api_key_masked
 *  - 创建返回明文 api_key
 */
export interface APIKeySummary {
  id: number;
  name: string;
  api_key_masked: string;
  enabled: boolean;
  /** unix seconds；0 = 永久。后端 omitempty：永久时字段缺省，消费方必须真值判断 */
  expire_at?: number;
  /** 模型名逗号分隔；空 = 全部。后端 omitempty：全部模型时字段缺省 */
  supported_models?: string;
  /** 密钥级最大并发；0 = 不限。超限 fail-fast 429 + Retry-After */
  max_concurrent: number;
  /** 密钥级每分钟请求数上限；0 = 不限 */
  rate_limit_rpm: number;
  /** 创建时间 unix 秒；后端 omitempty */
  created_at?: number;
  /** 最后使用时间 unix 秒；0/缺省 = 从未使用 */
  last_used_at?: number;
}

export interface APIKeyCreated {
  id: number;
  name: string;
  /** 创建时一次性返回的明文 */
  api_key: string;
  enabled: boolean;
  expire_at?: number;
  supported_models?: string;
  api_key_masked?: string;
  max_concurrent?: number;
  rate_limit_rpm?: number;
}

export interface APIKeyRequest {
  id?: number;
  name: string;
  /** 创建时可空，后端自动生成；更新时若空则保留原值 */
  api_key: string;
  enabled: boolean;
  expire_at: number;
  supported_models: string;
  /** 0 = 不限；负值后端 400 */
  max_concurrent: number;
  rate_limit_rpm: number;
}

// ============================================================
// 仪表盘 / 版本
// ============================================================

export interface ModelTokenUsage {
  name: string;
  input: number;
  output: number;
}

export interface NowVersion {
  version: string;
  commit: string;
  build_time: string;
  client_ip_count: number;
  total_requests: number;
  /** 选中时间窗口内的错误数（forever 为进程累计全量）。 */
  error_count: number;
  total_tokens_input: number;
  total_tokens_output: number;
  tokens_by_model: ModelTokenUsage[];
  /** 推理 token 总量（reasoning/thinking tokens）。 */
  reasoning_tokens?: number;
  /** 缓存命中 token 总量。 */
  cached_tokens?: number;
  /** 预计消耗（USD）。 */
  total_cost?: number;
  /** 使用时长（毫秒）。 */
  total_duration_ms?: number;
}

/** 后端构建元信息（轻量端点 /update/build-info，不含统计聚合）。 */
export interface BuildInfo {
  version: string;
  commit: string;
  build_time: string;
}

/** Token 趋势档位：与后端 op.ValidUsageRange 对齐（前端不暴露 forever 全量档）。 */
export type TokenTrendRange = "24h" | "7d" | "30d" | "1y" | "3y";

/**
 * Token 用量趋势单点 —— /api/v1/update/token-trends?range=
 * t 为采样区间起点（Unix 毫秒），in/out 为该区间全部模型的 token 合计。
 */
export interface TokenTrendPoint {
  t: number;
  in: number;
  out: number;
}

/** 详细指标 —— /api/v1/stats/usage-detail?range= */
export interface UsageDetail {
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_tokens: number;
  cost: number;
  duration_ms: number;
  request_count: number;
}

/** 热力图单点 —— /api/v1/stats/usage-heatmap?days= */
export interface UsageHeatmapPoint {
  /** UTC 日期 YYYY-MM-DD */
  date: string;
  /** 该日 input+output token 合计 */
  tokens: number;
  /** 该日预计消耗（USD） */
  cost: number;
  /** 该日请求数 */
  count: number;
}

export interface LastSyncTime {
  last_sync_at: string;
}

// ============================================================
// 日志
// ============================================================

export type RequestStatus =
  | "running"
  | "committed"
  | "success"
  | "failed"
  | "canceled";

export type AttemptOutcome = "success" | "failed" | "canceled";

export type ErrClass =
  | "zero_output"
  | "early_eof"
  | "timeout"
  | "client_cancel"
  | "admin_abort"
  | "upstream_4xx"
  | "upstream_5xx"
  | "upstream_network"
  | "upstream_error";

export interface AttemptRecord {
  seq: number;
  channel_id: number;
  channel_name: string;
  member_id: number;
  model: string;
  key_label?: string;
  proxy_addr?: string;
  /** 本轮首字耗时毫秒(TTFT)，首字未到为 0 或 undefined。 */
  first_token_ms?: number;
  latency_ms: number;
  outcome: AttemptOutcome;
  err_class?: ErrClass;
  err_brief?: string;
}

export interface RequestState {
  id: number;
  status: RequestStatus;
  started_at: string;
  /** 首字时点：首个已交付客户端的事件/整响应提交的时刻，用于展示首字耗时（TTFT）。 */
  first_token_at?: string;
  /** 后端同时提供 legacy duration（纳秒）与 duration_ms（毫秒）；旧服务可能只给 duration。 */
  duration?: number;
  duration_ms?: number;
  model: string;
  client_ip: string;
  /** 仅为脱敏尾缀（例如 ...ABCD），绝不是完整 API Key。 */
  api_key?: string;
  key_name?: string;
  /** 最新一轮出口代理地址（密码打码）；「系统代理 」前缀表示走全局代理，空为直连 */
  proxy_addr?: string;
  usage: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
    /** 命中上游缓存的输入 token 数（cache read），仅当 > 0 时存在。 */
    cache_tokens?: number;
    /** 上游返回的 prompt token 明细；缓存读 token 嵌套在此字段中。 */
    prompt_tokens_details?: {
      cached_tokens?: number;
    };
  };
  usage_estimated: boolean;
  round: number;
  target_channel: string;
  target_model: string;
  thinking_level?: string;
  client_format: string;
  upstream_type: string;
  relay_mode: "passthrough" | "converted";
  masked?: boolean;
  sending: boolean;
  attempts?: AttemptRecord[];
  /**
   * 本次请求脱敏命中的规则明细（文档 07）：仅在脱敏发生时由状态流下发。
   * 实施边界修订: 日志命中明细只含 label + placeholder 安全摘要, 不下发 original
   * (查看日志原文不是已批准能力); 与预览接口 MaskTestMatch 类型分离。
   * 旧版本进程 / 开关关闭时缺省或为空数组，前端按可选处理，无命中时不渲染。
   */
  mask_matches?: MaskMatchSummary[];
}

export interface FailureSummary {
  id: number;
  finished_at: string;
  model: string;
  target_channel: string;
  target_model: string;
  err_class: ErrClass;
  err_brief: string;
}

/**
 * /api/v1/log/failures 实时失败环形缓冲返回的快照，
 * 与 FailureSummary 同结构，命名以利前端语义区分（"实时" vs "持久化"）。
 */
export type FailedRuntime = FailureSummary;

export interface ErrorLog {
  id: number;
  created_at: string;
  model: string;
  channel_name: string;
  target_model: string;
  client_ip: string;
  api_key_name?: string;
  api_key_suffix?: string;
  client_format?: string;
  upstream_type?: string;
  relay_mode?: string;
  err_class: string;
  err_brief: string;
  request_body?: string;
  err_detail?: string;
  /**
   * 持久化错误日志携带的脱敏命中明细（文档 07 §3.2）：
   * 实施边界修订: 只含 label + placeholder, 不持久化 original。
   * 仅在保留完整请求体的条目上附带，旧记录该字段缺省/空数组，天然兼容。
   */
  mask_matches?: MaskMatchSummary[];
}

export interface ClientStats {
  client_ip: string;
  requests: number;
  /** 后端 ClientStat 无错误数字段；此前声明的 error_count 是幻影字段，恒 0 */
  first_seen?: string;
  last_seen: string;
}

export interface GroupClearCooldownResult {
  group_id: number;
  channels: number;
  member_items: number;
  key_cooldowns: number;
  rate_windows: number;
}

export interface StopAllState {
  /** 后端字段名；表示是否阻止新请求进入转发循环。 */
  is_stopped: boolean;
}

export interface StopAllResult extends StopAllState {
  /** 本次实际请求 Stop 的在途请求数。 */
  stopped: number;
}

// ============================================================
// 设置
// ============================================================

export type SettingKey =
  | "error_retention_days"
  | "error_retention_max_count"
  | string;

export interface SettingItem {
  key: SettingKey;
  value: string;
}

/** 一键填充渠道自定义 Header 的命名模板（对应后端 model.HeaderTemplate）。 */
export interface HeaderTemplate {
  name: string;
  headers: CustomHeader[];
}

/** header_templates 设置项存储的 JSON 数组元素约束（与后端防呆上限一致）。 */
export const HEADER_TEMPLATES_SETTING_KEY = "header_templates";

export interface DBDump {
  version: number;
  exported_at?: string;
  note?: string;
  channels?: unknown[];
  channel_models?: unknown[];
  groups?: unknown[];
  group_items?: unknown[];
  api_keys?: unknown[];
  settings?: unknown[];
}

export interface DBImportResult {
  rows_affected: Record<string, number>;
}

/** 兼容旧设置导入的 KV 形状；完整备份使用 DBDump。 */
export interface Settings {
  [key: string]: string;
}

// ----- 脱敏（Mask）-----

/**
 * 脱敏自定义敏感词配置项，与后端 model.MaskConfigTerm 对齐。
 * Value 为敏感词原文，Category 仅供管理台分组展示，不参与匹配。
 */
export interface MaskConfigTerm {
  value: string;
  category: string;
}

/**
 * 脱敏全局配置，与后端 model.MaskConfig 对齐。
 * 出厂状态恒为全关（enabled=false、builtin_rule_switch 全 false、custom_terms 为空）。
 */
export interface MaskConfig {
  enabled: boolean;
  builtin_rule_switch: Record<string, boolean>;
  custom_terms: MaskConfigTerm[];
}

/** 内置规则元信息，与后端 mask.RuleMeta 对齐。 */
export interface MaskRuleMeta {
  label: string;
  description: string;
  default_enabled: boolean;
}

/** 脱敏测试命中明细，与后端 handlers.maskTestMatch 对齐（预览接口，含命中原文）。 */
export interface MaskTestMatch {
  label: string;
  original: string;
  placeholder: string;
}

/** 日志命中明细安全摘要（文档 07）：只含规则标签 + 占位符, 不含 original。
 * 与 MaskTestMatch 分离: 预览接口可展示原文, 日志命中明细首期不下发/不持久化/不展示原文。 */
export interface MaskMatchSummary {
  label: string;
  placeholder: string;
}

/** 脱敏测试响应，与后端 handlers.maskTestResult 对齐。 */
export interface MaskTestResult {
  masked: string;
  matches: MaskTestMatch[];
}

// ============================================================
// 代理池
// ============================================================

/** 代理池条目，与后端 model.ProxyEntry 对齐。 */
export interface ProxyEntry {
  id: string;
  name: string;
  url: string;
  enabled: boolean;
}

/** 代理测试结果，与后端 handlers.proxyTestResult 对齐。 */
export interface ProxyTestResult {
  ip: string;
  elapsed: number;
}
