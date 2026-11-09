package migrate

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration028DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate028-%d?mode=memory&cache=shared", time.Now().UnixNano())
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
	if err := database.AutoMigrate(&model.OpencodeProxyAccount{}); err != nil {
		t.Fatalf("create account schema: %v", err)
	}
	return database
}

func execMigration028(t *testing.T, database *gorm.DB, query string, args ...interface{}) {
	t.Helper()
	if err := database.Exec(query, args...).Error; err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func pluckMigration028Ints(t *testing.T, database *gorm.DB, query string) []int {
	t.Helper()
	var values []int
	if err := database.Raw(query).Scan(&values).Error; err != nil {
		t.Fatalf("load %s: %v", query, err)
	}
	return values
}

func migration028Accounts(t *testing.T, database *gorm.DB) []string {
	t.Helper()
	var values []string
	query := "SELECT channel_id || ':' || account FROM opencode_proxy_accounts ORDER BY channel_id, account"
	if err := database.Raw(query).Scan(&values).Error; err != nil {
		t.Fatalf("load accounts: %v", err)
	}
	return values
}

func TestDeleteRetiredOpencodeFreeLegacyChannels(t *testing.T) {
	database := openMigration028DB(t)
	channels := []struct {
		id      int
		name    string
		builtin bool
	}{
		{1, "OpenCode Free", true},
		{2, "OpenCode Free Old", true},
		{10, "OpenCode Free V2", true},
		{11, "OpenCode Free V1", true},
		{12, "OpenCode Free", false},
		{14, "OpenCode Free V1 备用1", true},
	}
	for _, channel := range channels {
		execMigration028(t, database,
			"INSERT INTO channels(id, name, builtin) VALUES(?, ?, ?)",
			channel.id, channel.name, channel.builtin)
	}
	channelModels := [][2]int{
		{101, 1}, {102, 1}, {201, 2},
		{1001, 10}, {1101, 11}, {1201, 12}, {1401, 14},
	}
	for _, channelModel := range channelModels {
		execMigration028(t, database,
			"INSERT INTO channel_models(id, channel_id, name) VALUES(?, ?, 'm')",
			channelModel[0], channelModel[1])
	}
	for _, group := range [][2]int{{1, 1002}, {2, 1003}, {3, 1004}} {
		execMigration028(t, database,
			"INSERT INTO groups(id, active_item_id) VALUES(?, ?)", group[0], group[1])
	}
	for _, item := range [][3]int{
		{1001, 1, 101}, {1002, 1, 102}, {1003, 2, 1001},
		{1004, 3, 1201}, {1005, 3, 201},
	} {
		execMigration028(t, database,
			"INSERT INTO group_items(id, group_id, channel_model_id) VALUES(?, ?, ?)",
			item[0], item[1], item[2])
	}
	for _, rank := range [][3]int{
		{2001, 1, 101}, {2002, 2, 201}, {2003, 10, 1001},
		{2004, 12, 1201}, {2005, 14, 1401},
	} {
		execMigration028(t, database,
			"INSERT INTO model_eval_ranks(id, channel_id, channel_model_id) VALUES(?, ?, ?)",
			rank[0], rank[1], rank[2])
	}
	for _, account := range []struct {
		channelID    int
		account      string
		successCount int
	}{
		{10, "dup", 99},
		{1, "a", 7},
		{1, "dup", 5},
		{2, "x", 3},
		{12, "custom", 1},
	} {
		execMigration028(t, database,
			`INSERT INTO opencode_proxy_accounts(channel_id, key_id, account, last_success_at, success_count)
			 VALUES(?, '', ?, ?, ?)`,
			account.channelID, account.account, time.Now(), account.successCount)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := deleteRetiredOpencodeFreeLegacyChannels(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM channels ORDER BY id"), []int{10, 11, 12, 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM channel_models ORDER BY id"), []int{1001, 1101, 1201, 1401}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel_models = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM group_items ORDER BY id"), []int{1003, 1004}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group_items = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT active_item_id FROM groups ORDER BY id"), []int{0, 1003, 1004}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active_item_id = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM model_eval_ranks ORDER BY id"), []int{2003, 2004, 2005}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model_eval_ranks = %v, want %v", got, want)
	}
	if got, want := migration028Accounts(t, database), []string{"10:a", "10:dup", "11:x", "12:custom"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database,
		"SELECT success_count FROM opencode_proxy_accounts WHERE channel_id = 10 AND account = 'dup'"), []int{99}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target duplicate stats = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database,
		"SELECT success_count FROM opencode_proxy_accounts WHERE channel_id = 10 AND account = 'a'"), []int{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated account stats = %v, want %v", got, want)
	}
}

func TestDeleteRetiredOpencodeFreeLegacyChannelsWithoutTarget(t *testing.T) {
	database := openMigration028DB(t)
	execMigration028(t, database, "INSERT INTO channels(id, name, builtin) VALUES(1, 'OpenCode Free', 1)")
	execMigration028(t, database, "INSERT INTO channels(id, name, builtin) VALUES(12, 'OpenCode Free', 0)")
	execMigration028(t, database, "INSERT INTO channel_models(id, channel_id, name) VALUES(101, 1, 'm')")
	execMigration028(t, database, "INSERT INTO channel_models(id, channel_id, name) VALUES(1201, 12, 'm')")
	execMigration028(t, database, "INSERT INTO groups(id, active_item_id) VALUES(1, 1001)")
	execMigration028(t, database, "INSERT INTO groups(id, active_item_id) VALUES(2, 1002)")
	execMigration028(t, database, "INSERT INTO group_items(id, group_id, channel_model_id) VALUES(1001, 1, 101)")
	execMigration028(t, database, "INSERT INTO group_items(id, group_id, channel_model_id) VALUES(1002, 2, 1201)")
	execMigration028(t, database, "INSERT INTO model_eval_ranks(id, channel_id, channel_model_id) VALUES(2001, 1, 101)")
	execMigration028(t, database, "INSERT INTO model_eval_ranks(id, channel_id, channel_model_id) VALUES(2002, 12, 1201)")
	execMigration028(t, database,
		`INSERT INTO opencode_proxy_accounts(channel_id, key_id, account, last_success_at, success_count)
		 VALUES(1, '', 'a', ?, 7)`, time.Now())
	execMigration028(t, database,
		`INSERT INTO opencode_proxy_accounts(channel_id, key_id, account, last_success_at, success_count)
		 VALUES(12, '', 'custom', ?, 1)`, time.Now())

	for attempt := 0; attempt < 2; attempt++ {
		if err := deleteRetiredOpencodeFreeLegacyChannels(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM channels ORDER BY id"), []int{12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM channel_models ORDER BY id"), []int{1201}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel_models = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM group_items ORDER BY id"), []int{1002}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group_items = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT active_item_id FROM groups ORDER BY id"), []int{0, 1002}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active_item_id = %v, want %v", got, want)
	}
	if got, want := pluckMigration028Ints(t, database, "SELECT id FROM model_eval_ranks ORDER BY id"), []int{2002}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model_eval_ranks = %v, want %v", got, want)
	}
	if got, want := migration028Accounts(t, database), []string{"1:a", "12:custom"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
}
