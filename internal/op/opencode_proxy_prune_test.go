package op

import (
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertOcAccountAt 落库一个代理账号并指定三个活跃时间字段。
func insertOcAccountAt(t *testing.T, channelID int, account string, success, cooldown, probe time.Time) {
	t.Helper()
	row := model.OpencodeProxyAccount{
		ChannelID:     channelID,
		Account:       account,
		LastSuccessAt: success,
		CooldownUntil: cooldown,
		LastProbeAt:   probe,
		Source:        model.OpencodeAccountSourceGenerated,
	}
	require.NoError(t, db.GetDB().Create(&row).Error)
}

// insertOcAccountWithSuccess 落库一个成功过的代理账号(带成功计数)。
func insertOcAccountWithSuccess(t *testing.T, channelID int, account string, success time.Time, count int64) {
	t.Helper()
	row := model.OpencodeProxyAccount{
		ChannelID:     channelID,
		Account:       account,
		LastSuccessAt: success,
		SuccessCount:  count,
		Source:        model.OpencodeAccountSourceTraffic,
	}
	require.NoError(t, db.GetDB().Create(&row).Error)
}

// TestPruneOpencodeProxyAccounts 全为未成功组时按最近活跃时间(成功/冷却/体检取最大)
// 裁剪到 keep 条: 冷却中的记录不被先清, 零值最旧, 且不影响其他渠道。
func TestPruneOpencodeProxyAccounts(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertOcAccountAt(t, 970001, "keep-cooling", base, base.Add(5*time.Hour), base)
	insertOcAccountAt(t, 970001, "keep-success", base.Add(4*time.Hour), base, base)
	insertOcAccountAt(t, 970001, "drop-probe-only", base, base, base.Add(3*time.Hour))
	insertOcAccountAt(t, 970001, "drop-stale-a", base, base, base)
	insertOcAccountAt(t, 970001, "drop-stale-b", base, base, base)
	insertOcAccountAt(t, 970002, "other-channel", base, base, base)
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id IN ?", []int{970001, 970002}).Delete(&model.OpencodeProxyAccount{}).Error
	})

	require.NoError(t, PruneOpencodeProxyAccounts(970001, 2))

	var remaining []model.OpencodeProxyAccount
	require.NoError(t, db.GetDB().Where("channel_id = ?", 970001).Order("account ASC").Find(&remaining).Error)
	assert.Len(t, remaining, 2, "5 条裁到 2 条")
	assert.Equal(t, "keep-cooling", remaining[0].Account, "冷却中的记录不被提前清掉")
	assert.Equal(t, "keep-success", remaining[1].Account)

	var other model.OpencodeProxyAccount
	require.NoError(t, db.GetDB().Where("channel_id = ?", 970002).First(&other).Error)
	assert.Equal(t, "other-channel", other.Account, "裁剪不影响其他渠道")

	// 已不超过 keep 时 no-op。
	require.NoError(t, PruneOpencodeProxyAccounts(970001, 2))
	require.NoError(t, db.GetDB().Where("channel_id = ?", 970001).Find(&remaining).Error)
	assert.Len(t, remaining, 2)
}

// TestPruneOpencodeProxyAccountsKeepsHistoric 超限淘汰先删从未成功过的账号:
// 历史可用(累计成功过)优先保留, 哪怕其最近活跃时间更旧、未成功号仍在冷却。
func TestPruneOpencodeProxyAccountsKeepsHistoric(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertOcAccountWithSuccess(t, 970003, "historic-old", base, 3)
	insertOcAccountAt(t, 970003, "rejected-cooling", base, base.Add(100*time.Hour), base)
	insertOcAccountAt(t, 970003, "untested-probed", base, base, base.Add(50*time.Hour))
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", 970003).Delete(&model.OpencodeProxyAccount{}).Error
	})

	require.NoError(t, PruneOpencodeProxyAccounts(970003, 1))

	var remaining []model.OpencodeProxyAccount
	require.NoError(t, db.GetDB().Where("channel_id = ?", 970003).Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "historic-old", remaining[0].Account, "历史可用优先保留")
}

func loadOcAccount(t *testing.T, channelID int, account string) model.OpencodeProxyAccount {
	t.Helper()
	var row model.OpencodeProxyAccount
	require.NoError(t, db.GetDB().Where("channel_id = ? AND account = ?", channelID, account).First(&row).Error)
	return row
}

// 窗口按库内旧值累加: 首写为 1, 窗内加 1, 满 6 清零, 成功不清窗口。
func TestSaveOpencodeProxyFailureWindow(t *testing.T) {
	const channelID = 970011
	const account = "window-acc"
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channelID).Delete(&model.OpencodeProxyAccount{}).Error
	})

	require.NoError(t, SaveOpencodeProxyRealFailure(channelID, account))
	first := loadOcAccount(t, channelID, account)
	assert.Equal(t, int64(1), first.RealFailureCount)
	assert.Equal(t, 1, first.FailureWindowCount)
	assert.False(t, first.FailureWindowStart.IsZero())

	require.NoError(t, SaveOpencodeProxyRealFailure(channelID, account))
	second := loadOcAccount(t, channelID, account)
	assert.Equal(t, int64(2), second.RealFailureCount)
	assert.Equal(t, 2, second.FailureWindowCount)
	assert.True(t, second.FailureWindowStart.Equal(first.FailureWindowStart), "窗内不改起点")

	require.NoError(t, db.GetDB().Model(&model.OpencodeProxyAccount{}).
		Where("channel_id = ? AND account = ?", channelID, account).
		Update("failure_window_count", 5).Error)
	require.NoError(t, SaveOpencodeProxyRealFailure(channelID, account))
	full := loadOcAccount(t, channelID, account)
	assert.Equal(t, int64(3), full.RealFailureCount)
	assert.Equal(t, 0, full.FailureWindowCount, "计满 6 次清零")
	assert.True(t, full.FailureWindowStart.IsZero())

	require.NoError(t, SaveOpencodeProxySuccess(channelID, account, time.Now()))
	ok := loadOcAccount(t, channelID, account)
	assert.Equal(t, 0, ok.FailureWindowCount, "成功不清窗口")
}

// 起点超过 24h 后窗口从 1 重开, 不沿用旧计数。
func TestSaveOpencodeProxyFailureWindowResets(t *testing.T) {
	const channelID = 970012
	const account = "window-stale"
	start := time.Now().Add(-25 * time.Hour)
	require.NoError(t, db.GetDB().Create(&model.OpencodeProxyAccount{
		ChannelID:          channelID,
		Account:            account,
		Source:             model.OpencodeAccountSourceTraffic,
		RealFailureCount:   4,
		FailureWindowStart: start,
		FailureWindowCount: 4,
	}).Error)
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channelID).Delete(&model.OpencodeProxyAccount{}).Error
	})

	require.NoError(t, SaveOpencodeProxyRealFailure(channelID, account))
	row := loadOcAccount(t, channelID, account)
	assert.Equal(t, int64(5), row.RealFailureCount)
	assert.Equal(t, 1, row.FailureWindowCount)
	assert.True(t, row.FailureWindowStart.After(start))
}

// 冷却中的失败只加总计数, 窗口与冷却截止都不改; 冷却写入本身清窗口。
func TestSaveOpencodeProxyFailureSkipsCoolingWindow(t *testing.T) {
	const channelID = 970013
	const account = "window-cooling"
	start := time.Now().Add(-time.Hour)
	until := time.Now().Add(2 * time.Hour)
	require.NoError(t, db.GetDB().Create(&model.OpencodeProxyAccount{
		ChannelID:          channelID,
		Account:            account,
		Source:             model.OpencodeAccountSourceTraffic,
		RealFailureCount:   2,
		FailureWindowStart: start,
		FailureWindowCount: 2,
		CooldownUntil:      until,
	}).Error)
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channelID).Delete(&model.OpencodeProxyAccount{}).Error
	})

	require.NoError(t, SaveOpencodeProxyRealFailure(channelID, account))
	row := loadOcAccount(t, channelID, account)
	assert.Equal(t, int64(3), row.RealFailureCount)
	assert.Equal(t, 2, row.FailureWindowCount, "冷却中不推进窗口")
	assert.True(t, row.FailureWindowStart.Equal(start))
	assert.True(t, row.CooldownUntil.Equal(until), "失败落库不改冷却截止")

	require.NoError(t, SaveOpencodeProxyCooldown(channelID, account, until))
	cooled := loadOcAccount(t, channelID, account)
	assert.Equal(t, 0, cooled.FailureWindowCount)
	assert.True(t, cooled.FailureWindowStart.IsZero())
}
