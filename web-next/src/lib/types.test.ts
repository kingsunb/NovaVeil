import { describe, expect, it } from "vitest";

import { DEFAULT_GROUP_RELAY_CONFIG } from "./types";

/**
 * DEFAULT_GROUP_RELAY_CONFIG 必须与后端 internal/model/group.go 的
 * DefaultGroupRelayConfig（failover-first 调优版）保持一致；后端改默认值时
 * 这里会红，提醒同步前端（审计 §1.5）。
 */
describe("DEFAULT_GROUP_RELAY_CONFIG", () => {
  it("与后端 DefaultGroupRelayConfig 关键值对齐", () => {
    expect(DEFAULT_GROUP_RELAY_CONFIG.member_max_attempts).toBe(3);
    expect(DEFAULT_GROUP_RELAY_CONFIG.member_infra_max_retries).toBe(3);
    expect(DEFAULT_GROUP_RELAY_CONFIG.member_retry_interval_seconds).toBe(2);
    expect(
      DEFAULT_GROUP_RELAY_CONFIG.member_non_stream_response_timeout_seconds,
    ).toBe(1200);
    expect(
      DEFAULT_GROUP_RELAY_CONFIG.member_stream_first_event_timeout_seconds,
    ).toBe(60);
    expect(DEFAULT_GROUP_RELAY_CONFIG.member_cooldown_seconds).toBe(60);
    expect(DEFAULT_GROUP_RELAY_CONFIG.member_affinity_seconds).toBe(300);
    expect(DEFAULT_GROUP_RELAY_CONFIG.max_request_rounds).toBe(600);
    expect(DEFAULT_GROUP_RELAY_CONFIG.max_request_seconds).toBe(0);
    expect(DEFAULT_GROUP_RELAY_CONFIG.session_sticky_enabled).toBe(true);
    expect(DEFAULT_GROUP_RELAY_CONFIG.session_sticky_seconds).toBe(300);
    expect(DEFAULT_GROUP_RELAY_CONFIG.cooldown_backoff_multiplier).toBe(2);
    expect(DEFAULT_GROUP_RELAY_CONFIG.cooldown_max_seconds).toBe(1800);
    expect(DEFAULT_GROUP_RELAY_CONFIG.background_probe_enabled).toBe(false);
    expect(DEFAULT_GROUP_RELAY_CONFIG.background_probe_interval_seconds).toBe(60);
    expect(DEFAULT_GROUP_RELAY_CONFIG.emergency_item_id).toBe(0);
    expect(DEFAULT_GROUP_RELAY_CONFIG.prefer_passthrough).toBe(false);
  });
});
