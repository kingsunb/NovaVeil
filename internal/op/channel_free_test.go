package op

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestUserChannelCannotBeFreeOrBuiltin 验证用户/API 创建的渠道一律是自定义渠道：
// 即使用户请求体携带 is_free=true 或 builtin=true，也会被强制归为付费自定义渠道。
// 免费分类只能由代码内置（internal/builtin）维护。
func TestUserChannelCannotBeFreeOrBuiltin(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:           fmt.Sprintf("user-free-forced-%d", time.Now().UnixNano()),
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://example.invalid",
		IsFree:         true,
		Builtin:        true,
		OpencodeCompat: true,
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load stored channel: %v", err)
	}
	if stored.IsFree {
		t.Fatal("user-created channel should be forced to is_free=false")
	}
	if stored.Builtin {
		t.Fatal("user-created channel should be forced to builtin=false")
	}

	cached, err := ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("ChannelGet: %v", err)
	}
	if cached.IsFree || cached.Builtin {
		t.Fatalf("cache should reflect forced custom channel: IsFree=%v Builtin=%v", cached.IsFree, cached.Builtin)
	}
}

// TestBuiltinChannelCannotBeDeleted 验证内置渠道受删除保护：避免“固定提供商”
// 被误删后直到重启才恢复。
func TestBuiltinChannelCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    fmt.Sprintf("builtin-protected-%d", time.Now().UnixNano()),
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://example.invalid",
		IsFree:  true,
		Builtin: true,
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create builtin channel: %v", err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatalf("refresh channel cache: %v", err)
	}

	if err := ChannelDel(channel.ID, ctx); err == nil {
		t.Fatal("expected deleting builtin channel to fail")
	}
	var count int64
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).Count(&count).Error; err != nil {
		t.Fatalf("count builtin channel: %v", err)
	}
	if count != 1 {
		t.Fatalf("builtin channel should remain after rejected delete, count=%d", count)
	}
}

// TestBuiltinChannelIdentityFieldsLocked 验证内置渠道的身份字段（名称/上游类型/Base URL）
// 不能通过 ChannelUpdate 修改，防止改名后服务重启按原名称重复补建出第二条内置渠道。
func TestBuiltinChannelIdentityFieldsLocked(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("builtin-identity-%d", time.Now().UnixNano())
	channel := model.Channel{
		Name:    name,
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://example.invalid/v1",
		IsFree:  true,
		Builtin: true,
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create builtin channel: %v", err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatalf("refresh channel cache: %v", err)
	}

	renamed := name + "-renamed"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Name: &renamed}, ctx); err == nil {
		t.Fatal("expected renaming builtin channel to fail")
	}
	newType := model.ChannelProviderAnthropic
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Type: &newType}, ctx); err == nil {
		t.Fatal("expected changing builtin channel type to fail")
	}
	newBaseURL := "https://renamed.example.invalid/v1"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, BaseURL: &newBaseURL}, ctx); err == nil {
		t.Fatal("expected changing builtin channel base_url to fail")
	}

	// 个性化字段仍应可改（启停/Key）。
	enabled := true
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Enabled: &enabled}, ctx); err != nil {
		t.Fatalf("expected updating builtin channel enabled to succeed: %v", err)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load builtin channel: %v", err)
	}
	if stored.Name != name {
		t.Fatalf("builtin channel name changed to %q, want %q", stored.Name, name)
	}
	if stored.Type != model.ChannelProviderOpenAI {
		t.Fatalf("builtin channel type changed to %q", stored.Type)
	}
	if stored.BaseURL != "https://example.invalid/v1" {
		t.Fatalf("builtin channel base_url changed to %q", stored.BaseURL)
	}
	if !stored.Enabled {
		t.Fatal("builtin channel enabled should have been updated to true")
	}
}
