package migrate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration030DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate030-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	schema := `CREATE TABLE channels (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		builtin BOOLEAN NOT NULL,
		custom_header TEXT NOT NULL DEFAULT '[]'
	)`
	if err := database.Exec(schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return database
}

const desktopHeadersJSON = `[{"header_key":"x-opencode-client","header_value":"desktop"},{"header_key":"User-Agent","header_value":"opencode/latest/2.3.0/desktop"},{"header_key":"X-Custom","header_value":"keep"}]`

func TestNormalizeOpencodeOfficialClientHeaders(t *testing.T) {
	database := openMigration030DB(t)
	rows := []struct {
		id      int
		name    string
		builtin bool
		headers string
	}{
		{1, "OpenCode", true, desktopHeadersJSON},
		// 用户自建同名渠道不在清理范围。
		{2, "OpenCode", false, desktopHeadersJSON},
	}
	for _, row := range rows {
		if err := database.Exec(
			"INSERT INTO channels(id, name, builtin, custom_header) VALUES(?, ?, ?, ?)",
			row.id, row.name, row.builtin, row.headers,
		).Error; err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}

	if err := normalizeOpencodeOfficialClientHeaders(database); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	var builtinHeaders, customHeaders string
	if err := database.Raw("SELECT custom_header FROM channels WHERE id = 1").Scan(&builtinHeaders).Error; err != nil {
		t.Fatalf("load builtin: %v", err)
	}
	if err := database.Raw("SELECT custom_header FROM channels WHERE id = 2").Scan(&customHeaders).Error; err != nil {
		t.Fatalf("load custom: %v", err)
	}
	if !strings.Contains(builtinHeaders, `"header_value":"cli"`) {
		t.Fatalf("x-opencode-client 应归一为 cli: %s", builtinHeaders)
	}
	if !strings.Contains(builtinHeaders, `opencode/latest/2.3.0/cli`) {
		t.Fatalf("User-Agent 应归一为 cli: %s", builtinHeaders)
	}
	if strings.Contains(builtinHeaders, "desktop") {
		t.Fatalf("不应残留 desktop: %s", builtinHeaders)
	}
	if !strings.Contains(builtinHeaders, `"X-Custom"`) || !strings.Contains(builtinHeaders, `"keep"`) {
		t.Fatalf("其他自定义头应保留: %s", builtinHeaders)
	}
	if customHeaders != desktopHeadersJSON {
		t.Fatalf("非内置渠道不应改写: %s", customHeaders)
	}
}
