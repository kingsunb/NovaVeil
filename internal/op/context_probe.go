package op

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// SaveContextProbeRecord 写入一条探测历史。
func SaveContextProbeRecord(record *model.ContextProbeRecord) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	if err := conn.Create(record).Error; err != nil {
		return fmt.Errorf("保存上下文探测记录失败: %w", err)
	}
	return nil
}

// ListContextProbeRecords 返回最近的探测历史, channelID 为 0 时不过滤渠道。
func ListContextProbeRecords(channelID int, limit int) ([]model.ContextProbeRecord, error) {
	conn := db.GetDB()
	if conn == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := conn.Order("created_at DESC").Limit(limit)
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	var rows []model.ContextProbeRecord
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取上下文探测历史失败: %w", err)
	}
	return rows, nil
}
