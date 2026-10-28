package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGroupRelayConfigReasoningOnlyKeyCompat 校验新旧开关键指向同一字段:
// 旧键优先, 旧键缺省时才采纳 reasoning_only_as_tool_calls, 避免配置值静默丢失。
func TestGroupRelayConfigReasoningOnlyKeyCompat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   bool
	}{
		{"默认关闭", `{}`, false},
		{"旧键开启", `{"reasoning_fallback_to_text":true}`, true},
		{"新键开启", `{"reasoning_only_as_tool_calls":true}`, true},
		{"新键显式关闭", `{"reasoning_only_as_tool_calls":false}`, false},
		{"两键冲突取旧键", `{"reasoning_fallback_to_text":true,"reasoning_only_as_tool_calls":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config GroupRelayConfig
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatalf("解析配置失败: %v", err)
			}
			if got := config.ReasoningOnlyAsToolCalls; got != tc.want {
				t.Fatalf("开关应为 %v, 实际 %v", tc.want, got)
			}
		})
	}

	// 序列化仍写旧键, 前端与既有配置文件键名不变。
	encoded, err := json.Marshal(GroupRelayConfig{ReasoningOnlyAsToolCalls: true})
	if err != nil {
		t.Fatalf("序列化配置失败: %v", err)
	}
	if !strings.Contains(string(encoded), `"reasoning_fallback_to_text":true`) {
		t.Fatalf("序列化应仍写旧键, 实际: %s", encoded)
	}
}

func TestNormalizeGroupRelayConfigRetryInterval(t *testing.T) {
	defaults := DefaultGroupRelayConfig()
	for _, tc := range []struct {
		name   string
		config string
		want   float64
	}{
		{
			name:   "empty config keeps default",
			config: `{}`,
			want:   defaults.MemberRetryIntervalSeconds,
		},
		{
			name:   "zero retries immediately",
			config: `{"member_max_attempts":3,"member_retry_interval_seconds":0}`,
			want:   0,
		},
		{
			name:   "legacy integer interval is preserved",
			config: `{"member_max_attempts":3,"member_retry_interval_seconds":5}`,
			want:   5,
		},
		{
			name:   "hundredth of a second is preserved",
			config: `{"member_max_attempts":3,"member_retry_interval_seconds":0.01}`,
			want:   0.01,
		},
		{
			name:   "longer fractional interval is preserved",
			config: `{"member_max_attempts":3,"member_retry_interval_seconds":2.5}`,
			want:   2.5,
		},
		{
			name:   "negative interval uses default",
			config: `{"member_max_attempts":3,"member_retry_interval_seconds":-0.01}`,
			want:   defaults.MemberRetryIntervalSeconds,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config GroupRelayConfig
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatalf("解析配置失败: %v", err)
			}
			NormalizeGroupRelayConfig(&config)
			if got := config.MemberRetryIntervalSeconds; got != tc.want {
				t.Fatalf("重试间隔应为 %g, 实际 %g", tc.want, got)
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("序列化配置失败: %v", err)
			}
			var restored GroupRelayConfig
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatalf("重新读取配置失败: %v", err)
			}
			if got := restored.MemberRetryIntervalSeconds; got != tc.want {
				t.Fatalf("保存后重试间隔应为 %g, 实际 %g", tc.want, got)
			}
		})
	}
}
