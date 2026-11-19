package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 31,
		Up:      dropContextProbeRecords,
	})
}

// dropContextProbeRecords 删探测历史表。
func dropContextProbeRecords(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("context_probe_records") {
		return nil
	}
	if err := db.Migrator().DropTable("context_probe_records"); err != nil {
		return fmt.Errorf("failed to drop context_probe_records: %w", err)
	}
	return nil
}
