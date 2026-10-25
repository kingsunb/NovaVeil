package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 26,
		Up:      alignOpencodeFreeLaneChannels,
	})
}

// opencodeFreeLaneTargets 是内置免费档渠道的目标形态。
// 2026-10-26 实测: /zen 免费档已关闭(官方 1.19.4 指向 /zen/v1 同样 429),
// 官方 1.x 默认 baseURL 即 /inference; 两条渠道改为同端点, 用 opencode_lane 区分形态。
var opencodeFreeLaneTargets = []struct {
	Name    string
	BaseURL string
	Lane    string
}{
	{Name: "OpenCode Free", BaseURL: "https://opencode.ai/inference", Lane: "v2"},
	{Name: "OpenCode Free Old", BaseURL: "https://opencode.ai/inference", Lane: "v1"},
}

// alignOpencodeFreeLaneChannels 把存量内置免费档渠道对齐到 /inference 并写入显式形态。
// 尚未落库的渠道由 EnsureBuiltinChannels 按新定义创建, 此处跳过; 重复执行幂等。
func alignOpencodeFreeLaneChannels(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, target := range opencodeFreeLaneTargets {
			// 显式 SQL: opencode_lane 是新增列, 回填不依赖 GORM 的字段映射,
			// 避免新旧 schema 差异让更新静默丢列。
			result := tx.Exec(
				`UPDATE channels SET base_url = ?, opencode_lane = ? WHERE builtin = ? AND name = ?`,
				target.BaseURL, target.Lane, true, target.Name,
			)
			if result.Error != nil {
				return fmt.Errorf("对齐内置渠道 %s 的免费档形态失败: %w", target.Name, result.Error)
			}
		}
		return nil
	})
}
