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

func TestDeleteRetiredOpencodeFreeContextWindowSetting(t *testing.T) {
	dsn := fmt.Sprintf("file:migrate035-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	seed := []model.Setting{
		{Key: model.SettingKey("opencode_free_context_window"), Value: "0"},
		{Key: model.SettingKeyModelFilter, Value: ""},
	}
	if err := database.Create(&seed).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := deleteRetiredOpencodeFreeContextWindowSetting(database); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var count int64
	database.Model(&model.Setting{}).Where("key = ?", "opencode_free_context_window").Count(&count)
	if count != 0 {
		t.Fatalf("目标设置行应被删除, 剩余 %d", count)
	}
	database.Model(&model.Setting{}).Where("key = ?", model.SettingKeyModelFilter).Count(&count)
	if count != 1 {
		t.Fatalf("其它设置行不应受影响, got %d", count)
	}
}
