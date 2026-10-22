package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration023DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate023-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		builtin BOOLEAN NOT NULL,
		enabled BOOLEAN NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}
	return database
}

func TestDisableRetiredUncloseAIFree(t *testing.T) {
	database := openMigration023DB(t)
	rows := []struct {
		name    string
		builtin bool
		enabled bool
	}{
		{"UncloseAI Free", true, true},
		{"UncloseAI Free", false, true},
		{"Other Builtin Free", true, true},
		{"UncloseAI Free", true, false},
	}
	for _, row := range rows {
		if err := database.Exec(
			"INSERT INTO channels(name, builtin, enabled) VALUES(?, ?, ?)",
			row.name, row.builtin, row.enabled,
		).Error; err != nil {
			t.Fatalf("insert channel: %v", err)
		}
	}
	if err := database.Exec(`CREATE TABLE enabled_updates (count INTEGER NOT NULL)`).Error; err != nil {
		t.Fatalf("create update counter: %v", err)
	}
	if err := database.Exec(`INSERT INTO enabled_updates(count) VALUES(0)`).Error; err != nil {
		t.Fatalf("seed update counter: %v", err)
	}
	if err := database.Exec(`CREATE TRIGGER count_enabled_update AFTER UPDATE OF enabled ON channels
		BEGIN UPDATE enabled_updates SET count = count + 1; END`).Error; err != nil {
		t.Fatalf("create update trigger: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := disableRetiredUncloseAIFree(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	var got []bool
	if err := database.Table("channels").Order("id").Pluck("enabled", &got).Error; err != nil {
		t.Fatalf("load enabled states: %v", err)
	}
	want := []bool{false, true, true, false}
	if len(got) != len(want) {
		t.Fatalf("enabled states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("enabled states = %v, want %v", got, want)
		}
	}
	var updates int
	if err := database.Table("enabled_updates").Pluck("count", &updates).Error; err != nil {
		t.Fatalf("load update count: %v", err)
	}
	if updates != 1 {
		t.Fatalf("enabled updates = %d, want 1", updates)
	}
}

func TestDisableRetiredUncloseAIFreeWithoutMatch(t *testing.T) {
	database := openMigration023DB(t)
	if err := database.Exec(
		"INSERT INTO channels(name, builtin, enabled) VALUES(?, ?, ?)",
		"Other Builtin Free", true, true,
	).Error; err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := disableRetiredUncloseAIFree(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	var enabled bool
	if err := database.Table("channels").Pluck("enabled", &enabled).Error; err != nil {
		t.Fatalf("load enabled state: %v", err)
	}
	if !enabled {
		t.Fatal("unrelated builtin channel should remain enabled")
	}
}
