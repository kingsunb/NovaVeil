package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/testutil"
	"gorm.io/gorm"
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

func TestSyncCatalogFailureKeepsModelMetadata(t *testing.T) {
	updateCB := db.GetDB().Callback().Update()
	const cbName = "test_count_sync_model_updates"
	modelUpdates := 0
	if err := updateCB.Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channel_models" {
			modelUpdates++
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		if err := updateCB.Remove(cbName); err != nil {
			t.Errorf("remove update callback: %v", err)
		}
	}()

	channel := insertSyncChannel(t, "https://opencode.ai/zen/go", []model.ChannelModel{{
		Name:               "kept",
		Source:             model.ChannelModelSourceAuto,
		UpstreamProtocol:   model.UpstreamProtocolAnthropic,
		ContextWindow:      200000,
		MaxOutput:          8192,
		Reasoning:          true,
		ToolCall:           true,
		InputModalities:    "text,image",
		MaxReasoningEffort: "high",
	}})
	restore := stubSyncFetchCatalog([]string{"kept"}, nil, errors.New("catalog down"))
	defer restore()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
	got := loadChannelModels(t, channel.ID)["kept"]
	if got.UpstreamProtocol != model.UpstreamProtocolAnthropic {
		t.Fatalf("catalog failure changed protocol: %q", got.UpstreamProtocol)
	}
	if got.ContextWindow != 200000 {
		t.Fatalf("catalog failure changed context window: %d", got.ContextWindow)
	}
	if got.MaxOutput != 8192 {
		t.Fatalf("catalog failure changed max output: %d", got.MaxOutput)
	}
	if !got.Reasoning || !got.ToolCall {
		t.Fatalf("catalog failure changed capabilities: reasoning=%v tool_call=%v", got.Reasoning, got.ToolCall)
	}
	if got.InputModalities != "text,image" {
		t.Fatalf("catalog failure changed input modalities: %q", got.InputModalities)
	}
	if got.MaxReasoningEffort != "high" {
		t.Fatalf("catalog failure changed max reasoning effort: %q", got.MaxReasoningEffort)
	}
	if modelUpdates != 0 {
		t.Fatalf("catalog failure wrote channel models %d time(s), want 0", modelUpdates)
	}
}

func TestSyncCatalogMissingEntryKeepsModelMetadata(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen/go", []model.ChannelModel{{
		Name:               "kept",
		Source:             model.ChannelModelSourceAuto,
		UpstreamProtocol:   model.UpstreamProtocolAnthropic,
		ContextWindow:      200000,
		MaxOutput:          8192,
		Reasoning:          true,
		ToolCall:           true,
		InputModalities:    "text,image",
		MaxReasoningEffort: "high",
	}})
	restore := stubSyncFetchCatalog(
		[]string{"kept", "fresh"},
		map[string]helper.OpencodeModelInfo{
			"fresh": {
				Protocol:           model.UpstreamProtocolChat,
				ContextWindow:      128000,
				MaxOutput:          4096,
				Reasoning:          true,
				ToolCall:           true,
				InputModalities:    []string{"text", "image", "audio"},
				MaxReasoningEffort: "medium",
			},
		},
		nil,
	)
	defer restore()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
	got := loadChannelModels(t, channel.ID)
	kept := got["kept"]
	if kept.UpstreamProtocol != model.UpstreamProtocolAnthropic ||
		kept.ContextWindow != 200000 ||
		kept.MaxOutput != 8192 ||
		!kept.Reasoning ||
		!kept.ToolCall ||
		kept.InputModalities != "text,image" ||
		kept.MaxReasoningEffort != "high" {
		t.Fatalf("catalog missing entry changed metadata: %#v", kept)
	}
	fresh, ok := got["fresh"]
	if !ok {
		t.Fatal("catalog model was not created")
	}
	if fresh.UpstreamProtocol != model.UpstreamProtocolChat ||
		fresh.ContextWindow != 128000 ||
		fresh.MaxOutput != 4096 ||
		!fresh.Reasoning ||
		!fresh.ToolCall ||
		fresh.InputModalities != "text,image,audio" ||
		fresh.MaxReasoningEffort != "medium" {
		t.Fatalf("catalog model metadata = %#v", fresh)
	}
}

func TestSyncCatalogUpdatesModelMetadata(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen/go", []model.ChannelModel{{
		Name:               "kept",
		Source:             model.ChannelModelSourceAuto,
		UpstreamProtocol:   model.UpstreamProtocolAnthropic,
		ContextWindow:      200000,
		MaxOutput:          8192,
		Reasoning:          true,
		ToolCall:           true,
		InputModalities:    "text",
		MaxReasoningEffort: "low",
	}})
	restore := stubSyncFetchCatalog(
		[]string{"kept"},
		map[string]helper.OpencodeModelInfo{
			"kept": {
				Protocol:           model.UpstreamProtocolChat,
				ContextWindow:      400000,
				MaxOutput:          16384,
				Reasoning:          false,
				ToolCall:           false,
				InputModalities:    []string{"text", "image"},
				MaxReasoningEffort: "max",
			},
		},
		nil,
	)
	defer restore()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
	got := loadChannelModels(t, channel.ID)["kept"]
	if got.UpstreamProtocol != model.UpstreamProtocolChat {
		t.Fatalf("catalog protocol = %q, want chat", got.UpstreamProtocol)
	}
	if got.ContextWindow != 400000 {
		t.Fatalf("catalog context window = %d, want 400000", got.ContextWindow)
	}
	if got.MaxOutput != 16384 {
		t.Fatalf("catalog max output = %d, want 16384", got.MaxOutput)
	}
	if got.Reasoning || got.ToolCall {
		t.Fatalf("catalog capabilities = reasoning:%v tool_call:%v, want false/false", got.Reasoning, got.ToolCall)
	}
	if got.InputModalities != "text,image" {
		t.Fatalf("catalog input modalities = %q, want text,image", got.InputModalities)
	}
	if got.MaxReasoningEffort != "max" {
		t.Fatalf("catalog max reasoning effort = %q, want max", got.MaxReasoningEffort)
	}
}

func TestSyncModelsTaskSkipsDisabledChannels(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen", []model.ChannelModel{
		{Name: "kept", Source: model.ChannelModelSourceAuto},
	})
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable channel: %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("refresh cache: %v", err)
	}

	prevModels := fetchChannelModels
	fetchChannelModels = func(context.Context, model.Channel) ([]string, error) {
		t.Fatal("disabled channel must not be synced")
		return nil, nil
	}
	defer func() { fetchChannelModels = prevModels }()

	if err := SyncModelsTask(); err != nil {
		t.Fatalf("SyncModelsTask: %v", err)
	}
}

func TestSyncModelsTaskConcurrentReturnsInProgress(t *testing.T) {
	syncModelsMu.Lock()
	defer syncModelsMu.Unlock()

	err := SyncModelsTask()
	if !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("concurrent sync error = %v, want ErrSyncInProgress", err)
	}
}

func TestSyncFailureWarningIgnoresInProgressAndLogsRealError(t *testing.T) {
	channel := insertSyncChannel(t, "https://opencode.ai/zen", nil)
	prevFetch := fetchChannelModels
	fetchChannelModels = func(context.Context, model.Channel) ([]string, error) {
		return nil, errors.New("upstream down")
	}
	defer func() { fetchChannelModels = prevFetch }()

	syncErr := SyncModelsTask()
	if syncErr == nil || errors.Is(syncErr, ErrSyncInProgress) {
		t.Fatalf("sync error = %v, want real failure for channel %s", syncErr, channel.Name)
	}

	var warnings []string
	prevWarn := syncWarnf
	syncWarnf = func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	defer func() { syncWarnf = prevWarn }()

	warnSyncFailure(ErrSyncInProgress)
	if len(warnings) != 0 {
		t.Fatalf("in-progress sync must not warn: %v", warnings)
	}
	warnSyncFailure(syncErr)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "upstream down") {
		t.Fatalf("real sync failure warning = %v", warnings)
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
	catalog := make(map[string]helper.OpencodeModelInfo, len(protocols))
	for name, protocol := range protocols {
		catalog[name] = helper.OpencodeModelInfo{Protocol: protocol}
	}
	return stubSyncFetchCatalog(models, catalog, catalogErr)
}

func stubSyncFetchCatalog(models []string, catalog map[string]helper.OpencodeModelInfo, catalogErr error) func() {
	prevModels := fetchChannelModels
	prevCatalog := fetchOpencodeCatalog
	fetchChannelModels = func(context.Context, model.Channel) ([]string, error) {
		out := append([]string(nil), models...)
		return out, nil
	}
	fetchOpencodeCatalog = func(context.Context, model.Channel) (map[string]helper.OpencodeModelInfo, error) {
		if catalogErr != nil {
			return nil, catalogErr
		}
		return catalog, nil
	}
	return func() {
		fetchChannelModels = prevModels
		fetchOpencodeCatalog = prevCatalog
	}
}

func loadChannelModels(t *testing.T, channelID int) map[string]model.ChannelModel {
	t.Helper()
	var rows []model.ChannelModel
	if err := db.GetDB().Where("channel_id = ?", channelID).Find(&rows).Error; err != nil {
		t.Fatalf("load models: %v", err)
	}
	got := make(map[string]model.ChannelModel, len(rows))
	for _, row := range rows {
		got[row.Name] = row
	}
	return got
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
