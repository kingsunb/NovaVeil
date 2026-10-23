package migrate

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration024DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate024-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	schema := []string{
		`CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT NOT NULL, builtin BOOLEAN NOT NULL)`,
		`CREATE TABLE channel_models (id INTEGER PRIMARY KEY, channel_id INTEGER NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE groups (id INTEGER PRIMARY KEY, active_item_id INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE group_items (id INTEGER PRIMARY KEY, group_id INTEGER NOT NULL, channel_model_id INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE model_eval_ranks (id INTEGER PRIMARY KEY, channel_id INTEGER NOT NULL, channel_model_id INTEGER NOT NULL, position INTEGER NOT NULL DEFAULT 0)`,
	}
	for _, statement := range schema {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	return database
}

func execMigration024(t *testing.T, database *gorm.DB, query string, args ...interface{}) {
	t.Helper()
	if err := database.Exec(query, args...).Error; err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func pluckMigration024Ints(t *testing.T, database *gorm.DB, query string) []int {
	t.Helper()
	var values []int
	if err := database.Raw(query).Scan(&values).Error; err != nil {
		t.Fatalf("load %s: %v", query, err)
	}
	return values
}

func TestDeleteRetiredOpencodeFreeDuplicates(t *testing.T) {
	database := openMigration024DB(t)
	channels := []struct {
		id      int
		name    string
		builtin bool
	}{
		{1, "OpenCode Free", true},
		{2, "OpenCode Free 2", true},
		{3, "OpenCode Free 3", true},
		{4, "OpenCode Free 2", false},
		{5, "OpenCode Free Old", true},
	}
	for _, channel := range channels {
		execMigration024(t, database,
			"INSERT INTO channels(id, name, builtin) VALUES(?, ?, ?)",
			channel.id, channel.name, channel.builtin)
	}
	channelModels := [][2]interface{}{
		{11, 1}, {21, 2}, {22, 2}, {31, 3}, {41, 4},
	}
	for _, channelModel := range channelModels {
		execMigration024(t, database,
			"INSERT INTO channel_models(id, channel_id, name) VALUES(?, ?, 'm')",
			channelModel[0], channelModel[1])
	}
	execMigration024(t, database, "INSERT INTO groups(id, active_item_id) VALUES(1, 102)")
	execMigration024(t, database, "INSERT INTO groups(id, active_item_id) VALUES(2, 104)")
	groupItems := [][3]int{{101, 1, 21}, {102, 1, 22}, {103, 1, 41}, {104, 2, 11}}
	for _, item := range groupItems {
		execMigration024(t, database,
			"INSERT INTO group_items(id, group_id, channel_model_id) VALUES(?, ?, ?)",
			item[0], item[1], item[2])
	}
	ranks := [][3]int{{201, 2, 21}, {202, 2, 22}, {203, 4, 41}, {204, 1, 11}}
	for _, rank := range ranks {
		execMigration024(t, database,
			"INSERT INTO model_eval_ranks(id, channel_id, channel_model_id) VALUES(?, ?, ?)",
			rank[0], rank[1], rank[2])
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := deleteRetiredOpencodeFreeDuplicates(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM channels ORDER BY id"), []int{1, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM channel_models ORDER BY id"), []int{11, 41}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel_models = %v, want %v", got, want)
	}
	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM group_items ORDER BY id"), []int{103, 104}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group_items = %v, want %v", got, want)
	}
	if got, want := pluckMigration024Ints(t, database, "SELECT active_item_id FROM groups ORDER BY id"), []int{0, 104}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active_item_id = %v, want %v", got, want)
	}
	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM model_eval_ranks ORDER BY id"), []int{203, 204}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model_eval_ranks = %v, want %v", got, want)
	}
}

func TestDeleteRetiredOpencodeFreeDuplicatesWithoutMatch(t *testing.T) {
	database := openMigration024DB(t)
	execMigration024(t, database, "INSERT INTO channels(id, name, builtin) VALUES(1, 'OpenCode Free', 1)")
	execMigration024(t, database, "INSERT INTO channels(id, name, builtin) VALUES(2, 'OpenCode Free 2', 0)")
	execMigration024(t, database, "INSERT INTO channel_models(id, channel_id, name) VALUES(21, 2, 'm')")

	for attempt := 0; attempt < 2; attempt++ {
		if err := deleteRetiredOpencodeFreeDuplicates(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM channels ORDER BY id"), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	if got, want := pluckMigration024Ints(t, database, "SELECT id FROM channel_models ORDER BY id"), []int{21}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel_models = %v, want %v", got, want)
	}
}
