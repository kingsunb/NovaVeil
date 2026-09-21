package op

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestDBExportAllCredentialAudit 复核备份导出的凭据面:
// users 表(含 bcrypt 密码哈希)不得出现; 渠道 Key 与 API Key 以明文导出, 便于还原。
// 导出文件头部必须写明含有明文 Key。
func TestDBExportAllCredentialAudit(t *testing.T) {
	ctx := context.Background()

	const (
		channelID  = 910001
		apiKeyID   = 910002
		markerKey  = "sk-channel-secret-marker-910001"
		markerUser = "sk-user-key-marker-910002"
	)

	leakUser := model.User{Username: "backup-audit-user", Password: "audit-password-910001"}
	if err := leakUser.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	bcryptHash := leakUser.Password

	channel := model.Channel{ID: channelID, Name: "backup-audit-channel", Type: model.ChannelProviderOpenAI, Enabled: true, BaseURL: "https://example.invalid", Key: markerKey}
	apiKey := model.APIKey{ID: apiKeyID, Name: "backup-audit-key", APIKey: markerUser, Enabled: true}

	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channelID)
		db.GetDB().Delete(&model.APIKey{}, apiKeyID)
		db.GetDB().Where("username = ?", leakUser.Username).Delete(&model.User{})
	})
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := db.GetDB().Create(&apiKey).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.GetDB().Create(&leakUser).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	dump, err := DBExportAll(ctx)
	if err != nil {
		t.Fatalf("DBExportAll: %v", err)
	}
	payload, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	exported := string(payload)

	// 用户凭据不得泄漏: 无 users 表痕迹, 无 password 字段, 无 bcrypt 哈希。
	if strings.Contains(exported, `"users"`) {
		t.Fatal("export must not contain a users table")
	}
	if strings.Contains(exported, "password") {
		t.Fatal("export must not contain any password field")
	}
	if strings.Contains(exported, bcryptHash) {
		t.Fatal("export must not contain user bcrypt hash")
	}
	foundChannelKey, foundAPIKey := false, false
	for _, ch := range dump.Channels {
		if ch.ID == channelID {
			foundChannelKey = ch.Key == markerKey
		}
	}
	for _, ak := range dump.APIKeys {
		if ak.ID == apiKeyID {
			foundAPIKey = ak.APIKey == markerUser
		}
	}
	if !foundChannelKey {
		t.Fatal("channel key must be plaintext in export")
	}
	if !foundAPIKey {
		t.Fatal("api key must be plaintext in export")
	}

	// 敏感信息提示必须写入导出文件头部 note 字段, 并说明 Key 是明文。
	if dump.Note == "" {
		t.Fatal("export must carry sensitivity note in header field")
	}
	if !strings.Contains(dump.Note, "明文") || !strings.Contains(dump.Note, "API Key") || !strings.Contains(dump.Note, "渠道 Key") {
		t.Fatalf("sensitivity note must mention plaintext keys, got %q", dump.Note)
	}
	if !strings.Contains(exported, `"note"`) {
		t.Fatal("note field missing from serialized export")
	}
}

// TestDBExportImportClientStatsUsageBuckets 验证客户端调用统计与用量汇总
// 参与完整备份导出/导入: 换平台后趋势图与防滥用审计数据不丢。
func TestDBExportImportClientStatsUsageBuckets(t *testing.T) {
	ctx := context.Background()

	// 种子数据: 1 条客户端统计 + 1 条用量汇总
	cs := model.ClientStat{
		IP:           "203.0.113.42",
		RequestCount: 128,
	}
	ub := model.UsageBucket{
		ID:           920001,
		ModelName:    "gpt-4o",
		InputTokens:  50000,
		OutputTokens: 30000,
		RequestCount: 100,
	}

	// 全库导出会把 header_templates 的头值打成 **** 再 upsert 回来。
	// 测完把缓存里的明文写回, 避免后续用例读到打码后的模板。
	originalTemplates, _ := SettingGetString(model.SettingKeyHeaderTemplates)
	t.Cleanup(func() {
		db.GetDB().Where("ip = ?", cs.IP).Delete(&model.ClientStat{})
		db.GetDB().Where("id = ?", ub.ID).Delete(&model.UsageBucket{})
		if originalTemplates != "" {
			settingCache.Set(model.SettingKeyHeaderTemplates, "")
			_ = SettingSetString(model.SettingKeyHeaderTemplates, originalTemplates)
		}
	})
	if err := db.GetDB().Create(&cs).Error; err != nil {
		t.Fatalf("seed client stat: %v", err)
	}
	if err := db.GetDB().Create(&ub).Error; err != nil {
		t.Fatalf("seed usage bucket: %v", err)
	}

	// 导出
	dump, err := DBExportAll(ctx)
	if err != nil {
		t.Fatalf("DBExportAll: %v", err)
	}

	// 验证导出包含种子数据
	foundCS, foundUB := false, false
	for _, c := range dump.ClientStats {
		if c.IP == cs.IP && c.RequestCount == 128 {
			foundCS = true
		}
	}
	for _, u := range dump.UsageBuckets {
		if u.ID == ub.ID && u.ModelName == "gpt-4o" && u.InputTokens == 50000 {
			foundUB = true
		}
	}
	if !foundCS {
		t.Fatal("export must contain client_stats with seeded data")
	}
	if !foundUB {
		t.Fatal("export must contain usage_buckets with seeded data")
	}

	// 清库后导入, 验证统计表往返。代理仍是 ****, 先清掉以免凭据拒绝打断本用例。
	// 渠道 Key 与 API Key 已是明文, 不改写。
	for i := range dump.Channels {
		if dump.Channels[i].ChannelProxy != nil && *dump.Channels[i].ChannelProxy == "****" {
			dump.Channels[i].ChannelProxy = nil
		}
	}
	db.GetDB().Where("ip = ?", cs.IP).Delete(&model.ClientStat{})
	db.GetDB().Where("id = ?", ub.ID).Delete(&model.UsageBucket{})

	result, err := DBImportIncremental(ctx, dump)
	if err != nil {
		t.Fatalf("DBImportIncremental: %v", err)
	}
	if result.RowsAffected["client_stats"] < 1 {
		t.Fatalf("expected client_stats import rows >= 1, got %d", result.RowsAffected["client_stats"])
	}
	if result.RowsAffected["usage_buckets"] < 1 {
		t.Fatalf("expected usage_buckets import rows >= 1, got %d", result.RowsAffected["usage_buckets"])
	}

	// 验证导入后数据可读
	var restoredCS model.ClientStat
	if err := db.GetDB().Where("ip = ?", cs.IP).First(&restoredCS).Error; err != nil {
		t.Fatalf("read restored client stat: %v", err)
	}
	if restoredCS.RequestCount != 128 {
		t.Fatalf("restored client stat request_count = %d, want 128", restoredCS.RequestCount)
	}

	var restoredUB model.UsageBucket
	if err := db.GetDB().Where("id = ?", ub.ID).First(&restoredUB).Error; err != nil {
		t.Fatalf("read restored usage bucket: %v", err)
	}
	if restoredUB.InputTokens != 50000 || restoredUB.ModelName != "gpt-4o" {
		t.Fatalf("restored usage bucket mismatch: %+v", restoredUB)
	}
}
