package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeleteOrphanOpencodeProxyAccounts(t *testing.T) {
	dsn := fmt.Sprintf("file:migrate033-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Exec(`CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}
	if err := database.AutoMigrate(&model.OpencodeProxyAccount{}); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	if err := database.Exec(`INSERT INTO channels(id, name) VALUES(1, 'OpenCode Free V2')`).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	for _, channelID := range []int{1, 2} {
		if err := database.Exec(
			`INSERT INTO opencode_proxy_accounts(channel_id, key_id, account, last_success_at, success_count)
			 VALUES(?, '', ?, ?, 0)`,
			channelID, fmt.Sprintf("account-%d", channelID), time.Now()).Error; err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	if err := deleteOrphanOpencodeProxyAccounts(database); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var accounts []string
	if err := database.Raw("SELECT channel_id || ':' || account FROM opencode_proxy_accounts ORDER BY channel_id").
		Scan(&accounts).Error; err != nil {
		t.Fatalf("load accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0] != "1:account-1" {
		t.Fatalf("accounts = %v, want [1:account-1]", accounts)
	}
}
