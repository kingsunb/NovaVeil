package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rankExistsForChannelModel 报告是否仍有排序条目引用指定渠道模型。
func rankExistsForChannelModel(t *testing.T, channelModelID int) bool {
	t.Helper()
	var count int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).
		Where("channel_model_id = ?", channelModelID).Count(&count).Error)
	return count > 0
}

// TestChannelUpdateDeletingModelCleansEvalRank 验证编辑渠道移除某模型时,
// 引用该模型的评估排序条目被级联删除, 保留的模型排序不受影响。
// 对应用户反馈: 删除模型后点「更新分组」会报模型已删除, 删模型时应当一并清理关联排序。
func TestChannelUpdateDeletingModelCleansEvalRank(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "rk-sync", "rk-keep", "rk-drop")
	keepID, dropID := cm[0], cm[1]
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: keepID, ChannelName: ch.Name, ModelName: "rk-keep", Outcome: model.ModelEvalOK, Content: "keep",
	}))
	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: dropID, ChannelName: ch.Name, ModelName: "rk-drop", Outcome: model.ModelEvalOK, Content: "drop",
	}))

	// 编辑渠道仅保留 rk-keep, rk-drop 对应的渠道模型随之删除。
	keepOnly := []model.ChannelModel{{Name: "rk-keep"}}
	_, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Models: &keepOnly}, ctx)
	require.NoError(t, err)

	assert.False(t, rankExistsForChannelModel(t, dropID), "被移除模型的排序条目应已级联删除")
	assert.True(t, rankExistsForChannelModel(t, keepID), "保留模型的排序条目不应被误删")

	// 模拟前端「更新 auto 分组」: 用当前排序替换分组成员, 不应再因模型已删除而整体中止。
	ranks, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	g, _, err := GroupReplaceItemsByName(ctx, "auto-rk-sync", ranks)
	require.NoError(t, err, "残留排序已清理, 更新分组不应报模型已删除")
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })
	require.Len(t, g.Items, 1, "仅保留的模型应进入分组")
	assert.Equal(t, keepID, g.Items[0].ChannelModelID)
}

// TestChannelDelCleansEvalRanks 验证删除整条渠道时, 该渠道全部模型的评估排序条目被级联删除,
// 后续「更新 auto 分组」不会因引用已删模型而报错。
func TestChannelDelCleansEvalRanks(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "rk-del", "rk-del-model")
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: cm[0], ChannelName: ch.Name, ModelName: "rk-del-model", Outcome: model.ModelEvalOK, Content: "c",
	}))
	require.True(t, rankExistsForChannelModel(t, cm[0]), "前置: 排序条目应已写入")

	require.NoError(t, ChannelDel(ch.ID, ctx))

	assert.False(t, rankExistsForChannelModel(t, cm[0]), "渠道删除后其排序条目应已级联删除")

	ranks, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	for _, r := range ranks {
		assert.NotEqual(t, ch.ID, r.ChannelID, "已删渠道的排序不应再出现在列表")
	}
	// 用当前排序更新分组, 不应触发 ErrGroupReplaceTargetMissing。
	if _, _, err := GroupReplaceItemsByName(ctx, "auto-rk-del", ranks); err != nil {
		assert.NotErrorIs(t, err, ErrGroupReplaceTargetMissing, "残留排序已清理, 不应再报模型已删除")
	}
}

// TestDeleteEvalRanksByChannelModelsNoopOnEmpty 验证空入参直接返回, 不产生误删。
func TestDeleteEvalRanksByChannelModelsNoopOnEmpty(t *testing.T) {
	require.NoError(t, deleteEvalRanksByChannelModels(db.GetDB(), nil))
	require.NoError(t, deleteEvalRanksByChannelModels(db.GetDB(), []int{}))
}
