package op

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestChannelUpdateBaseURLResetsDensity(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    fmt.Sprintf("density-reset-%d", time.Now().UnixNano()),
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://density-a.example",
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	t.Cleanup(func() { _ = ChannelDel(channel.ID, context.Background()) })

	if err := TokenDensitySave(ctx, channel.ID, "density-model", 0.87, 3, 1, "2026-11-19"); err != nil {
		t.Fatalf("TokenDensitySave: %v", err)
	}

	newBaseURL := "https://density-b.example"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, BaseURL: &newBaseURL}, ctx); err != nil {
		t.Fatalf("ChannelUpdate: %v", err)
	}

	var count int64
	if err := db.GetDB().Model(&model.TokenDensity{}).Where("channel_id = ?", channel.ID).Count(&count).Error; err != nil {
		t.Fatalf("count densities: %v", err)
	}
	if count != 0 {
		t.Fatalf("base url change should clear densities, got %d rows", count)
	}
}
