package op

import (
	"context"
	"errors"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
)

// cleanupImportTestRows 删除导入测试使用的高 ID 段行, 避免跨测试污染。
func cleanupImportTestRows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	gorm := db.GetDB().WithContext(ctx)
	gorm.Where("id >= 920000 AND id < 930000").Delete(&model.ChannelModel{})
	gorm.Where("id >= 920000 AND id < 930000").Delete(&model.GroupItem{})
	gorm.Where("id >= 920000 AND id < 930000").Delete(&model.Group{})
	gorm.Where("id >= 920000 AND id < 930000").Delete(&model.Channel{})
	gorm.Where("id >= 920000 AND id < 930000").Delete(&model.APIKey{})
}

// TestImportEmptyDB 验证向无渠道/分组的库导入正常落库。
func TestImportEmptyDB(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{
			{ID: 920001, Name: "sta06-empty-channel", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"},
		},
		ChannelModels: []model.ChannelModel{
			{ID: 920101, ChannelID: 920001, Name: "model-empty"},
		},
	}

	result, err := DBImportIncremental(ctx, dump)
	if err != nil {
		t.Fatalf("DBImportIncremental: %v", err)
	}
	if result.RowsAffected["channels"] != 1 {
		t.Fatalf("channels rows affected = %d, want 1", result.RowsAffected["channels"])
	}
	if result.RowsAffected["channel_models"] != 1 {
		t.Fatalf("channel_models rows affected = %d, want 1", result.RowsAffected["channel_models"])
	}

	var ch model.Channel
	if err := db.GetDB().First(&ch, 920001).Error; err != nil {
		t.Fatalf("channel not inserted: %v", err)
	}
	if ch.Name != "sta06-empty-channel" {
		t.Fatalf("channel name = %q, want sta06-empty-channel", ch.Name)
	}
}

// TestImportRepeatSameDump 验证同库重复导入全部跳过(DO NOTHING), 不产生重复或覆盖。
func TestImportRepeatSameDump(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{
			{ID: 920001, Name: "sta06-repeat-channel", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"},
		},
		ChannelModels: []model.ChannelModel{
			{ID: 920101, ChannelID: 920001, Name: "model-repeat"},
		},
	}

	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// 第二次导入相同 dump: 全部 DO NOTHING。
	result2, err := DBImportIncremental(ctx, dump)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if result2.RowsAffected["channels"] != 0 {
		t.Fatalf("repeat channels rows affected = %d, want 0", result2.RowsAffected["channels"])
	}
	if result2.RowsAffected["channel_models"] != 0 {
		t.Fatalf("repeat channel_models rows affected = %d, want 0", result2.RowsAffected["channel_models"])
	}
}

// TestImportForeignSameID 是 STA-06 核心修复验证:
// 当前库 channel id=1 是 A, 外库相同 id 是 B; B 渠道被跳过(DO NOTHING),
// 且外库同 ID 的 channel_model 也必须跳过, 不能覆盖 A 的实际模型/路由。
func TestImportForeignSameID(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	// 当前库: 渠道 A + 模型 model-a
	localA := model.Channel{ID: 920001, Name: "sta06-local-A", Type: model.ChannelProviderOpenAI, BaseURL: "https://a.invalid"}
	localModel := model.ChannelModel{ID: 920101, ChannelID: 920001, Name: "model-a"}
	if err := db.GetDB().Create(&localA).Error; err != nil {
		t.Fatalf("seed local channel A: %v", err)
	}
	if err := db.GetDB().Create(&localModel).Error; err != nil {
		t.Fatalf("seed local model: %v", err)
	}

	// 外库 dump: 同 ID 渠道 B + 同 ID 模型 model-b
	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{
			{ID: 920001, Name: "sta06-foreign-B", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"},
		},
		ChannelModels: []model.ChannelModel{
			{ID: 920101, ChannelID: 920001, Name: "model-b"},
		},
	}

	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatalf("import foreign dump: %v", err)
	}

	// 渠道 A 必须保持不变。
	var ch model.Channel
	if err := db.GetDB().First(&ch, 920001).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if ch.Name != "sta06-local-A" {
		t.Fatalf("channel name = %q, want sta06-local-A (DO NOTHING on PK)", ch.Name)
	}
	if ch.BaseURL != "https://a.invalid" {
		t.Fatalf("channel base_url = %q, want https://a.invalid", ch.BaseURL)
	}

	// channel_model 必须保持 model-a, 不能被 model-b 覆盖。
	var cm model.ChannelModel
	if err := db.GetDB().First(&cm, 920101).Error; err != nil {
		t.Fatalf("read channel model: %v", err)
	}
	if cm.Name != "model-a" {
		t.Fatalf("channel_model name = %q, want model-a (DO NOTHING, not UpdateAll)", cm.Name)
	}
}

// TestImportInvalidSettings 验证导入设置执行与正常写接口相同的校验, 非法值被拒绝。
func TestImportInvalidSettings(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Settings: []model.Setting{
			{Key: model.SettingKeySyncLLMInterval, Value: "0"}, // < 1, 非法
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with invalid sync_llm_interval=0 should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
	if len(valErr.Preview.SettingsIssues) == 0 {
		t.Fatal("preview should report settings issues")
	}
}

// TestImportInvalidSettingsUpperBound 验证 sync_llm_interval 超上限被拒绝。
func TestImportInvalidSettingsUpperBound(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Settings: []model.Setting{
			{Key: model.SettingKeySyncLLMInterval, Value: "999999"}, // > 8760, 非法
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with sync_llm_interval=999999 should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
}

// TestImportMissingChannelRef 验证渠道模型引用不存在的渠道时被拒绝。
func TestImportMissingChannelRef(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		ChannelModels: []model.ChannelModel{
			{ID: 920301, ChannelID: 920399, Name: "orphan-model"}, // 920399 不存在
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with orphan channel_model should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
	if len(valErr.Preview.InvalidRefs) == 0 {
		t.Fatal("preview should report invalid references")
	}
}

// TestImportMissingGroupItemRef 验证分组成员引用不存在的渠道模型/分组时被拒绝。
func TestImportMissingGroupItemRef(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Groups: []model.Group{
			{ID: 920010, Name: "sta06-missing-ref-group", Mode: model.GroupModeFailover},
		},
		GroupItems: []model.GroupItem{
			{ID: 920020, GroupID: 920010, ChannelModelID: 920399, Priority: 1}, // 920399 不存在
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with missing channel_model ref should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
}

// TestImportCircularGroupRef 验证分组引用循环被检测并拒绝。
func TestImportCircularGroupRef(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Groups: []model.Group{
			{ID: 920010, Name: "sta06-cycle-x", Mode: model.GroupModeFailover},
			{ID: 920011, Name: "sta06-cycle-y", Mode: model.GroupModeFailover},
		},
		GroupItems: []model.GroupItem{
			{ID: 920020, GroupID: 920010, RefGroupName: "sta06-cycle-y", Priority: 1},
			{ID: 920021, GroupID: 920011, RefGroupName: "sta06-cycle-x", Priority: 1},
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with circular group ref should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
	if len(valErr.Preview.Cycles) == 0 {
		t.Fatal("preview should detect reference cycle")
	}
}

// TestImportSelfReferencingGroup 验证分组自引用被检测并拒绝。
func TestImportSelfReferencingGroup(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Groups: []model.Group{
			{ID: 920010, Name: "sta06-self-ref", Mode: model.GroupModeFailover},
		},
		GroupItems: []model.GroupItem{
			{ID: 920020, GroupID: 920010, RefGroupName: "sta06-self-ref", Priority: 1},
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with self-referencing group should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
	if len(valErr.Preview.Cycles) == 0 {
		t.Fatal("preview should detect self-reference cycle")
	}
}

// TestImportTransactionAbort 验证校验失败时事务回滚, 已验证通过的行也不落库。
func TestImportTransactionAbort(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{
			{ID: 920401, Name: "sta06-txn-valid-channel", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"},
		},
		Settings: []model.Setting{
			{Key: model.SettingKeySyncLLMInterval, Value: "0"}, // 非法, 触发回滚
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with invalid setting should fail")
	}

	// 事务回滚: 合法渠道也不应落库。
	var ch model.Channel
	if err := db.GetDB().First(&ch, 920401).Error; err == nil {
		t.Fatal("channel should NOT be inserted after transaction rollback")
	}
}

// TestImportDuplicateAPIKey 验证 dump 内重复 API key 明文被检测并拒绝。
func TestImportDuplicateAPIKey(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		APIKeys: []model.APIKey{
			{ID: 920501, Name: "key-a", APIKey: "sk-dup-value-sta06", Enabled: true},
			{ID: 920502, Name: "key-b", APIKey: "sk-dup-value-sta06", Enabled: true}, // 重复明文
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with duplicate API key value should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
}

// TestImportRejectsRedactedAPIKey 验证导出脱敏后的 "****" API Key 不会被当作
// 可用密钥导入: 预检应报告 InvalidRef 并返回 DBImportValidationError。
func TestImportRejectsRedactedAPIKey(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		APIKeys: []model.APIKey{
			{ID: 920511, Name: "redacted-key", APIKey: "****", Enabled: true},
		},
	}

	_, err := DBImportIncremental(ctx, dump)
	if err == nil {
		t.Fatal("import with redacted API key should fail")
	}
	var valErr *DBImportValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error should be DBImportValidationError, got %T: %v", err, err)
	}
	found := false
	for _, ref := range valErr.Preview.InvalidRefs {
		if ref.Table == "api_keys" && ref.ID == 920511 {
			found = true
		}
	}
	if !found {
		t.Fatalf("preview should report redacted api_keys row, got %+v", valErr.Preview.InvalidRefs)
	}
}

// TestImportRejectsRedactedChannelCredentials 验证渠道 Key 与多 Key 上的精确 "****"
// 不能导入。代理和自定义头的 "****" 是现行导出打码, 不拒绝整份备份, 也不写回。
func TestImportRejectsRedactedChannelCredentials(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })
	mask := "****"
	cases := []model.Channel{
		{ID: 920611, Name: "redacted-key", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com", Key: mask},
		{ID: 920612, Name: "redacted-keys", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com", Keys: []model.ChannelKey{{Key: mask}}},
	}
	for _, ch := range cases {
		dump := &model.DBDump{Version: dbDumpVersion, Channels: []model.Channel{ch}}
		_, err := DBImportIncremental(ctx, dump)
		if err == nil {
			t.Fatalf("import of channel %d should fail", ch.ID)
		}
		var valErr *DBImportValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("channel %d: error should be DBImportValidationError, got %T: %v", ch.ID, err, err)
		}
		found := false
		for _, ref := range valErr.Preview.InvalidRefs {
			if ref.Table == "channels" && ref.ID == ch.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("channel %d: preview missing invalid ref: %+v", ch.ID, valErr.Preview.InvalidRefs)
		}
		var stored model.Channel
		if err := db.GetDB().First(&stored, ch.ID).Error; err == nil {
			t.Fatalf("channel %d must not be inserted", ch.ID)
		}
	}
}

func TestImportDropsRedactedProxyAndCustomHeader(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })
	mask := "****"
	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{{
			ID:           920613,
			Name:         "redacted-proxy",
			Type:         model.ChannelProviderOpenAI,
			BaseURL:      "https://example.com",
			Key:          "sk-real-but-proxy-masked",
			ChannelProxy: &mask,
			CustomHeader: []model.CustomHeader{{HeaderKey: "X-Token", HeaderValue: mask}},
		}},
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatalf("redacted proxy must not block key import: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().First(&stored, 920613).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	plain, err := seal.Open(stored.Key)
	if err != nil || plain != "sk-real-but-proxy-masked" {
		t.Fatalf("key = %q err=%v", plain, err)
	}
	if stored.ChannelProxy != nil {
		t.Fatalf("redacted proxy must be omitted, got %q", *stored.ChannelProxy)
	}
	if len(stored.CustomHeader) != 0 {
		t.Fatalf("redacted custom header must be omitted, got %+v", stored.CustomHeader)
	}
}

// TestImportSealsCustomHeaderAndAllowsPrivateProxy 验证导入会加密自定义头,
// 且内网 channel_proxy 不走 BaseURL 的出口拒绝。
func TestImportSealsCustomHeaderAndAllowsPrivateProxy(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })
	proxy := "http://alice:s3cret@10.2.3.4:7890"
	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{{
			ID:           920621,
			Name:         "import-private-proxy",
			Type:         model.ChannelProviderOpenAI,
			BaseURL:      "https://example.com",
			Key:          "sk-import-header-secret",
			ChannelProxy: &proxy,
			CustomHeader: []model.CustomHeader{{HeaderKey: "X-Token", HeaderValue: "header-import-secret"}},
		}},
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatalf("import private proxy: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().First(&stored, 920621).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if !seal.IsSealed(stored.Key) || !seal.IsSealed(stored.CustomHeader[0].HeaderValue) {
		t.Fatalf("key/header not sealed: key=%q header=%q", stored.Key, stored.CustomHeader[0].HeaderValue)
	}
	if stored.ChannelProxy == nil || !seal.IsSealed(*stored.ChannelProxy) {
		t.Fatalf("proxy not sealed: %v", stored.ChannelProxy)
	}
	openedProxy, err := seal.Open(*stored.ChannelProxy)
	if err != nil || openedProxy != proxy {
		t.Fatalf("opened proxy = %q, %v", openedProxy, err)
	}
	openedHeader, err := seal.Open(stored.CustomHeader[0].HeaderValue)
	if err != nil || openedHeader != "header-import-secret" {
		t.Fatalf("opened header = %q, %v", openedHeader, err)
	}
	if stringsContainsCredential(stored) {
		t.Fatal("plaintext credential stored")
	}
}

func stringsContainsCredential(ch model.Channel) bool {
	if ch.Key == "sk-import-header-secret" {
		return true
	}
	if len(ch.CustomHeader) > 0 && ch.CustomHeader[0].HeaderValue == "header-import-secret" {
		return true
	}
	return ch.ChannelProxy != nil && *ch.ChannelProxy == "http://alice:s3cret@10.2.3.4:7890"
}

// TestImportPreviewDryRun 验证预检不写入数据, 仅返回统计与校验结果。
func TestImportPreviewDryRun(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		Channels: []model.Channel{
			{ID: 920001, Name: "sta06-preview-channel", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"},
		},
		ChannelModels: []model.ChannelModel{
			{ID: 920101, ChannelID: 920001, Name: "model-preview"},
		},
	}

	preview, err := DBImportPreview(ctx, dump)
	if err != nil {
		t.Fatalf("DBImportPreview: %v", err)
	}
	if !preview.CanImport {
		t.Fatalf("preview should allow import, issues: %v", preview.SettingsIssues)
	}
	if preview.Summary.New["channels"] != 1 {
		t.Fatalf("preview new channels = %d, want 1", preview.Summary.New["channels"])
	}

	// 预检不应写入任何数据。
	var ch model.Channel
	if err := db.GetDB().First(&ch, 920001).Error; err == nil {
		t.Fatal("preview must not write channel to DB")
	}
}

// TestImportPreviewDetectsIssues 验证预检能检测出无效引用并报告 CanImport=false。
func TestImportPreviewDetectsIssues(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	dump := &model.DBDump{
		Version: dbDumpVersion,
		ChannelModels: []model.ChannelModel{
			{ID: 920301, ChannelID: 920399, Name: "orphan"}, // 无效引用
		},
	}

	preview, err := DBImportPreview(ctx, dump)
	if err != nil {
		t.Fatalf("DBImportPreview: %v", err)
	}
	if preview.CanImport {
		t.Fatal("preview should report CanImport=false for invalid refs")
	}
	if len(preview.InvalidRefs) == 0 {
		t.Fatal("preview should report invalid references")
	}
}

// TestExportConsistentSnapshot 验证导出在事务内读取所有表, 返回完整数据。
func TestExportConsistentSnapshot(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupImportTestRows(t) })

	ch := model.Channel{ID: 920001, Name: "sta06-export-channel", Type: model.ChannelProviderOpenAI, BaseURL: "https://example.com"}
	if err := db.GetDB().Create(&ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	cm := model.ChannelModel{ID: 920101, ChannelID: 920001, Name: "model-export"}
	if err := db.GetDB().Create(&cm).Error; err != nil {
		t.Fatalf("seed channel model: %v", err)
	}

	dump, err := DBExportAll(ctx)
	if err != nil {
		t.Fatalf("DBExportAll: %v", err)
	}

	foundCh, foundCM := false, false
	for _, c := range dump.Channels {
		if c.ID == 920001 {
			foundCh = true
		}
	}
	for _, m := range dump.ChannelModels {
		if m.ID == 920101 {
			foundCM = true
		}
	}
	if !foundCh {
		t.Fatal("export must include seeded channel")
	}
	if !foundCM {
		t.Fatal("export must include seeded channel model")
	}
}
