package op

import (
	"fmt"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/require"
)

// TestUpsertOpencodeGeneratedAccountsBeyondParamLimit 守住宽表落账的参数上限:
// 2000 行 × 18 列 = 36000 个绑定参数, 超过 SQLite 的 32766, 必须靠分批收窄。
func TestUpsertOpencodeGeneratedAccountsBeyondParamLimit(t *testing.T) {
	const channelID = 980001
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channelID).Delete(&model.OpencodeProxyAccount{}).Error
	})

	accounts := make([]string, 0, 2000)
	for i := range 2000 {
		accounts = append(accounts, fmt.Sprintf("acct_%05d", i))
	}

	require.NoError(t, UpsertOpencodeGeneratedAccounts(channelID, accounts))

	var count int64
	require.NoError(t, db.GetDB().Model(&model.OpencodeProxyAccount{}).
		Where("channel_id = ?", channelID).Count(&count).Error)
	require.EqualValues(t, len(accounts), count)

	// 重复落账走 DO NOTHING, 不新增行也不报错。
	require.NoError(t, UpsertOpencodeGeneratedAccounts(channelID, accounts))
	require.NoError(t, db.GetDB().Model(&model.OpencodeProxyAccount{}).
		Where("channel_id = ?", channelID).Count(&count).Error)
	require.EqualValues(t, len(accounts), count)
}
