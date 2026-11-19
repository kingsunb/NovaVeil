package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestTokenDensitySaveGetDelete(t *testing.T) {
	ctx := context.Background()
	const channelID = 9901
	const modelName = "density-persist-test"
	cleanup := func() {
		_ = db.GetDB().Where("channel_id = ? AND model = ?", channelID, modelName).Delete(&model.TokenDensity{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := TokenDensitySave(ctx, channelID, modelName, 0.87, 3, 2, "2026-11-19"); err != nil {
		t.Fatalf("save density: %v", err)
	}
	row, found, err := TokenDensityGet(ctx, channelID, modelName)
	if err != nil || !found {
		t.Fatalf("get density: found=%v err=%v", found, err)
	}
	if row.Density != 0.87 || row.SampleCount != 3 || row.DailySamples != 2 || row.SampleDay != "2026-11-19" {
		t.Fatalf("density row = %+v", row)
	}

	if err := TokenDensitySave(ctx, channelID, modelName, 0.9, 4, 3, "2026-11-20"); err != nil {
		t.Fatalf("update density: %v", err)
	}
	row, found, err = TokenDensityGet(ctx, channelID, modelName)
	if err != nil || !found || row.Density != 0.9 || row.SampleCount != 4 || row.DailySamples != 3 || row.SampleDay != "2026-11-20" {
		t.Fatalf("updated density row = %+v, found=%v err=%v", row, found, err)
	}

	if err := TokenDensityDelete(ctx, channelID, modelName); err != nil {
		t.Fatalf("delete density: %v", err)
	}
	if _, found, err := TokenDensityGet(ctx, channelID, modelName); err != nil || found {
		t.Fatalf("deleted density still present: found=%v err=%v", found, err)
	}
}

func TestTokenDensityListIncludesChannelName(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    "density-list-test",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://density-list.invalid",
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channel.ID).Delete(&model.TokenDensity{}).Error
		_ = db.GetDB().Delete(&model.Channel{}, channel.ID).Error
	})

	if err := TokenDensitySave(ctx, channel.ID, "density-list-model", 0.5, 1, 1, "2026-11-19"); err != nil {
		t.Fatalf("save density: %v", err)
	}
	rows, err := TokenDensityList(ctx)
	if err != nil {
		t.Fatalf("list density: %v", err)
	}
	for _, row := range rows {
		if row.ChannelID == channel.ID && row.Model == "density-list-model" {
			if row.ChannelName != channel.Name {
				t.Fatalf("channel name = %q, want %q", row.ChannelName, channel.Name)
			}
			return
		}
	}
	t.Fatal("density row not found in list")
}
