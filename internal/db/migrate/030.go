package migrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 30,
		Up:      normalizeOpencodeOfficialClientHeaders,
	})
}

// opencodeOfficialChannelName 是内置 OpenCode 官方渠道名。
const opencodeOfficialChannelName = "OpenCode"

// 把内置 OpenCode 官方渠道的 desktop 头归一为 cli。
// 官方推理请求只由 cli 客户端发起, 桌面端取值在 2.x 后已不适用。
func normalizeOpencodeOfficialClientHeaders(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable("channels") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := tx.Where("builtin = ? AND name = ?", true, opencodeOfficialChannelName).
			Find(&channels).Error; err != nil {
			return fmt.Errorf("查找内置 OpenCode 渠道失败: %w", err)
		}
		for i := range channels {
			updated, changed := opencodeCliHeaders(channels[i].CustomHeader)
			if !changed {
				continue
			}
			payload, err := json.Marshal(updated)
			if err != nil {
				return fmt.Errorf("序列化 OpenCode 自定义头失败: %w", err)
			}
			// 直接写 JSON 文本, 避免 serializer 二次编码。
			if err := tx.Exec("UPDATE channels SET custom_header = ? WHERE id = ?",
				string(payload), channels[i].ID).Error; err != nil {
				return fmt.Errorf("更新 OpenCode 自定义头失败: %w", err)
			}
		}
		return nil
	})
}

// opencodeCliHeaders 只归一桌面端取值, 其余自定义头原样保留。
func opencodeCliHeaders(headers []model.CustomHeader) ([]model.CustomHeader, bool) {
	out := make([]model.CustomHeader, len(headers))
	copy(out, headers)
	changed := false
	for i := range out {
		switch out[i].HeaderKey {
		case "x-opencode-client":
			if out[i].HeaderValue == "desktop" {
				out[i].HeaderValue = "cli"
				changed = true
			}
		case "User-Agent":
			value := out[i].HeaderValue
			if strings.HasPrefix(value, "opencode/latest/") && strings.HasSuffix(value, "/desktop") {
				out[i].HeaderValue = strings.TrimSuffix(value, "/desktop") + "/cli"
				changed = true
			}
		}
	}
	return out, changed
}
