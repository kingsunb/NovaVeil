package migrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 20,
		Up:      refreshOpencodeBuiltinEndpoints,
	})
}

// refreshOpencodeBuiltinEndpoints 把已落库的内置 OpenCode 渠道的 BaseURL 和
// CustomHeader 里的 User-Agent 刷成 opencode v2 官方客户端对齐的值。
//
// 背景：builtin.EnsureBuiltinChannels 只补建缺失渠道，不覆盖已存在的同名渠道。
// 升级代码后，存量库里 "OpenCode Free" / "OpenCode" 渠道仍保留旧 BaseURL 和旧 UA，
// 导致新端点(/inference、/zen/v1)和新 UA(opencode/latest/2.0.29/{cli,desktop})
// 不生效。本迁移按旧 BaseURL 模式匹配并就地更新，管理员自定义的 BaseURL 不动。
//
// 幂等：已是新端点的渠道不匹配旧模式，跳过；无 custom_header 列时只更新 base_url。
func refreshOpencodeBuiltinEndpoints(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channels") {
		return nil
	}
	if !hasPhysicalColumn(db, "channels", "base_url") {
		return nil
	}
	hasCustomHeader := hasPhysicalColumn(db, "channels", "custom_header")

	type channelRow struct {
		ID           int    `gorm:"column:id"`
		BaseURL      string `gorm:"column:base_url"`
		CustomHeader string `gorm:"column:custom_header"`
	}

	selectCols := "id, base_url"
	if hasCustomHeader {
		selectCols = "id, base_url, custom_header"
	}

	var rows []channelRow
	if err := db.Table("channels").Select(selectCols).
		Where("builtin = ? AND opencode_compat = ?", true, true).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("query opencode builtin channels: %w", err)
	}

	for _, row := range rows {
		newBaseURL, newUA := opencodeEndpointUpgrade(row.BaseURL)
		if newBaseURL == "" {
			continue // 已是新端点或管理员自定义，跳过
		}

		if err := db.Table("channels").Where("id = ?", row.ID).
			Update("base_url", newBaseURL).Error; err != nil {
			return fmt.Errorf("update base_url for channel %d: %w", row.ID, err)
		}

		if hasCustomHeader && newUA != "" {
			updated, err := rewriteCustomHeaderUserAgent(row.CustomHeader, newUA)
			if err != nil {
				return fmt.Errorf("rewrite user-agent for channel %d: %w", row.ID, err)
			}
			if updated != "" {
				if err := db.Table("channels").Where("id = ?", row.ID).
					Update("custom_header", updated).Error; err != nil {
					return fmt.Errorf("update custom_header for channel %d: %w", row.ID, err)
				}
			}
		}
	}

	return nil
}

// opencodeEndpointUpgrade 按旧 BaseURL 模式返回新端点和新 UA。
// 返回 ("", "") 表示无需更新（已是新端点或非内置 OpenCode 端点）。
func opencodeEndpointUpgrade(current string) (newBaseURL, newUserAgent string) {
	switch {
	case strings.Contains(current, "/zen/go"):
		// 旧付费档 → 新付费档
		return "https://opencode.ai/zen/v1", "opencode/latest/2.0.29/desktop"
	case strings.TrimSpace(current) == "https://opencode.ai/zen":
		// 旧免费档 → 新免费档（精确匹配，避免误伤含 /zen 的自定义端点）
		return "https://opencode.ai/inference", "opencode/latest/2.0.29/cli"
	default:
		return "", ""
	}
}

// customHeaderEntry 对齐 model.CustomHeader 的 JSON 结构，用于迁移内点状改写。
type customHeaderEntry struct {
	HeaderKey   string `json:"header_key"`
	HeaderValue string `json:"header_value"`
}

// rewriteCustomHeaderUserAgent 在 CustomHeader JSON 里把 User-Agent 的值替换成 newUA。
// 无 User-Agent 条目或空 JSON 时返回空串（不写回）。其余头不动。
func rewriteCustomHeaderUserAgent(rawJSON, newUA string) (string, error) {
	if strings.TrimSpace(rawJSON) == "" || rawJSON == "null" {
		return "", nil
	}
	var headers []customHeaderEntry
	if err := json.Unmarshal([]byte(rawJSON), &headers); err != nil {
		return "", fmt.Errorf("decode custom_header: %w", err)
	}
	found := false
	for i, h := range headers {
		if strings.EqualFold(h.HeaderKey, "User-Agent") {
			headers[i].HeaderValue = newUA
			found = true
			break
		}
	}
	if !found {
		return "", nil
	}
	out, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("encode custom_header: %w", err)
	}
	return string(out), nil
}
