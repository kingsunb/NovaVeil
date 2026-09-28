package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupRanksByChannel 删除指定渠道的排序条目，保证测试隔离。
func cleanupRanksByChannel(t *testing.T, channelIDs ...int) {
	t.Helper()
	require.NoError(t, db.GetDB().Where("channel_id IN ?", channelIDs).Delete(&model.ModelEvalRank{}).Error)
}

// TestModelEvalRankUpsertAssignsPositions 验证新增条目的 position 分配：
// 可入组(ok/violation)从 0 起递增；失败(error)不写入排序。
func TestModelEvalRankUpsertAssignsPositions(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970001, 970002, 970003) })

	r1 := &model.ModelEvalRank{ChannelID: 970001, ChannelModelID: 1, ModelName: "rank-a", Outcome: model.ModelEvalOK, Content: "c1"}
	require.NoError(t, ModelEvalRankUpsert(ctx, r1))
	assert.Equal(t, 0, r1.Position)

	r2 := &model.ModelEvalRank{ChannelID: 970002, ChannelModelID: 2, ModelName: "rank-b", Outcome: model.ModelEvalViolation, Content: "c2"}
	require.NoError(t, ModelEvalRankUpsert(ctx, r2))
	assert.Equal(t, 1, r2.Position)

	// error 评估不进入排序：Upsert 返回 nil 但不写入，Position 保持零值。
	r3 := &model.ModelEvalRank{ChannelID: 970003, ChannelModelID: 3, ModelName: "rank-err", Outcome: model.ModelEvalError, Error: "boom"}
	require.NoError(t, ModelEvalRankUpsert(ctx, r3))
	assert.Equal(t, 0, r3.Position, "error 不写入，Position 不被赋值")
	items, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	for _, it := range items {
		assert.NotEqual(t, 970003, it.ChannelID, "error 条目不应出现在排序列表")
	}
}

// TestModelEvalRankUpsertSamePartitionKeepsPosition 验证同分区更新时保留用户已调整的 position。
func TestModelEvalRankUpsertSamePartitionKeepsPosition(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970010) })

	r := &model.ModelEvalRank{ChannelID: 970010, ChannelModelID: 10, ModelName: "upsert-same", Outcome: model.ModelEvalOK, Content: "v1"}
	require.NoError(t, ModelEvalRankUpsert(ctx, r))
	origPos := r.Position

	// 再次 upsert 同目标，仍为 ok，position 应保持不变。
	r2 := &model.ModelEvalRank{ChannelID: 970010, ChannelModelID: 10, ModelName: "upsert-same", Outcome: model.ModelEvalOK, Content: "v2"}
	require.NoError(t, ModelEvalRankUpsert(ctx, r2))
	assert.Equal(t, origPos, r2.Position)

	got, err := ModelEvalRankContent(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, "v2", got.Content, "内容应被更新")
}

// TestModelEvalRankUpsertErrorDoesNotEvictOk 验证 error 评估不会驱逐已存在的成功条目。
func TestModelEvalRankUpsertErrorDoesNotEvictOk(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970020) })

	ok1 := &model.ModelEvalRank{ChannelID: 970020, ChannelModelID: 20, ModelName: "pc-ok", Outcome: model.ModelEvalOK, Content: "good"}
	require.NoError(t, ModelEvalRankUpsert(ctx, ok1))
	origPos := ok1.Position

	// 同渠道同模型再来一次 error 评估：应跳过，不影响已有成功条目。
	changed := &model.ModelEvalRank{ChannelID: 970020, ChannelModelID: 20, ModelName: "pc-ok", Outcome: model.ModelEvalError, Error: "now error"}
	require.NoError(t, ModelEvalRankUpsert(ctx, changed))

	got, err := ModelEvalRankContent(ctx, ok1.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ModelEvalOK, got.Outcome, "已有成功条目不应被 error 覆盖")
	assert.Equal(t, "good", got.Content, "已有成功条目内容不应被覆盖")
	assert.Equal(t, origPos, got.Position, "已有成功条目 position 不应变动")
}

// TestModelEvalRankListOrder 验证列表按 position ASC, id ASC 排序且不含 content。
func TestModelEvalRankListOrder(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970030, 970031) })

	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{ChannelID: 970030, ChannelModelID: 30, ModelName: "lo-a", Outcome: model.ModelEvalOK, Content: "big-content"}))
	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{ChannelID: 970031, ChannelModelID: 31, ModelName: "lo-b", Outcome: model.ModelEvalOK, Content: "big-content-2"}))

	items, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	// 找到本测试插入的两条，验证相对顺序（position 小的在前）。
	var ours []model.ModelEvalRankSummary
	for _, it := range items {
		if it.ChannelID == 970030 || it.ChannelID == 970031 {
			ours = append(ours, it)
		}
	}
	require.Len(t, ours, 2)
	assert.Less(t, ours[0].Position, ours[1].Position)
}

// TestModelEvalRankMoveSwapsPositions 验证向前/向后移动交换 position 并返回最新列表。
func TestModelEvalRankMoveSwapsPositions(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970040, 970041) })

	a := &model.ModelEvalRank{ChannelID: 970040, ChannelModelID: 40, ModelName: "mv-a", Outcome: model.ModelEvalOK}
	require.NoError(t, ModelEvalRankUpsert(ctx, a))
	b := &model.ModelEvalRank{ChannelID: 970041, ChannelModelID: 41, ModelName: "mv-b", Outcome: model.ModelEvalOK}
	require.NoError(t, ModelEvalRankUpsert(ctx, b))
	require.Less(t, a.Position, b.Position)

	// b 向前移动（direction=-1），应与 a 交换。
	list, err := ModelEvalRankMove(ctx, b.ID, -1)
	require.NoError(t, err)
	var afterA, afterB model.ModelEvalRankSummary
	for _, it := range list {
		if it.ID == a.ID {
			afterA = it
		}
		if it.ID == b.ID {
			afterB = it
		}
	}
	assert.Equal(t, b.Position, afterA.Position, "a 应拿到 b 原位置")
	assert.Equal(t, a.Position, afterB.Position, "b 应拿到 a 原位置")

	// 再把 b（现在在前）向前移动应触达边界。
	_, err = ModelEvalRankMove(ctx, afterB.ID, -1)
	assert.ErrorIs(t, err, ErrEvalRankMoveBounds)
}

// TestModelEvalRankMoveRejectsErrorOutcome 验证失败条目不可移动（防御性检查）。
func TestModelEvalRankMoveRejectsErrorOutcome(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970050) })

	// error 条目不经由 Upsert 写入（Upsert 会跳过），直接落库以测试 Move 的防御逻辑。
	e := &model.ModelEvalRank{ChannelID: 970050, ChannelModelID: 50, ModelName: "mv-err", Outcome: model.ModelEvalError, Error: "x", Position: -1}
	require.NoError(t, db.GetDB().Create(e).Error)

	_, err := ModelEvalRankMove(ctx, e.ID, 1)
	assert.ErrorIs(t, err, ErrEvalRankMoveErrorOutcome)
}

// TestModelEvalRankMoveNotFound 验证移动不存在的条目返回 ErrEvalRankNotFound。
func TestModelEvalRankMoveNotFound(t *testing.T) {
	ctx := context.Background()
	_, err := ModelEvalRankMove(ctx, 999999999, 1)
	assert.ErrorIs(t, err, ErrEvalRankNotFound)
}

// TestModelEvalRankRemove 验证删除排序条目。
func TestModelEvalRankRemove(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970060) })

	r := &model.ModelEvalRank{ChannelID: 970060, ChannelModelID: 60, ModelName: "rm-a", Outcome: model.ModelEvalOK}
	require.NoError(t, ModelEvalRankUpsert(ctx, r))
	require.NoError(t, ModelEvalRankRemove(ctx, r.ID))

	_, err := ModelEvalRankContent(ctx, r.ID)
	assert.Error(t, err, "删除后读取应失败")
}

// TestModelEvalRankListExcludesError 验证排序列表不返回 error 条目（即使 DB 中存在历史遗留）。
func TestModelEvalRankListExcludesError(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970070, 970071) })

	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{ChannelID: 970070, ChannelModelID: 70, ModelName: "ok-item", Outcome: model.ModelEvalOK, Content: "c"}))
	// 直接落库一条 error 条目，模拟改动前遗留的数据。
	require.NoError(t, db.GetDB().Create(&model.ModelEvalRank{ChannelID: 970071, ChannelModelID: 71, ModelName: "err-item", Outcome: model.ModelEvalError, Error: "boom", Position: -1}).Error)

	items, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	for _, it := range items {
		assert.NotEqual(t, model.ModelEvalError, it.Outcome, "排序列表不应包含 error 条目")
		assert.NotEqual(t, 970071, it.ChannelID, "error 条目不应出现在列表")
	}
}

// TestModelEvalRankFromHistoryRejectsError 验证从历史加入失败评估时返回 ErrEvalRankErrorOutcome。
func TestModelEvalRankFromHistoryRejectsError(t *testing.T) {
	ctx := context.Background()
	eval := &model.ModelEval{
		ModelEvalSummary: model.ModelEvalSummary{
			ChannelID: 970080, ChannelModelID: 80, ChannelName: "ch", ChannelType: "openai", ModelName: "m",
			Outcome: model.ModelEvalError, Error: "boom",
		},
	}
	require.NoError(t, db.GetDB().Create(eval).Error)
	t.Cleanup(func() { db.GetDB().Delete(&model.ModelEval{}, eval.ID) })

	_, err := ModelEvalRankFromHistory(ctx, eval.ID)
	assert.ErrorIs(t, err, ErrEvalRankErrorOutcome)
}

// TestModelEvalRankListIncludesViolation 验证排序列表包含 violation 条目（成功但格式不符）。
func TestModelEvalRankListIncludesViolation(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970090, 970091) })

	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{ChannelID: 970090, ChannelModelID: 90, ModelName: "vio-a", Outcome: model.ModelEvalViolation, Content: "c"}))
	require.NoError(t, ModelEvalRankUpsert(ctx, &model.ModelEvalRank{ChannelID: 970091, ChannelModelID: 91, ModelName: "ok-a", Outcome: model.ModelEvalOK, Content: "c"}))

	items, err := ModelEvalRankList(ctx)
	require.NoError(t, err)
	var foundVio, foundOK bool
	for _, it := range items {
		if it.ChannelID == 970090 && it.Outcome == model.ModelEvalViolation {
			foundVio = true
		}
		if it.ChannelID == 970091 && it.Outcome == model.ModelEvalOK {
			foundOK = true
		}
	}
	assert.True(t, foundVio, "violation 条目应出现在排序列表")
	assert.True(t, foundOK, "ok 条目应出现在排序列表")
}

// TestModelEvalRankMoveAllowsViolation 验证 violation 条目可调整顺序。
func TestModelEvalRankMoveAllowsViolation(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { cleanupRanksByChannel(t, 970100, 970101) })

	a := &model.ModelEvalRank{ChannelID: 970100, ChannelModelID: 100, ModelName: "vio-mv", Outcome: model.ModelEvalViolation}
	require.NoError(t, ModelEvalRankUpsert(ctx, a))
	b := &model.ModelEvalRank{ChannelID: 970101, ChannelModelID: 101, ModelName: "ok-mv", Outcome: model.ModelEvalOK}
	require.NoError(t, ModelEvalRankUpsert(ctx, b))
	require.Less(t, a.Position, b.Position)

	// b 向前移动（direction=-1），应与 a 交换，不应报错。
	list, err := ModelEvalRankMove(ctx, b.ID, -1)
	require.NoError(t, err, "violation 条目应可移动")

	var afterA model.ModelEvalRankSummary
	for _, it := range list {
		if it.ID == a.ID {
			afterA = it
		}
	}
	assert.Equal(t, b.Position, afterA.Position, "a 应拿到 b 原位置")
}

// TestModelEvalRankManualAdd 验证手动加入排序：写入 manual 条目、空内容、
// 同目标幂等不重复、停用渠道拒绝。
func TestModelEvalRankManualAdd(t *testing.T) {
	ctx := context.Background()

	ch, cmIDs := createChannelForEval(t, "manual-ch", "manual-model")
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })
	cmID := cmIDs[0]

	// 首次手动加入：应写入一条 manual 条目，空内容。
	list, err := ModelEvalRankManualAdd(ctx, []int{cmID})
	require.NoError(t, err)
	var found *model.ModelEvalRankSummary
	for i := range list {
		if list[i].ChannelID == ch.ID && list[i].ModelName == "manual-model" {
			found = &list[i]
			break
		}
	}
	require.NotNil(t, found, "manual 条目应出现在排序列表")
	assert.Equal(t, model.ModelEvalManual, found.Outcome)
	assert.Equal(t, ch.Name, found.ChannelName)
	assert.Equal(t, ch.Type, found.ChannelType)

	rec, err := ModelEvalRankContent(ctx, found.ID)
	require.NoError(t, err)
	assert.Empty(t, rec.Content, "手动加入条目不产生回复内容")
	assert.Equal(t, int64(0), rec.SourceEvalID, "手动加入条目没有来源评估记录")

	// 同 channel_model_id 再次手动加入：幂等，不重复追加。
	list2, err := ModelEvalRankManualAdd(ctx, []int{cmID})
	require.NoError(t, err)
	var count int
	for _, it := range list2 {
		if it.ChannelID == ch.ID && it.ModelName == "manual-model" {
			count++
		}
	}
	assert.Equal(t, 1, count, "重复手动加入不应产生重复排序条目")

	// 停用渠道后，手动加入应被拒绝。
	enabled := false
	_, err = ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Enabled: &enabled}, ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		en := true
		_, _ = ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Enabled: &en}, ctx)
	})
	_, err = ModelEvalRankManualAdd(ctx, []int{cmID})
	assert.ErrorIs(t, err, ErrEvalRankModelUnavailable)
}

// TestModelEvalRankFromHistoryAcceptsViolation 验证从历史加入 violation 评估时成功入排序。
func TestModelEvalRankFromHistoryAcceptsViolation(t *testing.T) {
	ctx := context.Background()
	// 先创建渠道和渠道模型（FromHistory 会校验渠道存在且启用）；用 ChannelCreate 以刷新缓存。
	ch := &model.Channel{Name: "vio-ch", Type: "openai", Enabled: true, BaseURL: "https://api.openai.com", Key: "sk-test", Models: []model.ChannelModel{{Name: "vio-model"}}}
	require.NoError(t, ChannelCreate(ch, ctx))
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, ch.ID)
		cleanupRanksByChannel(t, ch.ID)
	})

	eval := &model.ModelEval{
		ModelEvalSummary: model.ModelEvalSummary{
			ChannelID: ch.ID, ChannelModelID: ch.Models[0].ID, ChannelName: ch.Name, ChannelType: ch.Type, ModelName: "vio-model",
			Outcome: model.ModelEvalViolation,
		},
		Content: "some content without markers",
	}
	require.NoError(t, db.GetDB().Create(eval).Error)
	t.Cleanup(func() { db.GetDB().Delete(&model.ModelEval{}, eval.ID) })

	list, err := ModelEvalRankFromHistory(ctx, eval.ID)
	require.NoError(t, err, "violation 评估应可从历史加入排序")
	var found bool
	for _, it := range list {
		if it.ChannelID == ch.ID && it.Outcome == model.ModelEvalViolation {
			found = true
		}
	}
	assert.True(t, found, "violation 条目应出现在排序列表")
}
