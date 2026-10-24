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

// TestPruneOpencodeProxyAccounts 按最近活跃时间(成功/冷却/体检取最大)裁剪到 keep 条:
// 冷却中的记录不会被提前清掉, 零值最旧, 且不影响其他渠道。
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
