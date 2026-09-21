package task

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/testutil"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	if err := op.InitCache(); err != nil {
		panic(err)
	}
	m.Run()
}

func TestSyncRebuildAutoKeepsProtocol(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen", []model.ChannelModel{
		{Name: "kept", Source: model.ChannelModelSourceAuto, UpstreamProtocol: model.UpstreamProtocolAnthropic},
		{Name: "manual", Source: model.ChannelModelSourceManual, UpstreamProtocol: model.UpstreamProtocolResponses},
	})
	restore := stubSyncFetch(
		[]string{"kept", "manual", "added"},
		map[string]string{
			"kept":   model.UpstreamProtocolAnthropic,
			"manual": model.UpstreamProtocolChat,
			"added":  "",
		},
		nil,
	)
	defer restore()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
	got := loadProtocols(t, channel.ID)
	if got["kept"] != model.UpstreamProtocolAnthropic {
		t.Fatalf("rebuilt auto protocol = %q, want anthropic", got["kept"])
	}
	if got["manual"] != model.UpstreamProtocolResponses {
		t.Fatalf("manual protocol = %q, want responses", got["manual"])
	}
	if _, ok := got["added"]; !ok {
		t.Fatal("new auto model was not created")
	}
	if got["added"] != "" {
		t.Fatalf("unknown new model protocol = %q, want empty", got["added"])
	}

	restore()
	restore = stubSyncFetch(
		[]string{"kept", "manual", "added"},
		map[string]string{"kept": model.UpstreamProtocolChat},
		nil,
	)
	defer restore()
	if err := SyncModelsTask(); err != nil {
		t.Fatalf("second SyncModelsTask: %v", err)
	}
	got = loadProtocols(t, channel.ID)
	if got["kept"] != model.UpstreamProtocolChat {
		t.Fatalf("catalog update protocol = %q, want chat", got["kept"])
	}
	if got["manual"] != model.UpstreamProtocolResponses {
		t.Fatalf("manual protocol overwritten: %q", got["manual"])
	}
	if got["added"] != "" {
		t.Fatalf("model absent from catalog lost empty protocol: %q", got["added"])
	}
}

func TestSyncCatalogFailureKeepsModelsAndProtocols(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen/go", []model.ChannelModel{
		{Name: "kept", Source: model.ChannelModelSourceAuto, UpstreamProtocol: model.UpstreamProtocolAnthropic},
		{Name: "manual", Source: model.ChannelModelSourceManual, UpstreamProtocol: model.UpstreamProtocolChat},
	})
	restore := stubSyncFetch([]string{"kept", "manual", "added"}, nil, fmt.Errorf("catalog down"))
	defer restore()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
	got := loadProtocols(t, channel.ID)
	if len(got) != 3 {
		t.Fatalf("models = %#v, want kept, manual, added", got)
	}
	if got["kept"] != model.UpstreamProtocolAnthropic {
		t.Fatalf("catalog failure cleared protocol: %q", got["kept"])
	}
	if got["manual"] != model.UpstreamProtocolChat {
		t.Fatalf("manual protocol = %q", got["manual"])
	}
	if got["added"] != "" {
		t.Fatalf("new model during catalog failure = %q, want empty", got["added"])
	}
}

func insertSyncChannel(t *testing.T, baseURL string, models []model.ChannelModel) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name:           fmt.Sprintf("sync-oc-%d", time.Now().UnixNano()),
		Type:           model.ChannelProviderOpenAI,
		Enabled:        true,
		Builtin:        true,
		OpencodeCompat: true,
		AutoSync:       true,
		BaseURL:        baseURL,
		Key:            "public",
		Models:         models,
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() {
		db.GetDB().Where("channel_id = ?", channel.ID).Delete(&model.ChannelModel{})
		db.GetDB().Delete(&model.Channel{}, channel.ID)
		_ = op.InitCache()
	})
	if err := op.InitCache(); err != nil {
		t.Fatalf("refresh cache: %v", err)
	}
	return channel
}

func stubSyncFetch(models []string, protocols map[string]string, catalogErr error) func() {
	prevModels := fetchChannelModels
	prevCatalog := fetchOpencodeProtocols
	fetchChannelModels = func(context.Context, model.Channel) ([]string, error) {
		out := append([]string(nil), models...)
		return out, nil
	}
	fetchOpencodeProtocols = func(context.Context, model.Channel) (map[string]string, error) {
		if catalogErr != nil {
			return nil, catalogErr
		}
		out := make(map[string]string, len(protocols))
		for name, protocol := range protocols {
			out[name] = protocol
		}
		return out, nil
	}
	return func() {
		fetchChannelModels = prevModels
		fetchOpencodeProtocols = prevCatalog
	}
}

func loadProtocols(t *testing.T, channelID int) map[string]string {
	t.Helper()
	var rows []model.ChannelModel
	if err := db.GetDB().Where("channel_id = ?", channelID).Find(&rows).Error; err != nil {
		t.Fatalf("load models: %v", err)
	}
	got := make(map[string]string, len(rows))
	for _, row := range rows {
		got[row.Name] = row.UpstreamProtocol
	}
	return got
}
