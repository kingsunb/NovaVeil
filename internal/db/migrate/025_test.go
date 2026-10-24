package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigration025DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate025-%d?mode=memory&cache=shared", time.Now().UnixNano())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(&model.OpencodeProxyAccount{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return database
}

func TestCleanFabricatedOpencodeSuccessTimes(t *testing.T) {
	database := openMigration025DB(t)
	now := time.Now().Truncate(time.Second).UTC()
	fabricatedCooldown := now.Add(24 * time.Hour)
	// 旧版伪造行: 冷却落库时把最近成功写成「冷却时间 − 7 天」。
	fabricated := model.OpencodeProxyAccount{
		ChannelID: 1, Account: "acct-fabricated",
		CooldownUntil: fabricatedCooldown, LastSuccessAt: fabricatedCooldown.Add(-7 * 24 * time.Hour),
	}
	// 真实成功行: 冷却中的账号此前确有成功(时间不吻合伪造公式)。
	realSuccess := model.OpencodeProxyAccount{
		ChannelID: 1, Account: "acct-real-success",
		CooldownUntil: now.Add(2 * time.Hour), LastSuccessAt: now.Add(-3 * time.Hour),
		SuccessCount: 5, SuccessDays: 2,
	}
	// 无冷却的真实成功行。
	plainSuccess := model.OpencodeProxyAccount{
		ChannelID: 1, Account: "acct-plain-success",
		LastSuccessAt: now.Add(-time.Hour), SuccessCount: 2, SuccessDays: 1,
	}
	// 未探测行: 无冷却无成功。
	untested := model.OpencodeProxyAccount{ChannelID: 1, Account: "acct-untested"}
	for _, row := range []*model.OpencodeProxyAccount{&fabricated, &realSuccess, &plainSuccess, &untested} {
		if err := database.Create(row).Error; err != nil {
			t.Fatalf("seed %s: %v", row.Account, err)
		}
	}

	if err := cleanFabricatedOpencodeSuccessTimes(database); err != nil {
		t.Fatalf("migration: %v", err)
	}

	loaded := map[string]model.OpencodeProxyAccount{}
	var rows []model.OpencodeProxyAccount
	if err := database.Find(&rows).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, row := range rows {
		loaded[row.Account] = row
	}
	if _, ok := loaded["acct-fabricated"]; !ok {
		t.Fatalf("fabricated 账号应存在")
	}
	if !loaded["acct-fabricated"].LastSuccessAt.IsZero() {
		t.Fatalf("伪造的最近成功应被清零: %+v", loaded["acct-fabricated"])
	}
	for _, account := range []string{"acct-real-success", "acct-plain-success"} {
		if loaded[account].LastSuccessAt.IsZero() {
			t.Fatalf("%s 的真实最近成功不应被清零: %+v", account, loaded[account])
		}
	}
	if loaded["acct-untested"].LastSuccessAt.IsZero() != true {
		t.Fatalf("未探测行应保持零值")
	}
	if loaded["acct-real-success"].SuccessCount != 5 || loaded["acct-plain-success"].SuccessCount != 2 {
		t.Fatalf("成功次数不应被迁移改动")
	}
}
