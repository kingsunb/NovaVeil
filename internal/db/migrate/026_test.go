package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration026DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate026-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	statement := `CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT NOT NULL, builtin BOOLEAN NOT NULL, base_url TEXT NOT NULL DEFAULT '', opencode_lane TEXT NOT NULL DEFAULT '')`
	if err := database.Exec(statement).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return database
}

// TestAlignOpencodeFreeLaneChannels 锁定内置免费档渠道的端点与形态:
// /zen 已关闭, 两条渠道都必须指向 /inference, 且形态由 opencode_lane 显式区分。
func TestAlignOpencodeFreeLaneChannels(t *testing.T) {
	database := openMigration026DB(t)
	rows := []struct {
		name     string
		baseURL  string
		builtin  bool
	}{
		{"OpenCode Free", "https://opencode.ai/inference", true},
		{"OpenCode Free Old", "https://opencode.ai/zen", true},
		{"自建渠道", "https://opencode.ai/zen", false},
	}
	for _, row := range rows {
		if err := database.Exec(
			`INSERT INTO channels (name, builtin, base_url, opencode_lane) VALUES (?, ?, ?, '')`,
			row.name, row.builtin, row.baseURL,
		).Error; err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}

	if err := alignOpencodeFreeLaneChannels(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type channelRow struct {
		BaseURL      string
		OpencodeLane string
	}
	read := func(name string) channelRow {
		var out channelRow
		if err := database.Raw(`SELECT base_url, opencode_lane FROM channels WHERE name = ?`, name).Scan(&out).Error; err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return out
	}

	if got := read("OpenCode Free"); got.BaseURL != "https://opencode.ai/inference" || got.OpencodeLane != "v2" {
		t.Fatalf("OpenCode Free 应为 /inference + v2, 实际 %+v", got)
	}
	if got := read("OpenCode Free Old"); got.BaseURL != "https://opencode.ai/inference" || got.OpencodeLane != "v1" {
		t.Fatalf("OpenCode Free Old 应改指 /inference + v1, 实际 %+v", got)
	}
	if got := read("自建渠道"); got.BaseURL != "https://opencode.ai/zen" || got.OpencodeLane != "" {
		t.Fatalf("自定义渠道不应被改写, 实际 %+v", got)
	}

	// 重复执行幂等: 迁移可能在多实例启动时重放。
	if err := alignOpencodeFreeLaneChannels(database); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if got := read("OpenCode Free Old"); got.OpencodeLane != "v1" {
		t.Fatalf("重复执行后形态被改写: %+v", got)
	}
}
