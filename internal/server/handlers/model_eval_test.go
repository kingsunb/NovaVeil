package handlers

import (
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestRedactChannelCredentials(t *testing.T) {
	channel := model.Channel{
		Key:  "sk-direct",
		Keys: []model.ChannelKey{{Key: "sk-pool"}, {Key: ""}},
	}
	got := redactChannelCredentials(channel, "upstream echo sk-direct sk-pool")
	if strings.Contains(got, "sk-direct") || strings.Contains(got, "sk-pool") {
		t.Fatalf("密钥未脱敏: %s", got)
	}
	if count := strings.Count(got, "[REDACTED]"); count != 2 {
		t.Fatalf("脱敏次数 = %d, want 2: %s", count, got)
	}
	if got := redactChannelCredentials(model.Channel{}, "无凭据"); got != "无凭据" {
		t.Fatalf("无凭据时不应改动: %s", got)
	}
}
