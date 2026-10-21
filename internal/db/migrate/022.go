package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 22,
		Up:      refreshG4FGroqBuiltinBaseURL,
	})
}

// 修正存量 Groq 渠道地址。
func refreshG4FGroqBuiltinBaseURL(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channels") || !hasPhysicalColumn(db, "channels", "base_url") {
		return nil
	}
	return db.Table("channels").
		Where("builtin = ? AND name = ? AND base_url = ?",
			true, "G4F Space Groq Free", "https://g4f.space/api/groq").
		Update("base_url", "https://g4f.space/api/groq/openai").Error
}
