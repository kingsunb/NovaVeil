package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration029DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate029-%d?mode=memory&cache=shared", time.Now().UnixNano())
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

func TestDeleteRetiredOpencodeFreeV1Channels(t *testing.T) {
	database := openMigration029DB(t)
	channels := []struct {
		id      int
		name    string
		builtin bool
	}{
		{1, "OpenCode Free V2", true},
		{2, "OpenCode Free V1", true},
		{3, "OpenCode Free V1 账号池", true},
		{4, "OpenCode Free V1 备用1", true},
		{5, "OpenCode Free V1 备用2", true},
		// 用户自建的同名渠道不在清理范围。
		{6, "OpenCode Free V1", false},
	}
	for _, channel := range channels {
		if err := database.Exec("INSERT INTO channels(id, name, builtin) VALUES(?, ?, ?)", channel.id, channel.name, channel.builtin).Error; err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}
	if err := database.Exec("INSERT INTO channel_models(id, channel_id, name) VALUES(1, 2, 'deepseek-v4-flash-free')").Error; err != nil {
		t.Fatalf("seed channel model: %v", err)
	}
	if err := database.Exec("INSERT INTO groups(id, active_item_id) VALUES(1, 1)").Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := database.Exec("INSERT INTO group_items(id, group_id, channel_model_id) VALUES(1, 1, 1)").Error; err != nil {
		t.Fatalf("seed group item: %v", err)
	}
	if err := database.Exec("INSERT INTO model_eval_ranks(id, channel_id, channel_model_id, position) VALUES(1, 2, 1, 0)").Error; err != nil {
		t.Fatalf("seed eval rank: %v", err)
	}

	if err := deleteRetiredOpencodeFreeV1Channels(database); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	var remaining []string
	if err := database.Raw("SELECT name FROM channels ORDER BY id").Scan(&remaining).Error; err != nil {
		t.Fatalf("load channels: %v", err)
	}
	want := []string{"OpenCode Free V2", "OpenCode Free V1"}
	if len(remaining) != len(want) || remaining[0] != want[0] || remaining[1] != want[1] {
		t.Fatalf("保留下来的渠道异常: %v", remaining)
	}
	var activeItem int
	if err := database.Raw("SELECT active_item_id FROM groups WHERE id = 1").Scan(&activeItem).Error; err != nil {
		t.Fatalf("load group: %v", err)
	}
	if activeItem != 0 {
		t.Fatalf("生效成员应被清除, 实际 %d", activeItem)
	}
	var items int64
	if err := database.Raw("SELECT COUNT(*) FROM group_items").Scan(&items).Error; err != nil {
		t.Fatalf("count group items: %v", err)
	}
	if items != 0 {
		t.Fatalf("分组成员应被删除, 实际 %d", items)
	}
	var ranks int64
	if err := database.Raw("SELECT COUNT(*) FROM model_eval_ranks").Scan(&ranks).Error; err != nil {
		t.Fatalf("count eval ranks: %v", err)
	}
	if ranks != 0 {
		t.Fatalf("评估排序应被删除, 实际 %d", ranks)
	}
}
