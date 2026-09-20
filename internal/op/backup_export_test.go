package op

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestDBExportAllCredentialAudit 复核备份导出的凭据暴露面:
// users 表(含 bcrypt 密码哈希)不得出现在导出中; 渠道 Key、渠道代理与 API Key
// 明文在导出时统一替换为 "****"(SEC-04), 但导出文件头部必须携带敏感信息提示。
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
	if strings.Contains(exported, markerKey) {
		t.Fatal("export must not leak channel plaintext key")
	}
	if strings.Contains(exported, markerUser) {
		t.Fatal("export must not leak API plaintext key")
	}
	// 凭据一律脱敏为 "****", 备份文件中不存在可用上游密钥(审计 SEC-04/S-M5/S-M6)。
	foundChannelKey, foundAPIKey := false, false
	for _, ch := range dump.Channels {
		if ch.ID == channelID {
			foundChannelKey = ch.Key == "****"
		}
	}
	for _, ak := range dump.APIKeys {
		if ak.ID == apiKeyID {
			foundAPIKey = ak.APIKey == "****"
		}
	}
	if !foundChannelKey {
		t.Fatal("channel key must be redacted to **** in export")
	}
	if !foundAPIKey {
		t.Fatal("api key must be redacted to **** in export")
	}

	// 敏感信息提示必须写入导出文件头部 note 字段。
	if dump.Note == "" {
		t.Fatal("export must carry sensitivity note in header field")
	}
	if !strings.Contains(dump.Note, "敏感") || !strings.Contains(dump.Note, "API Key") || !strings.Contains(dump.Note, "渠道 Key") {
		t.Fatalf("sensitivity note must mention plaintext credentials, got %q", dump.Note)
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

	t.Cleanup(func() {
		db.GetDB().Where("ip = ?", cs.IP).Delete(&model.ClientStat{})
		db.GetDB().Where("id = ?", ub.ID).Delete(&model.UsageBucket{})
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

	// 清库后导入, 验证数据还原
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
