package migrate

import (
	"fmt"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 25,
		Up:      cleanFabricatedOpencodeSuccessTimes,
	})
}

// opencodeFabricatedSuccessDelta 是旧版冷却落库伪造最近成功时间的固定偏移:
// 旧 SaveOpencodeProxyCooldown 把 last_success_at 写成「冷却时间 − 7 天」,
// 让从未成功的账号带上虚假的最近成功(池加载按非零时间判 verified, 虚假值会被误信)。
const opencodeFabricatedSuccessDelta = -7 * 24 * time.Hour

// cleanFabricatedOpencodeSuccessTimes 清理旧版冷却落库伪造的最近成功时间。
// 判据: success_count = 0 且 last_success_at 恰等于 cooldown_until − 7 天(旧写入由同一
// 时刻的冷却值精确推导, 相等即可判定); 命中行最近成功清零。真实成功行(次数 > 0 或
// 时间不吻合)一律不动。修复后的冷却落库不再伪造, 本迁移只清洗存量。
func cleanFabricatedOpencodeSuccessTimes(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&model.OpencodeProxyAccount{}) {
		return nil
	}
	var rows []model.OpencodeProxyAccount
	if err := db.Where("success_count = ?", 0).Find(&rows).Error; err != nil {
		return fmt.Errorf("查询待清理的代理账号失败: %w", err)
	}
	for _, row := range rows {
		if row.LastSuccessAt.IsZero() || row.CooldownUntil.IsZero() {
			continue
		}
		if !row.LastSuccessAt.Equal(row.CooldownUntil.Add(opencodeFabricatedSuccessDelta)) {
			continue
		}
		if err := db.Model(&model.OpencodeProxyAccount{}).
			Where("id = ?", row.ID).
			Update("last_success_at", time.Time{}).Error; err != nil {
			return fmt.Errorf("清理伪造的最近成功失败: %w", err)
		}
	}
	return nil
}
