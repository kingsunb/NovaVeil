package op

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestNextUTCZero(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "UTC 整点跨日",
			now:  time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "北京时间折算",
			now:  time.Date(2026, 11, 8, 22, 57, 0, 0, time.FixedZone("CST", 8*3600)),
			want: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		if got := nextUTCZero(tc.now); !got.Equal(tc.want) {
			t.Fatalf("%s: nextUTCZero(%s) = %s, want %s", tc.name, tc.now, got, tc.want)
		}
	}
}

func TestChannelAutoDisableAndExpire(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    fmt.Sprintf("auto-disable-%d", time.Now().UnixNano()),
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://example.invalid",
		Enabled: true,
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}

	if err := ChannelAutoDisable(channel.ID, ctx); err != nil {
		t.Fatalf("ChannelAutoDisable: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load stored channel: %v", err)
	}
	if stored.Enabled {
		t.Fatal("429 后渠道应停用")
	}
	if stored.AutoDisabledUntil == nil || !stored.AutoDisabledUntil.After(time.Now()) {
		t.Fatalf("自动停用截止时刻应在未来, got %v", stored.AutoDisabledUntil)
	}
	if cached, err := ChannelGetCore(channel.ID); err != nil || cached.Enabled || cached.AutoDisabledUntil == nil {
		t.Fatalf("缓存应同步停用状态, err=%v", err)
	}

	// 未到期时恢复任务不应改动渠道。
	if _, err := ChannelAutoEnableExpired(ctx); err != nil {
		t.Fatalf("ChannelAutoEnableExpired: %v", err)
	}
	if cached, _ := ChannelGetCore(channel.ID); cached.Enabled {
		t.Fatal("未到期不应恢复")
	}

	// 把截止时刻回拨到过去, 模拟跨过 UTC 零点。
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).
		Update("auto_disabled_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("rewind auto_disabled_until: %v", err)
	}
	restored, err := ChannelAutoEnableExpired(ctx)
	if err != nil {
		t.Fatalf("ChannelAutoEnableExpired: %v", err)
	}
	if restored == 0 {
		t.Fatal("到期渠道应被恢复")
	}
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if !stored.Enabled || stored.AutoDisabledUntil != nil {
		t.Fatalf("恢复后应启用且清空标记, enabled=%v until=%v", stored.Enabled, stored.AutoDisabledUntil)
	}
	if cached, _ := ChannelGetCore(channel.ID); !cached.Enabled || cached.AutoDisabledUntil != nil {
		t.Fatal("缓存应同步恢复状态")
	}
}

func TestChannelEnabledClearsAutoDisable(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    fmt.Sprintf("auto-disable-manual-%d", time.Now().UnixNano()),
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://example.invalid",
		Enabled: true,
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	if err := ChannelAutoDisable(channel.ID, ctx); err != nil {
		t.Fatalf("ChannelAutoDisable: %v", err)
	}
	if err := ChannelEnabled(channel.ID, true, ctx); err != nil {
		t.Fatalf("ChannelEnabled: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load stored channel: %v", err)
	}
	if !stored.Enabled || stored.AutoDisabledUntil != nil {
		t.Fatalf("人工启用应清空自动停用标记, enabled=%v until=%v", stored.Enabled, stored.AutoDisabledUntil)
	}
	if cached, _ := ChannelGetCore(channel.ID); !cached.Enabled || cached.AutoDisabledUntil != nil {
		t.Fatal("缓存应同步人工启用状态")
	}
}
