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

func TestDeleteOrphanTokenDensities(t *testing.T) {
	dsn := fmt.Sprintf("file:migrate034-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Exec(`CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}
	if err := database.AutoMigrate(&model.TokenDensity{}); err != nil {
		t.Fatalf("create densities: %v", err)
	}
	if err := database.Exec(`INSERT INTO channels(id, name) VALUES(1, 'OpenCode Free V2')`).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	for _, channelID := range []int{1, 2} {
		if err := database.Exec(
			"INSERT INTO token_densities(channel_id, model, density) VALUES(?, 'm', 1)",
			channelID).Error; err != nil {
			t.Fatalf("seed density: %v", err)
		}
	}

	if err := deleteOrphanTokenDensities(database); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var channelIDs []int
	if err := database.Raw("SELECT channel_id FROM token_densities ORDER BY channel_id").
		Scan(&channelIDs).Error; err != nil {
		t.Fatalf("load densities: %v", err)
	}
	if len(channelIDs) != 1 || channelIDs[0] != 1 {
		t.Fatalf("densities = %v, want [1]", channelIDs)
	}
}
