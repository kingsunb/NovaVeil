package builtin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
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

// ---------------------------------------------------------------------------
// 免费渠道测试
// ---------------------------------------------------------------------------

func TestEnsureFreeBuiltinChannelsInsertsMissing(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()

	name := fmt.Sprintf("builtin-free-%d", time.Now().UnixNano())
	BuiltinFreeChannels = []model.Channel{testChannel(name)}

	if err := ensureFreeBuiltinChannels(ctx); err != nil {
		t.Fatalf("ensureFreeBuiltinChannels: %v", err)
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

func TestEnsureFreeBuiltinChannelsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	old := BuiltinFreeChannels
	defer func() { BuiltinFreeChannels = old }()

	name := fmt.Sprintf("builtin-idempotent-%d", time.Now().UnixNano())
	channel := testChannel(name)
	BuiltinFreeChannels = []model.Channel{channel}

	if err := ensureFreeBuiltinChannels(ctx); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	var before model.Channel
	if err := db.GetDB().First(&before, "name = ?", name).Error; err != nil {
		t.Fatalf("load before: %v", err)
	}

	if err := ensureFreeBuiltinChannels(ctx); err != nil {
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

func TestEnsureFreeBuiltinChannelsDoesNotOverwriteUserChannel(t *testing.T) {
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
	if err := ensureFreeBuiltinChannels(ctx); err != nil {
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

func TestEnsureFreeBuiltinChannelsClearsCustomFreeFlag(t *testing.T) {
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

	if err := ensureFreeBuiltinChannels(ctx); err != nil {
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

// ---------------------------------------------------------------------------
// 官方渠道测试
// ---------------------------------------------------------------------------

func TestEnsureOfficialBuiltinChannelsInsertsMissing(t *testing.T) {
	ctx := context.Background()
	old := BuiltinOfficialChannels
	defer func() { BuiltinOfficialChannels = old }()

	name := fmt.Sprintf("builtin-official-%d", time.Now().UnixNano())
	BuiltinOfficialChannels = []model.Channel{
		{Name: name, Type: model.ChannelProviderOpenAI, BaseURL: "https://official.example.invalid"},
	}

	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("ensureOfficialBuiltinChannels: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, "name = ?", name).Error; err != nil {
		t.Fatalf("load builtin official channel: %v", err)
	}
	if stored.IsFree {
		t.Fatal("official channel should have IsFree=false")
	}
	if !stored.Builtin {
		t.Fatal("official channel should have Builtin=true")
	}
	if stored.Enabled {
		t.Fatal("official channel should default Enabled=false")
	}
	if stored.Key != "" {
		t.Fatalf("official channel should have empty Key, got %q", stored.Key)
	}
	if stored.AutoSync {
		t.Fatal("official channel should default AutoSync=false")
	}
	var modelCount int64
	if err := db.GetDB().Model(&model.ChannelModel{}).Where("channel_id = ?", stored.ID).Count(&modelCount).Error; err != nil {
		t.Fatalf("count official channel models: %v", err)
	}
	if modelCount != 0 {
		t.Fatalf("official channel models in db = %d, want 0 (user fills in)", modelCount)
	}
}

func TestEnsureOfficialBuiltinChannelsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	old := BuiltinOfficialChannels
	defer func() { BuiltinOfficialChannels = old }()

	name := fmt.Sprintf("official-idempotent-%d", time.Now().UnixNano())
	BuiltinOfficialChannels = []model.Channel{
		{Name: name, Type: model.ChannelProviderOpenAI, BaseURL: "https://official.example.invalid"},
	}

	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	var before model.Channel
	if err := db.GetDB().First(&before, "name = ?", name).Error; err != nil {
		t.Fatalf("load before: %v", err)
	}

	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
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

func TestEnsureOfficialBuiltinChannelsDoesNotOverwriteUserChannel(t *testing.T) {
	ctx := context.Background()
	old := BuiltinOfficialChannels
	defer func() { BuiltinOfficialChannels = old }()

	name := fmt.Sprintf("custom-owns-official-name-%d", time.Now().UnixNano())
	custom := model.Channel{
		Name:    name,
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		IsFree:  false,
		Builtin: false,
		BaseURL: "https://custom.example.invalid",
		Key:     "sk-user-secret",
	}
	if err := db.GetDB().Create(&custom).Error; err != nil {
		t.Fatalf("create custom channel: %v", err)
	}

	BuiltinOfficialChannels = []model.Channel{
		{Name: name, Type: model.ChannelProviderOpenAI, BaseURL: "https://official.example.invalid"},
	}
	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, custom.ID).Error; err != nil {
		t.Fatalf("reload custom channel: %v", err)
	}
	if stored.Builtin || stored.IsFree {
		t.Fatalf("official seed must not overwrite user channel: Builtin=%v IsFree=%v", stored.Builtin, stored.IsFree)
	}
	if stored.Key != "sk-user-secret" {
		t.Fatalf("user Key should be preserved, got %q", stored.Key)
	}
}

func TestEnsureOfficialBuiltinChannelsDoesNotOverwriteConfiguredBuiltin(t *testing.T) {
	ctx := context.Background()
	old := BuiltinOfficialChannels
	defer func() { BuiltinOfficialChannels = old }()

	// 模拟管理员已通过编辑器给官方渠道填入 Key、启用、同步了模型。
	name := fmt.Sprintf("official-user-configured-%d", time.Now().UnixNano())
	BuiltinOfficialChannels = []model.Channel{
		{Name: name, Type: model.ChannelProviderOpenAI, BaseURL: "https://official.example.invalid"},
	}
	// 先补建
	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	// 模拟管理员配置：填 Key、启用、加模型
	var stored model.Channel
	if err := db.GetDB().First(&stored, "name = ?", name).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	stored.Key = "sk-configured"
	stored.Enabled = true
	stored.AutoSync = true
	stored.Models = []model.ChannelModel{{Name: "gpt-test", Source: model.ChannelModelSourceAuto}}
	if err := db.GetDB().Save(&stored).Error; err != nil {
		t.Fatalf("update channel: %v", err)
	}

	// 再次 Ensure：不应覆盖管理员的配置
	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	var after model.Channel
	if err := db.GetDB().First(&after, "name = ?", name).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if after.Key != "sk-configured" {
		t.Fatalf("user-configured Key should be preserved, got %q", after.Key)
	}
	if !after.Enabled {
		t.Fatal("user-configured Enabled should be preserved")
	}
	if !after.AutoSync {
		t.Fatal("user-configured AutoSync should be preserved")
	}
}

func TestBuiltinOfficialChannelsDefinitions(t *testing.T) {
	if len(BuiltinOfficialChannels) == 0 {
		t.Fatal("BuiltinOfficialChannels should contain at least one official provider")
	}
	seen := make(map[string]bool, len(BuiltinOfficialChannels))
	for i, def := range BuiltinOfficialChannels {
		if def.Name == "" {
			t.Fatalf("entry %d missing Name", i)
		}
		if seen[def.Name] {
			t.Fatalf("duplicate builtin official channel name %q", def.Name)
		}
		seen[def.Name] = true
		if def.Type == "" {
			t.Fatalf("builtin official channel %q missing Type", def.Name)
		}
		if def.BaseURL == "" {
			t.Fatalf("builtin official channel %q missing BaseURL", def.Name)
		}
		// 官方渠道定义不应预置 Key（用户自行输入）
		if def.Key != "" {
			t.Fatalf("builtin official channel %q should not ship with a Key", def.Name)
		}
		// 官方渠道定义不应预置模型（用户自行拉取）
		if len(def.Models) != 0 {
			t.Fatalf("builtin official channel %q should not ship with Models", def.Name)
		}
		// 官方渠道不应与免费渠道重名
		for _, free := range BuiltinFreeChannels {
			if def.Name == free.Name {
				t.Fatalf("official channel %q conflicts with free channel name", def.Name)
			}
		}
	}
}

func TestOpenCodeFreeSeedProtocolsAreVerified(t *testing.T) {
	var free *model.Channel
	for i := range BuiltinFreeChannels {
		if BuiltinFreeChannels[i].Name == "OpenCode Free" {
			free = &BuiltinFreeChannels[i]
			break
		}
	}
	if free == nil {
		t.Fatal("BuiltinFreeChannels should contain OpenCode Free")
	}
	seen := make(map[string]bool, len(free.Models))
	for _, channelModel := range free.Models {
		seen[channelModel.Name] = true
		want, ok := model.OpencodeZenSeedProtocols[channelModel.Name]
		if !ok {
			if channelModel.UpstreamProtocol != "" {
				t.Fatalf("unverified model %s has protocol %q", channelModel.Name, channelModel.UpstreamProtocol)
			}
			continue
		}
		if channelModel.UpstreamProtocol != want {
			t.Fatalf("model %s protocol = %q, want %q", channelModel.Name, channelModel.UpstreamProtocol, want)
		}
	}
	for name := range model.OpencodeZenSeedProtocols {
		if !seen[name] {
			t.Fatalf("verified model %s missing from OpenCode Free seed", name)
		}
	}
	for _, official := range BuiltinOfficialChannels {
		if official.Name == "OpenCode" && len(official.Models) != 0 {
			t.Fatal("official OpenCode channel should not ship a model list")
		}
	}
}

func TestFreeChannelFromDefinitionKeepsUpstreamProtocol(t *testing.T) {
	got := freeChannelFromDefinition(model.Channel{
		Name:    "protocol-copy",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://opencode.ai/zen",
		Models: []model.ChannelModel{{
			Name:             "deepseek-v4-flash-free",
			UpstreamProtocol: model.UpstreamProtocolChat,
		}},
	})
	if len(got.Models) != 1 {
		t.Fatalf("models = %d", len(got.Models))
	}
	if got.Models[0].UpstreamProtocol != model.UpstreamProtocolChat {
		t.Fatalf("protocol = %q", got.Models[0].UpstreamProtocol)
	}
	if got.Models[0].Source != model.ChannelModelSourceManual {
		t.Fatalf("source = %q", got.Models[0].Source)
	}
}

func TestBackfillOpencodeUpstreamProtocolsDoesNotOverwrite(t *testing.T) {
	suffix := time.Now().UnixNano()
	zen := model.Channel{
		Name:           fmt.Sprintf("backfill-zen-%d", suffix),
		Type:           model.ChannelProviderOpenAI,
		Enabled:        true,
		Builtin:        true,
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/zen",
		Key:            "public-key",
		Models: []model.ChannelModel{
			{Name: "deepseek-v4-flash-free", Source: model.ChannelModelSourceManual},
			{Name: "hy3-free", Source: model.ChannelModelSourceManual, UpstreamProtocol: model.UpstreamProtocolAnthropic},
			{Name: "not-in-catalog", Source: model.ChannelModelSourceManual},
		},
	}
	goChannel := model.Channel{
		Name:           fmt.Sprintf("backfill-go-%d", suffix),
		Type:           model.ChannelProviderOpenAI,
		Enabled:        false,
		Builtin:        true,
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/zen/go",
		Key:            "go-key",
		Models: []model.ChannelModel{
			{Name: "deepseek-v4-flash-free", Source: model.ChannelModelSourceAuto},
		},
	}
	custom := model.Channel{
		Name:           fmt.Sprintf("backfill-custom-%d", suffix),
		Type:           model.ChannelProviderAnthropic,
		Enabled:        true,
		Builtin:        false,
		OpencodeCompat: true,
		BaseURL:        "https://opencode.ai/zen",
		Key:            "custom-key",
		Models: []model.ChannelModel{
			{Name: "deepseek-v4-flash-free", Source: model.ChannelModelSourceManual},
		},
	}
	for _, channel := range []*model.Channel{&zen, &goChannel, &custom} {
		if err := db.GetDB().Create(channel).Error; err != nil {
			t.Fatalf("create %s: %v", channel.Name, err)
		}
	}

	if err := BackfillOpencodeUpstreamProtocols(db.GetDB()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	zenModels := protocolsByName(t, zen.ID)
	if zenModels["deepseek-v4-flash-free"] != model.UpstreamProtocolChat {
		t.Fatalf("empty zen protocol = %q, want chat", zenModels["deepseek-v4-flash-free"])
	}
	if zenModels["hy3-free"] != model.UpstreamProtocolAnthropic {
		t.Fatalf("non-empty protocol overwritten: %q", zenModels["hy3-free"])
	}
	if zenModels["not-in-catalog"] != "" {
		t.Fatalf("unverified model filled: %q", zenModels["not-in-catalog"])
	}
	if protocolsByName(t, goChannel.ID)["deepseek-v4-flash-free"] != "" {
		t.Fatal("go tier must not receive the zen seed protocol")
	}
	if protocolsByName(t, custom.ID)["deepseek-v4-flash-free"] != "" {
		t.Fatal("non-builtin channel must not be backfilled")
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, zen.ID).Error; err != nil {
		t.Fatalf("reload zen: %v", err)
	}
	if stored.Type != model.ChannelProviderOpenAI || stored.Key != "public-key" || !stored.Enabled {
		t.Fatalf("backfill changed channel identity: type=%s key=%q enabled=%v", stored.Type, stored.Key, stored.Enabled)
	}
	var count int64
	if err := db.GetDB().Model(&model.ChannelModel{}).Where("channel_id = ?", zen.ID).Count(&count).Error; err != nil {
		t.Fatalf("count models: %v", err)
	}
	if count != 3 {
		t.Fatalf("model count = %d, want 3", count)
	}

	if err := db.GetDB().Model(&model.ChannelModel{}).
		Where("channel_id = ? AND name = ?", zen.ID, "deepseek-v4-flash-free").
		Update("upstream_protocol", model.UpstreamProtocolResponses).Error; err != nil {
		t.Fatalf("set admin protocol: %v", err)
	}
	if err := BackfillOpencodeUpstreamProtocols(db.GetDB()); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if protocolsByName(t, zen.ID)["deepseek-v4-flash-free"] != model.UpstreamProtocolResponses {
		t.Fatal("second backfill overwrote a non-empty protocol")
	}
}

func protocolsByName(t *testing.T, channelID int) map[string]string {
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

func TestBuiltinOfficialOpenCodeHasOpencodeCompat(t *testing.T) {
	// OpenCode 官方渠道与免费版一样需要 OpencodeCompat 和 x-opencode-client /
	// User-Agent 请求头，确保上游 opencode.ai/zen/go 不因缺少会话头拒绝请求。
	var opencode *model.Channel
	for i := range BuiltinOfficialChannels {
		if BuiltinOfficialChannels[i].Name == "OpenCode" {
			opencode = &BuiltinOfficialChannels[i]
			break
		}
	}
	if opencode == nil {
		t.Fatal("BuiltinOfficialChannels should contain an OpenCode entry")
	}
	if !opencode.OpencodeCompat {
		t.Fatal("OpenCode official channel should have OpencodeCompat=true")
	}
	seen := make(map[string]string)
	for _, h := range opencode.CustomHeader {
		seen[h.HeaderKey] = h.HeaderValue
	}
	if seen["x-opencode-client"] == "" {
		t.Fatal("OpenCode official channel should ship x-opencode-client header")
	}
	if seen["User-Agent"] == "" {
		t.Fatal("OpenCode official channel should ship User-Agent header")
	}
}

func TestEnsureOfficialBuiltinChannelsPreservesOpencodeCompat(t *testing.T) {
	ctx := context.Background()
	old := BuiltinOfficialChannels
	defer func() { BuiltinOfficialChannels = old }()

	name := fmt.Sprintf("official-opencode-%d", time.Now().UnixNano())
	BuiltinOfficialChannels = []model.Channel{
		{
			Name:           name,
			Type:           model.ChannelProviderOpenAI,
			BaseURL:        "https://opencode.example.invalid",
			OpencodeCompat: true,
			CustomHeader: []model.CustomHeader{
				{HeaderKey: "x-opencode-client", HeaderValue: "desktop"},
			},
		},
	}
	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		t.Fatalf("ensureOfficialBuiltinChannels: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, "name = ?", name).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	if !stored.OpencodeCompat {
		t.Fatal("official channel should preserve OpencodeCompat=true from definition")
	}
	if len(stored.CustomHeader) != 1 || stored.CustomHeader[0].HeaderKey != "x-opencode-client" {
		t.Fatalf("official channel should preserve custom headers, got %+v", stored.CustomHeader)
	}
	if !seal.IsSealed(stored.CustomHeader[0].HeaderValue) {
		t.Fatalf("builtin header value must be sealed, got %q", stored.CustomHeader[0].HeaderValue)
	}
	opened, err := seal.Open(stored.CustomHeader[0].HeaderValue)
	if err != nil || opened != "desktop" {
		t.Fatalf("opened builtin header = %q, %v", opened, err)
	}
}

// ---------------------------------------------------------------------------
// 合并入口测试
// ---------------------------------------------------------------------------

func TestEnsureBuiltinChannelsPreservesOfficialIsFreeFalse(t *testing.T) {
	ctx := context.Background()
	oldFree := BuiltinFreeChannels
	oldOfficial := BuiltinOfficialChannels
	defer func() {
		BuiltinFreeChannels = oldFree
		BuiltinOfficialChannels = oldOfficial
	}()

	freeName := fmt.Sprintf("ensure-all-free-%d", time.Now().UnixNano())
	officialName := fmt.Sprintf("ensure-all-official-%d", time.Now().UnixNano())
	BuiltinFreeChannels = []model.Channel{testChannel(freeName)}
	BuiltinOfficialChannels = []model.Channel{
		{Name: officialName, Type: model.ChannelProviderOpenAI, BaseURL: "https://official.example.invalid"},
	}

	if err := EnsureBuiltinChannels(ctx); err != nil {
		t.Fatalf("EnsureBuiltinChannels: %v", err)
	}

	var free model.Channel
	if err := db.GetDB().First(&free, "name = ?", freeName).Error; err != nil {
		t.Fatalf("load free: %v", err)
	}
	if !free.IsFree || !free.Builtin || !free.Enabled {
		t.Fatalf("free channel wrong: IsFree=%v Builtin=%v Enabled=%v", free.IsFree, free.Builtin, free.Enabled)
	}

	var official model.Channel
	if err := db.GetDB().First(&official, "name = ?", officialName).Error; err != nil {
		t.Fatalf("load official: %v", err)
	}
	if official.IsFree || !official.Builtin || official.Enabled {
		t.Fatalf("official channel wrong: IsFree=%v Builtin=%v Enabled=%v", official.IsFree, official.Builtin, official.Enabled)
	}
}
