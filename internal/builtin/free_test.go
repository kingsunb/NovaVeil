package builtin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/testutil"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	m.Run()
}

func testChannel(name string) model.Channel {
	return model.Channel{
		Name:    name,
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://free.example.invalid/v1",
		Models: []model.ChannelModel{
			{Name: "free-model"},
			{Name: "free-model-2"},
		},
	}
}

func TestEnsureBuiltinFreeChannelsInsertsMissing(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()

	name := fmt.Sprintf("builtin-free-%d", time.Now().UnixNano())
	BuiltinFreeChannels = []model.Channel{testChannel(name)}

	if err := EnsureBuiltinFreeChannels(ctx); err != nil {
		t.Fatalf("EnsureBuiltinFreeChannels: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, "name = ?", name).Error; err != nil {
		t.Fatalf("load builtin channel: %v", err)
	}
	if !stored.IsFree {
		t.Fatal("builtin channel should have IsFree=true")
	}
	if !stored.Builtin {
		t.Fatal("builtin channel should have Builtin=true")
	}
	if !stored.Enabled {
		t.Fatal("builtin channel should default Enabled=true")
	}
	var modelCount int64
	if err := db.GetDB().Model(&model.ChannelModel{}).Where("channel_id = ?", stored.ID).Count(&modelCount).Error; err != nil {
		t.Fatalf("count builtin models: %v", err)
	}
	if modelCount != 2 {
		t.Fatalf("builtin channel models in db = %d, want 2", modelCount)
	}
}

func TestEnsureBuiltinFreeChannelsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()

	name := fmt.Sprintf("builtin-idempotent-%d", time.Now().UnixNano())
	channel := testChannel(name)
	BuiltinFreeChannels = []model.Channel{channel}

	if err := EnsureBuiltinFreeChannels(ctx); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	var before model.Channel
	if err := db.GetDB().First(&before, "name = ?", name).Error; err != nil {
		t.Fatalf("load before: %v", err)
	}

	if err := EnsureBuiltinFreeChannels(ctx); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	var after []model.Channel
	if err := db.GetDB().Where("name = ?", name).Find(&after).Error; err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("second Ensure duplicated channel, count = %d", len(after))
	}
	if after[0].ID != before.ID {
		t.Fatalf("second Ensure changed channel ID: before=%d after=%d", before.ID, after[0].ID)
	}
}

func TestEnsureBuiltinFreeChannelsDoesNotOverwriteUserChannel(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()

	name := fmt.Sprintf("custom-owns-name-%d", time.Now().UnixNano())
	custom := model.Channel{
		Name:           name,
		Type:           model.ChannelProviderOpenAI,
		Enabled:        true,
		IsFree:         false,
		Builtin:        false,
		OpencodeCompat: true,
		BaseURL:        "https://custom.example.invalid/v1",
	}
	if err := db.GetDB().Create(&custom).Error; err != nil {
		t.Fatalf("create custom channel: %v", err)
	}

	BuiltinFreeChannels = []model.Channel{testChannel(name)}
	if err := EnsureBuiltinFreeChannels(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, custom.ID).Error; err != nil {
		t.Fatalf("reload custom channel: %v", err)
	}
	if stored.Builtin || stored.IsFree {
		t.Fatalf("builtin seed must not overwrite user channel: Builtin=%v IsFree=%v", stored.Builtin, stored.IsFree)
	}
}

func TestEnsureBuiltinFreeChannelsClearsCustomFreeFlag(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()
	BuiltinFreeChannels = nil

	name := fmt.Sprintf("custom-free-cleared-%d", time.Now().UnixNano())
	custom := model.Channel{
		Name:           name,
		Type:           model.ChannelProviderOpenAI,
		Enabled:        true,
		IsFree:         true,
		Builtin:        false,
		OpencodeCompat: true,
		BaseURL:        "https://custom.example.invalid/v1",
	}
	if err := db.GetDB().Create(&custom).Error; err != nil {
		t.Fatalf("create custom channel: %v", err)
	}

	if err := EnsureBuiltinFreeChannels(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, custom.ID).Error; err != nil {
		t.Fatalf("reload custom channel: %v", err)
	}
	if stored.IsFree {
		t.Fatalf("custom channel should have IsFree cleared, got true")
	}
	if stored.Builtin {
		t.Fatal("custom channel should remain Builtin=false")
	}

	if stored.OpencodeCompat {
		t.Fatal("custom channel should have OpencodeCompat cleared, got true")
	}
}

func TestBuiltinFreeChannelsDefinitions(t *testing.T) {
	if len(BuiltinFreeChannels) == 0 {
		t.Fatal("BuiltinFreeChannels should contain at least one fixed free provider")
	}
	seen := make(map[string]bool, len(BuiltinFreeChannels))
	for i, def := range BuiltinFreeChannels {
		if def.Name == "" {
			t.Fatalf("entry %d missing Name", i)
		}
		if seen[def.Name] {
			t.Fatalf("duplicate builtin free channel name %q", def.Name)
		}
		seen[def.Name] = true
		if def.Type == "" {
			t.Fatalf("builtin free channel %q missing Type", def.Name)
		}
		if def.Type != model.ChannelProviderCustom && def.BaseURL == "" {
			t.Fatalf("builtin free channel %q missing BaseURL", def.Name)
		}
		switch def.Type {
		case model.ChannelProviderOpenAI,
			model.ChannelProviderOpenAIResponses,
			model.ChannelProviderAnthropic,
			model.ChannelProviderGemini,
			model.ChannelProviderVolcengine,
			model.ChannelProviderCustom:
		default:
			t.Fatalf("builtin free channel %q uses unsupported Type %q", def.Name, def.Type)
		}
		if len(def.Models) == 0 {
			t.Fatalf("builtin free channel %q should ship a fallback model list", def.Name)
		}
	}
}
