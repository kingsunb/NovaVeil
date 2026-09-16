import type { Channel, Group } from "@/lib/types";

/**
 * 共享测试 fixtures —— 替代各 *.test.tsx 里散落的 sampleChannel/sampleGroup
 *  每个 fixture 保持与 lib/types 一致的字段；类型由 api.ts 后端契约驱动
 */

export const sampleChannel: Channel = {
  id: 1,
  name: "openai-prod",
  type: "openai",
  enabled: true,
  base_url: "https://api.example.com",
  key: "",
  key_masked: "sk-N…ABCD",
  keys: [
    { id: "k1", key: "", key_masked: "sk-N…ABCD", remark: "" },
  ],
  models: [
    { id: 100, channel_id: 1, name: "gpt-4o", source: "auto" },
    { id: 101, channel_id: 1, name: "gpt-4o-mini", source: "auto" },
  ],
  fixed_reply: "",
  proxy: false,
  auto_sync: false,
  opencode_compat: false,
  custom_header: [],
  model_limits: {},
  tags: ["prod"],
  sort: 0,
  rate_limit_rpm: 60,
  max_concurrent: 5,
  pass_through_body_enabled: false,
};

export const sampleChannelDisabled: Channel = {
  ...sampleChannel,
  id: 2,
  name: "openai-dev",
  enabled: false,
};

export const sampleGroup: Group = {
  id: 10,
  name: "gpt-4o-prod",
  mode: "failover",
  active_item_id: 0,
  relay_config: {
    member_max_attempts: 3,
    member_infra_max_retries: 3,
    member_retry_interval_seconds: 3,
    member_non_stream_response_timeout_seconds: 1200,
    member_stream_first_event_timeout_seconds: 300,
    member_cooldown_seconds: 60,
    member_affinity_seconds: 300,
    max_request_rounds: 60,
    max_request_seconds: 0,
    session_sticky_enabled: false,
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
  },
  items: [
    { id: 1, group_id: 10, channel_model_id: 100, ref_group_name: "", priority: 1 },
    { id: 2, group_id: 10, channel_model_id: 101, ref_group_name: "", priority: 2 },
  ],
};
