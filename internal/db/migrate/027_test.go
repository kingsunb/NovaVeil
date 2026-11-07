package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration027DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate027-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	statement := `CREATE TABLE opencode_proxy_accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL,
		key_id TEXT,
		account TEXT NOT NULL
	)`
	if err := database.Exec(statement).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := database.Exec(`CREATE UNIQUE INDEX idx_oc_proxy_channel_account ON opencode_proxy_accounts(channel_id, account)`).Error; err != nil {
		t.Fatalf("create old index: %v", err)
	}
	return database
}

// TestScopeOpencodeProxyAccountsByKey 锁定迁移 027:
// 存量行回填空 Key、旧唯一索引删除、新索引允许同账号跨 Key 共存。
func TestScopeOpencodeProxyAccountsByKey(t *testing.T) {
	database := openMigration027DB(t)
	if err := database.Exec(
		`INSERT INTO opencode_proxy_accounts (channel_id, key_id, account) VALUES (1, NULL, 'a'), (1, '', 'b')`,
	).Error; err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	if err := scopeOpencodeProxyAccountsByKey(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var row struct{ KeyID string }
	if err := database.Raw(`SELECT key_id FROM opencode_proxy_accounts WHERE account = 'a'`).Scan(&row).Error; err != nil {
		t.Fatalf("read key_id: %v", err)
	}
	if row.KeyID != "" {
		t.Fatalf("null key_id should be backfilled, got %q", row.KeyID)
	}
	hasIndex := func(name string) bool {
		var count int64
		if err := database.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count).Error; err != nil {
			t.Fatalf("read index %s: %v", name, err)
		}
		return count > 0
	}
	if hasIndex("idx_oc_proxy_channel_account") {
		t.Fatal("old unique index should be dropped")
	}
	if !hasIndex("idx_oc_proxy_channel_key_account") {
		t.Fatal("new unique index should be created")
	}

	if err := database.Exec(
		`INSERT INTO opencode_proxy_accounts (channel_id, key_id, account) VALUES (1, 'key-b', 'a')`,
	).Error; err != nil {
		t.Fatalf("same account under another key should be allowed: %v", err)
	}
	if err := database.Exec(
		`INSERT INTO opencode_proxy_accounts (channel_id, key_id, account) VALUES (1, 'key-b', 'a')`,
	).Error; err == nil {
		t.Fatal("same account under the same key should be rejected")
	}

	if err := scopeOpencodeProxyAccountsByKey(database); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
}
