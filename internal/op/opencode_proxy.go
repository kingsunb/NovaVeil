package op

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadOpencodeProxyAccounts 读取某渠道旧单 Key 的代理账号。
func LoadOpencodeProxyAccounts(channelID int) ([]model.OpencodeProxyAccount, error) {
	return LoadOpencodeProxyAccountsForKey(channelID, "")
}

// LoadOpencodeProxyAccountsForKey 读取某渠道指定 Key 的代理账号, 供进程内池懒加载。
func LoadOpencodeProxyAccountsForKey(channelID int, keyID string) ([]model.OpencodeProxyAccount, error) {
	conn := db.GetDB()
	if conn == nil {
		return nil, nil
	}
	var rows []model.OpencodeProxyAccount
	if err := conn.Where("channel_id = ? AND key_id = ?", channelID, keyID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取代理账号失败: %w", err)
	}
	return rows, nil
}

// SaveOpencodeProxySuccess 记录一次下游真实转发成功: 更新最近成功时间, 累计总成功
// 与真实成功数并按自然日(0 点到 24 点)去重累计成功天数; 不存在则插入。
func SaveOpencodeProxySuccess(channelID int, account string, at time.Time) error {
	return SaveOpencodeProxySuccessForKey(channelID, "", account, at)
}

// SaveOpencodeProxySuccessForKey 记录指定 Key 的一次下游真实转发成功。
func SaveOpencodeProxySuccessForKey(channelID int, keyID, account string, at time.Time) error {
	return saveOpencodeAccountSuccessForKey(channelID, keyID, account, at, "real_success_count")
}

// SaveOpencodeProxyProbeSuccess 记录旧单 Key 的一次体检探测成功。
func SaveOpencodeProxyProbeSuccess(channelID int, account string, at time.Time) error {
	return SaveOpencodeProxyProbeSuccessForKey(channelID, "", account, at)
}

// SaveOpencodeProxyProbeSuccessForKey 记录指定 Key 的一次体检探测成功。
func SaveOpencodeProxyProbeSuccessForKey(channelID int, keyID, account string, at time.Time) error {
	return saveOpencodeAccountSuccessForKey(channelID, keyID, account, at, "probe_success_count")
}

// saveOpencodeAccountSuccessForKey 是两类成功记录的公共 upsert: counterColumn 取
// real_success_count / probe_success_count, 插入行对应列置 1。
func saveOpencodeAccountSuccessForKey(channelID int, keyID, account string, at time.Time, counterColumn string) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	day := at.Format("2006-01-02")
	row := model.OpencodeProxyAccount{
		ChannelID:      channelID,
		KeyID:          keyID,
		Account:        account,
		LastSuccessAt:  at,
		Source:         model.OpencodeAccountSourceTraffic,
		SuccessCount:   1,
		SuccessDays:    1,
		LastSuccessDay: day,
	}
	if counterColumn == "probe_success_count" {
		row.ProbeSuccessCount = 1
	} else {
		row.RealSuccessCount = 1
	}
	result := conn.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_id"}, {Name: "account"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_success_at":  at,
			"updated_at":       time.Now(),
			"success_count":    gorm.Expr("success_count + 1"),
			"success_days":     gorm.Expr("CASE WHEN last_success_day = ? THEN success_days ELSE success_days + 1 END", day),
			"last_success_day": day,
			counterColumn:      gorm.Expr(counterColumn + " + 1"),
			// 成功即清除冷却, 与池内回填语义一致; 不清会让台账页在冷却期内
			// 持续显示「健康但不可用」, 且重启后池从库加载又回到冷却。
			"cooldown_until": time.Time{},
		}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存代理账号成功记录失败: %w", result.Error)
	}
	return nil
}

// ocProxyFailureWindow 与池内同名常量对齐: 真实失败翻滚窗口。
const ocProxyFailureWindow = 24 * time.Hour

// ocProxyFailureWindowLimit 与池内同名常量对齐: 窗口内触发冷却的失败次数。
const ocProxyFailureWindowLimit = 6

// SaveOpencodeProxyRealFailure 累计一次下游真实转发失败, 并在事务内维护
// 24h 翻滚窗口: 窗口起点为零或距今超过 24h 则重开(起点=当前、计数=1), 否则
// 计数加一; 计满 6 次窗口清零(起点归零)。该行仍在冷却中时只加总失败数,
// 窗口与冷却截止不动。成功字段与冷却截止时间不在这里改。
func SaveOpencodeProxyRealFailure(channelID int, account string) error {
	return SaveOpencodeProxyRealFailureForKey(channelID, "", account)
}

// SaveOpencodeProxyRealFailureForKey 累计指定 Key 的一次下游真实转发失败。
func SaveOpencodeProxyRealFailureForKey(channelID int, keyID, account string) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	now := time.Now()
	var row model.OpencodeProxyAccount
	err := conn.Where("channel_id = ? AND key_id = ? AND account = ?", channelID, keyID, account).First(&row).Error
	notFound := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !notFound {
		return err
	}
	if notFound {
		row = model.OpencodeProxyAccount{
			ChannelID: channelID,
			KeyID:     keyID,
			Account:   account,
			Source:    model.OpencodeAccountSourceTraffic,
		}
	}
	row.RealFailureCount++
	if !row.CooldownUntil.After(now) {
		if stale := row.FailureWindowStart.IsZero() || now.Sub(row.FailureWindowStart) > ocProxyFailureWindow; stale {
			row.FailureWindowStart = now
			row.FailureWindowCount = 1
		} else if row.FailureWindowCount+1 >= ocProxyFailureWindowLimit {
			row.FailureWindowStart = time.Time{}
			row.FailureWindowCount = 0
		} else {
			row.FailureWindowCount++
		}
	}
	row.UpdatedAt = now
	// 原子 upsert: 并发首写不再撞 UNIQUE 竞态。
	return conn.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_id"}, {Name: "account"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"real_failure_count":   row.RealFailureCount,
			"failure_window_start": row.FailureWindowStart,
			"failure_window_count": row.FailureWindowCount,
			"updated_at":           row.UpdatedAt,
		}),
	}).Create(&row).Error
}

// SaveOpencodeProxyCooldown 记录账号冷却截止时间, 不存在则插入。
// 不伪造最近成功时间: 未成功过的账号冷却行保持零值, 由冷却字段本身驱动可用性。
func SaveOpencodeProxyCooldown(channelID int, account string, until time.Time) error {
	return SaveOpencodeProxyCooldownForKey(channelID, "", account, until)
}

// SaveOpencodeProxyCooldownForKey 记录指定 Key 的账号冷却截止时间。
func SaveOpencodeProxyCooldownForKey(channelID int, keyID, account string, until time.Time) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	row := model.OpencodeProxyAccount{
		ChannelID:     channelID,
		KeyID:         keyID,
		Account:       account,
		Source:        model.OpencodeAccountSourceTraffic,
		CooldownUntil: until,
	}
	result := conn.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_id"}, {Name: "account"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"cooldown_until":       until,
			"failure_window_start": time.Time{},
			"failure_window_count": 0,
			"updated_at":           time.Now(),
		}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存代理账号冷却记录失败: %w", result.Error)
	}
	return nil
}

// opencodeAccountBatchSize 是宽表批量上限: 18 列, 参数须压在 SQLite 32766 内。
const opencodeAccountBatchSize = 500

// UpsertOpencodeGeneratedAccounts 把体检页生成/粘贴的账号落账为未探测状态;
// 已存在的账号不覆盖任何字段。生成即落账, 支撑台账长期展示与筛选。
func UpsertOpencodeGeneratedAccounts(channelID int, accounts []string) error {
	return UpsertOpencodeGeneratedAccountsForKey(channelID, "", accounts)
}

// UpsertOpencodeGeneratedAccountsForKey 把指定 Key 的生成/粘贴账号落账为未探测状态。
func UpsertOpencodeGeneratedAccountsForKey(channelID int, keyID string, accounts []string) error {
	conn := db.GetDB()
	if conn == nil || len(accounts) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]model.OpencodeProxyAccount, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, model.OpencodeProxyAccount{
			ChannelID: channelID,
			KeyID:     keyID,
			Account:   account,
			Source:    model.OpencodeAccountSourceGenerated,
			UpdatedAt: now,
		})
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "key_id"}, {Name: "account"}},
		DoNothing: true,
	}).CreateInBatches(&rows, opencodeAccountBatchSize)
	if result.Error != nil {
		return fmt.Errorf("落账体检账号失败: %w", result.Error)
	}
	return nil
}

// SaveOpencodeProbeResult 记录账号最近一次体检结论; 不存在的账号按体检来源插入。
// 只更新探测字段, 成功时间与冷却由转发路径的回填逻辑维护。
func SaveOpencodeProbeResult(channelID int, account string, outcome string, status int, at time.Time) error {
	return SaveOpencodeProbeResultForKey(channelID, "", account, outcome, status, at)
}

// SaveOpencodeProbeResultForKey 记录指定 Key 的账号最近一次体检结论。
func SaveOpencodeProbeResultForKey(channelID int, keyID, account string, outcome string, status int, at time.Time) error {
	conn := db.GetDB()
	if conn == nil {
		return nil
	}
	row := model.OpencodeProxyAccount{
		ChannelID:        channelID,
		KeyID:            keyID,
		Account:          account,
		Source:           model.OpencodeAccountSourceGenerated,
		LastProbeAt:      at,
		LastProbeOutcome: outcome,
		LastProbeStatus:  status,
	}
	result := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "key_id"}, {Name: "account"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_probe_at", "last_probe_outcome", "last_probe_status", "updated_at"}),
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("保存账号体检结论失败: %w", result.Error)
	}
	return nil
}

// DeleteOpencodeProxyAccounts 删除台账中的指定账号, 返回删除行数。
func DeleteOpencodeProxyAccounts(channelID int, accounts []string) (int64, error) {
	return DeleteOpencodeProxyAccountsForKey(channelID, "", accounts)
}

// DeleteOpencodeProxyAccountsForKey 删除指定 Key 台账中的指定账号。
func DeleteOpencodeProxyAccountsForKey(channelID int, keyID string, accounts []string) (int64, error) {
	conn := db.GetDB()
	if conn == nil || len(accounts) == 0 {
		return 0, nil
	}
	result := conn.Where("channel_id = ? AND key_id = ? AND account IN ?", channelID, keyID, accounts).
		Delete(&model.OpencodeProxyAccount{})
	if result.Error != nil {
		return 0, fmt.Errorf("删除代理账号失败: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// PruneOpencodeProxyAccounts 淘汰某渠道记录直到不超过 keep 条。
// 历史可用(累计成功过)优先保留, 从未成功过的先删;
// 同组按最近活跃时间(成功/冷却/体检三者取最大)从旧到新淘汰。
// 排序在应用层做: 多参 MAX 与裸 OFFSET 仅 SQLite 合法, MySQL/Postgres 直接报错。
func PruneOpencodeProxyAccounts(channelID int, keep int) error {
	return PruneOpencodeProxyAccountsForKey(channelID, "", keep)
}

// PruneOpencodeProxyAccountsForKey 淘汰指定 Key 的记录直到不超过 keep 条。
func PruneOpencodeProxyAccountsForKey(channelID int, keyID string, keep int) error {
	conn := db.GetDB()
	if conn == nil || keep <= 0 {
		return nil
	}
	var rows []model.OpencodeProxyAccount
	if err := conn.Where("channel_id = ? AND key_id = ?", channelID, keyID).Find(&rows).Error; err != nil {
		return fmt.Errorf("查询待清理代理账号失败: %w", err)
	}
	if len(rows) <= keep {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].SuccessCount > 0) != (rows[j].SuccessCount > 0) {
			return rows[i].SuccessCount > 0
		}
		return ocAccountRecency(rows[i]).After(ocAccountRecency(rows[j]))
	})
	stale := make([]uint, 0, len(rows)-keep)
	for _, row := range rows[keep:] {
		stale = append(stale, row.ID)
	}
	if err := conn.Delete(&model.OpencodeProxyAccount{}, stale).Error; err != nil {
		return fmt.Errorf("清理代理账号失败: %w", err)
	}
	return nil
}

// ocAccountRecency 取成功/冷却/体检三个时间的最大值, 零值视为最旧。
func ocAccountRecency(row model.OpencodeProxyAccount) time.Time {
	recency := row.LastSuccessAt
	if row.CooldownUntil.After(recency) {
		recency = row.CooldownUntil
	}
	if row.LastProbeAt.After(recency) {
		recency = row.LastProbeAt
	}
	return recency
}

// EnsureOpencodeAccountAutoProbeSetting 保证自动体检开关的设置行存在并载入缓存。
// 该键不进 DefaultSettings, 随缓存初始化惰性补种默认值("1"开启; 台账为空时任务空转)。
func EnsureOpencodeAccountAutoProbeSetting(ctx context.Context) error {
	key := model.SettingKeyOpencodeAccountAutoProbe
	if _, ok := settingCache.Get(key); ok {
		return nil
	}
	gormDB := db.GetDB()
	if gormDB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB = gormDB.WithContext(ctx)

	var setting model.Setting
	err := gormDB.Where("key = ?", key).First(&setting).Error
	switch {
	case err == nil:
		settingCache.Set(setting.Key, setting.Value)
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("读取自动体检开关失败: %w", err)
	}
	setting = model.Setting{Key: key, Value: "1"}
	if err := gormDB.Create(&setting).Error; err != nil {
		// 并发补种时另一协程可能已插入成功, 重查一次兜底。
		var existing model.Setting
		if retryErr := gormDB.Where("key = ?", key).First(&existing).Error; retryErr == nil {
			settingCache.Set(existing.Key, existing.Value)
			return nil
		}
		return fmt.Errorf("补种自动体检开关失败: %w", err)
	}
	settingCache.Set(setting.Key, setting.Value)
	return nil
}
