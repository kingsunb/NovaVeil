package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRefreshG4FGroqBuiltinBaseURL(t *testing.T) {
	dsn := fmt.Sprintf("file:migrate022-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		builtin BOOLEAN NOT NULL,
		base_url TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}
	rows := []struct {
		name    string
		builtin bool
		baseURL string
	}{
		{"G4F Space Groq Free", true, "https://g4f.space/api/groq"},
		{"G4F Space Groq Free", false, "https://g4f.space/api/groq"},
		{"custom-groq", true, "https://g4f.space/api/groq"},
		{"G4F Space Groq Free", true, "https://g4f.space/api/groq/openai"},
	}
	for _, row := range rows {
		if err := database.Exec(
			"INSERT INTO channels(name, builtin, base_url) VALUES(?, ?, ?)",
			row.name, row.builtin, row.baseURL,
		).Error; err != nil {
			t.Fatalf("insert channel: %v", err)
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := refreshG4FGroqBuiltinBaseURL(database); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}

	var got []string
	if err := database.Table("channels").Order("id").Pluck("base_url", &got).Error; err != nil {
		t.Fatalf("load base urls: %v", err)
	}
	want := []string{
		"https://g4f.space/api/groq/openai",
		"https://g4f.space/api/groq",
		"https://g4f.space/api/groq",
		"https://g4f.space/api/groq/openai",
	}
	if len(got) != len(want) {
		t.Fatalf("base urls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("base urls = %v, want %v", got, want)
		}
	}
}
