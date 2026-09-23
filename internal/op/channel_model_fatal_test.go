package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelModelRecordFatalEvalFailure(t *testing.T) {
	ctx := context.Background()
	_, cmIDs := createChannelForEval(t, "fatal-eval", "fatal-model")
	cmID := cmIDs[0]

	for i := 1; i <= 2; i++ {
		deleted, err := ChannelModelRecordFatalEvalFailure(ctx, cmID)
		require.NoError(t, err)
		assert.False(t, deleted, "第 %d 次不应删除", i)

		var cm model.ChannelModel
		require.NoError(t, db.GetDB().First(&cm, cmID).Error)
		assert.Equal(t, i, cm.EvalFatalFailures, "第 %d 次累计计数", i)
	}

	// 第 3 次达到阈值, 删除渠道模型。
	deleted, err := ChannelModelRecordFatalEvalFailure(ctx, cmID)
	require.NoError(t, err)
	assert.True(t, deleted)

	var count int64
	require.NoError(t, db.GetDB().Model(&model.ChannelModel{}).Where("id = ?", cmID).Count(&count).Error)
	assert.Zero(t, count, "达到阈值后渠道模型应被删除")

	// 幂等: 模型已删, 再次调用不报错、不自增。
	deleted, err = ChannelModelRecordFatalEvalFailure(ctx, cmID)
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestChannelModelRecordFatalEvalFailureZeroID(t *testing.T) {
	deleted, err := ChannelModelRecordFatalEvalFailure(context.Background(), 0)
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestChannelModelRecordFatalEvalFailureMissingID(t *testing.T) {
	deleted, err := ChannelModelRecordFatalEvalFailure(context.Background(), 999999999)
	require.NoError(t, err)
	assert.False(t, deleted)
}
