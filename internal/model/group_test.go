package model

import (
	"encoding/json"
	"testing"
)

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
