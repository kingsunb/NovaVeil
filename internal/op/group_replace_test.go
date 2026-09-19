package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupGroup 删除指定分组及其成员并重建分组缓存，保证后续测试不串味。
func cleanupGroup(t *testing.T, groupID int) {
	t.Helper()
	_ = db.GetDB().Where("group_id = ?", groupID).Delete(&model.GroupItem{}).Error
	_ = db.GetDB().Delete(&model.Group{}, groupID).Error
	_ = groupRefreshCache(context.Background())
}

// TestGroupReplaceItemsByNameCreatesGroup 验证新建分组：可入组条目按 position 升序映射为
// priority 递减，分组模式为 failover。
func TestGroupReplaceItemsByNameCreatesGroup(t *testing.T) {
	ctx := context.Background()
	chA, cmA := createChannelForEval(t, "grp-a", "grp-model-a")
	chB, cmB := createChannelForEval(t, "grp-b", "grp-model-b")

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cmA[0], ChannelID: chA.ID, ChannelName: chA.Name, ModelName: "grp-model-a", Outcome: model.ModelEvalOK, Position: 0},
		{ChannelModelID: cmB[0], ChannelID: chB.ID, ChannelName: chB.Name, ModelName: "grp-model-b", Outcome: model.ModelEvalViolation, Position: 1},
	}
	g, _, created, err := GroupReplaceItemsByName(ctx, "grp-replace-new", ranks)
	require.NoError(t, err)
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })

	assert.True(t, created, "新名称应创建分组")
	assert.Equal(t, model.GroupModeFailover, g.Mode)
	require.Len(t, g.Items, 2)

	// position 越小越靠前 → priority 越小越优先（路由先选）。两条 priority 应为 {1, 2}。
	prios := map[int]int{}
	for _, it := range g.Items {
		prios[it.ChannelModelID] = it.Priority
	}
	assert.Equal(t, 1, prios[cmA[0]], "position=0 应得最小 priority（路由优先选）")
	assert.Equal(t, 2, prios[cmB[0]], "position=1 应得次小 priority")
}

// TestGroupReplaceItemsByNameReplacesExisting 验证已存在分组被整体替换成员。
func TestGroupReplaceItemsByNameReplacesExisting(t *testing.T) {
	ctx := context.Background()
	chA, cmA := createChannelForEval(t, "grp-re-a", "grp-re-model-a")
	chB, cmB := createChannelForEval(t, "grp-re-b", "grp-re-model-b")

	first := []model.ModelEvalRankSummary{
		{ChannelModelID: cmA[0], ChannelID: chA.ID, ChannelName: chA.Name, ModelName: "grp-re-model-a", Outcome: model.ModelEvalOK, Position: 0},
		{ChannelModelID: cmB[0], ChannelID: chB.ID, ChannelName: chB.Name, ModelName: "grp-re-model-b", Outcome: model.ModelEvalOK, Position: 1},
	}
	g1, _, created1, err := GroupReplaceItemsByName(ctx, "grp-replace-existing", first)
	require.NoError(t, err)
	require.True(t, created1)
	t.Cleanup(func() { cleanupGroup(t, g1.ID) })

	// 第二次仅保留一个成员，应替换而非追加。
	second := []model.ModelEvalRankSummary{
		{ChannelModelID: cmB[0], ChannelID: chB.ID, ChannelName: chB.Name, ModelName: "grp-re-model-b", Outcome: model.ModelEvalOK, Position: 0},
	}
	g2, _, created2, err := GroupReplaceItemsByName(ctx, "grp-replace-existing", second)
	require.NoError(t, err)
	assert.False(t, created2, "已存在名称不应再新建")
	assert.Equal(t, g1.ID, g2.ID)
	require.Len(t, g2.Items, 1, "成员应被整体替换为 1 条")
	assert.Equal(t, cmB[0], g2.Items[0].ChannelModelID)
}

// TestGroupReplaceItemsByNameSkipsErrors 验证 error 条目被过滤，仅可入组条目入分组。
func TestGroupReplaceItemsByNameSkipsErrors(t *testing.T) {
	ctx := context.Background()
	chA, cmA := createChannelForEval(t, "grp-skip-a", "grp-skip-model-a")

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cmA[0], ChannelID: chA.ID, ChannelName: chA.Name, ModelName: "grp-skip-model-a", Outcome: model.ModelEvalOK, Position: 0},
		{ChannelModelID: 999999, ChannelID: 999999, ChannelName: "ghost", ModelName: "ghost-model", Outcome: model.ModelEvalError, Position: 1},
	}
	g, _, _, err := GroupReplaceItemsByName(ctx, "grp-replace-skip", ranks)
	require.NoError(t, err)
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })
	require.Len(t, g.Items, 1, "error 条目应被过滤")
	assert.Equal(t, cmA[0], g.Items[0].ChannelModelID)
}

// TestGroupReplaceItemsByNameNoRankable 验证全部为 error 时返回 ErrGroupReplaceNoRankable。
func TestGroupReplaceItemsByNameNoRankable(t *testing.T) {
	ctx := context.Background()
	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: 1, Outcome: model.ModelEvalError, Position: 0},
		{ChannelModelID: 2, Outcome: model.ModelEvalError, Position: 1},
	}
	_, _, _, err := GroupReplaceItemsByName(ctx, "grp-replace-empty", ranks)
	assert.ErrorIs(t, err, ErrGroupReplaceNoRankable)
}

// TestGroupReplaceItemsByNameDisabledChannel 验证渠道已停用但模型仍在时,
// 该模型被保留入组且 priority 保持原排名, 不再整体中止。
func TestGroupReplaceItemsByNameDisabledChannel(t *testing.T) {
	ctx := context.Background()
	// 创建一个启用渠道拿到模型 ID，再停用它。
	ch, cm := createChannelForEval(t, "grp-disabled", "grp-disabled-model")
	enabled := false
	_, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Enabled: &enabled}, ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		// 恢复启用以走 createChannelForEval 的统一清理。
		en := true
		_, _ = ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Enabled: &en}, ctx)
	})

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-disabled-model", Outcome: model.ModelEvalOK, Position: 0},
	}
	g, report, _, err := GroupReplaceItemsByName(ctx, "grp-replace-disabled", ranks)
	require.NoError(t, err, "渠道停用但模型仍在不应整体中止")
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })

	// 分组包含该模型且 priority 保持原排名(position=0 → priority=1)。
	require.Len(t, g.Items, 1)
	assert.Equal(t, cm[0], g.Items[0].ChannelModelID)
	assert.Equal(t, 1, g.Items[0].Priority)

	// report.KeptDisabled 含该模型, 且未产生失效清理。
	require.Len(t, report.KeptDisabled, 1)
	assert.Equal(t, cm[0], report.KeptDisabled[0].ChannelModelID)
	assert.Equal(t, "grp-disabled-model", report.KeptDisabled[0].ModelName)
	assert.Empty(t, report.CleanedStale)
}

// TestGroupReplaceItemsByNameTrimsName 验证分组名被 trim，前后空格不影响命中。
func TestGroupReplaceItemsByNameTrimsName(t *testing.T) {
	ctx := context.Background()
	chA, cmA := createChannelForEval(t, "grp-trim-a", "grp-trim-model-a")

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cmA[0], ChannelID: chA.ID, ChannelName: chA.Name, ModelName: "grp-trim-model-a", Outcome: model.ModelEvalOK, Position: 0},
	}
	g1, _, _, err := GroupReplaceItemsByName(ctx, "  grp-trim-name  ", ranks)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupGroup(t, g1.ID) })
	assert.Equal(t, "grp-trim-name", g1.Name)

	// 用同样带空格的名称再次调用应命中已存在分组（created=false）。
	g2, _, created2, err := GroupReplaceItemsByName(ctx, "grp-trim-name", ranks)
	require.NoError(t, err)
	assert.False(t, created2)
	assert.Equal(t, g1.ID, g2.ID)
}

// TestGroupReplaceItemsByNameCleansStaleModel 验证失效模型(渠道模型已删除/改名)被清理:
// 对应 model_eval_ranks 行被删、不进入分组、report.CleanedStale 说明, 全程不整体中止。
// 对齐 spec 5.1.1 规则 3 与 5.1.3 场景 2/3。
func TestGroupReplaceItemsByNameCleansStaleModel(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "grp-stale", "grp-stale-ok")

	// 直接落库一条引用不存在 channel_model_id 的排序记录, 模拟模型已删除残留。
	stale := &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: 999999999, ChannelName: ch.Name,
		ModelName: "grp-stale-ghost", Outcome: model.ModelEvalOK, Position: 100,
	}
	require.NoError(t, db.GetDB().Create(stale).Error)
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	ranks := []model.ModelEvalRankSummary{
		{ID: stale.ID, ChannelModelID: 999999999, ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-stale-ghost", Outcome: model.ModelEvalOK, Position: 1},
		{ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-stale-ok", Outcome: model.ModelEvalOK, Position: 0},
	}
	g, report, _, err := GroupReplaceItemsByName(ctx, "grp-stale-group", ranks)
	require.NoError(t, err, "失效模型不应阻断整体更新")
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })

	// 失效排序行被删除。
	var staleCount int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).Where("id = ?", stale.ID).Count(&staleCount).Error)
	assert.Zero(t, staleCount, "失效排序行应被删除")

	// 不进入分组, 仅有效模型入组。
	require.Len(t, g.Items, 1)
	assert.Equal(t, cm[0], g.Items[0].ChannelModelID)

	// report.CleanedStale 含该失效记录。
	require.Len(t, report.CleanedStale, 1)
	assert.Equal(t, 999999999, report.CleanedStale[0].ChannelModelID)
	assert.Equal(t, "grp-stale-ghost", report.CleanedStale[0].ModelName)
	assert.Empty(t, report.KeptDisabled)
}

// TestGroupReplaceItemsByNameMixedSkipsNothing 验证「渠道停用(模型在) + 模型已删除 + 正常模型」
// 混合时逐条容错: 正常全入组、停用保留且 priority 不变、删除被清理, report 逐类说明。
// 对齐 spec 5.1.1 规则 4 与 5.1.3 场景 5。
func TestGroupReplaceItemsByNameMixedSkipsNothing(t *testing.T) {
	ctx := context.Background()
	chOK, cmOK := createChannelForEval(t, "grp-mix-ok", "grp-mix-ok-model")
	chDis, cmDis := createChannelForEval(t, "grp-mix-dis", "grp-mix-dis-model")

	// 停用渠道但模型仍在。
	enabled := false
	_, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: chDis.ID, Enabled: &enabled}, ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		en := true
		_, _ = ChannelUpdate(&model.ChannelUpdateRequest{ID: chDis.ID, Enabled: &en}, ctx)
	})

	// 引用不存在 channel_model_id 的排序记录, 模拟模型已删除。
	stale := &model.ModelEvalRank{
		ChannelID: chOK.ID, ChannelModelID: 888888888, ChannelName: chOK.Name,
		ModelName: "grp-mix-stale", Outcome: model.ModelEvalOK, Position: 100,
	}
	require.NoError(t, db.GetDB().Create(stale).Error)
	t.Cleanup(func() { cleanupRanksByChannel(t, chOK.ID, chDis.ID) })

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cmDis[0], ChannelID: chDis.ID, ChannelName: chDis.Name, ModelName: "grp-mix-dis-model", Outcome: model.ModelEvalOK, Position: 0},
		{ChannelModelID: cmOK[0], ChannelID: chOK.ID, ChannelName: chOK.Name, ModelName: "grp-mix-ok-model", Outcome: model.ModelEvalOK, Position: 1},
		{ID: stale.ID, ChannelModelID: 888888888, ChannelID: chOK.ID, ChannelName: chOK.Name, ModelName: "grp-mix-stale", Outcome: model.ModelEvalOK, Position: 2},
	}
	g, report, _, err := GroupReplaceItemsByName(ctx, "grp-mix-group", ranks)
	require.NoError(t, err, "混合场景不应整体中止")
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })

	// 正常模型 + 停用模型共 2 个成员。
	require.Len(t, g.Items, 2)
	prios := map[int]int{}
	for _, it := range g.Items {
		prios[it.ChannelModelID] = it.Priority
	}
	assert.Equal(t, 1, prios[cmDis[0]], "停用模型应保留且 priority 保持原排名")
	assert.Equal(t, 2, prios[cmOK[0]], "正常模型应入组且 priority 次之")

	// 失效模型被清理。
	var staleCount int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).Where("id = ?", stale.ID).Count(&staleCount).Error)
	assert.Zero(t, staleCount, "失效排序行应被删除")

	// report 逐类说明。
	require.Len(t, report.KeptDisabled, 1)
	assert.Equal(t, cmDis[0], report.KeptDisabled[0].ChannelModelID)
	require.Len(t, report.CleanedStale, 1)
	assert.Equal(t, 888888888, report.CleanedStale[0].ChannelModelID)
}

// TestGroupReplaceItemsByNameAllStaleOnlyCleans 验证排序仅含失效模型时:
// 返回 ErrGroupReplaceNoRankable、不产生空分组、失效排序行被删除。
// 对齐 spec 5.1.1 规则 6 与 5.1.3 场景 6。
func TestGroupReplaceItemsByNameAllStaleOnlyCleans(t *testing.T) {
	ctx := context.Background()
	ch, _ := createChannelForEval(t, "grp-allstale", "grp-allstale-ok")

	stale := &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: 777777777, ChannelName: ch.Name,
		ModelName: "grp-allstale-stale", Outcome: model.ModelEvalOK, Position: 100,
	}
	require.NoError(t, db.GetDB().Create(stale).Error)
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	ranks := []model.ModelEvalRankSummary{
		{ID: stale.ID, ChannelModelID: 777777777, ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-allstale-stale", Outcome: model.ModelEvalOK, Position: 0},
	}
	g, report, created, err := GroupReplaceItemsByName(ctx, "grp-allstale-group", ranks)
	assert.ErrorIs(t, err, ErrGroupReplaceNoRankable)
	assert.Nil(t, g)
	assert.False(t, created)

	// 失效排序行被幂等清理。
	var staleCount int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).Where("id = ?", stale.ID).Count(&staleCount).Error)
	assert.Zero(t, staleCount, "全部失效时失效排序行仍应被清理")

	require.Len(t, report.CleanedStale, 1)

	// 不产生空分组。
	var groupCount int64
	require.NoError(t, db.GetDB().Model(&model.Group{}).Where("name = ?", "grp-allstale-group").Count(&groupCount).Error)
	assert.Zero(t, groupCount, "不应产生空分组")
}

// TestGroupReplaceItemsByNameRollbackAtomic 验证事务原子性: 写库失败触发整体回滚,
// 失效排序行与分组成员均保持更新前状态。
// 做法: 事务内「清空旧成员」前临时删除 group_items 表迫使该步失败(项目既有手法,
// 见 usage_bucket_test.go), 断言回滚后失效排序未删、分组缓存成员未变。
// 对齐 spec 5.1.1 规则 6。
func TestGroupReplaceItemsByNameRollbackAtomic(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "grp-rollback", "grp-rollback-model")

	// 先建立含 1 个有效成员的分组。
	initial := []model.ModelEvalRankSummary{
		{ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-rollback-model", Outcome: model.ModelEvalOK, Position: 0},
	}
	g1, _, _, err := GroupReplaceItemsByName(ctx, "grp-rollback-group", initial)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupGroup(t, g1.ID) })

	// 引用不存在模型的失效排序。
	stale := &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: 666666666, ChannelName: ch.Name,
		ModelName: "grp-rollback-stale", Outcome: model.ModelEvalOK, Position: 100,
	}
	require.NoError(t, db.GetDB().Create(stale).Error)
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	// 临时删除 group_items 表迫使事务内写库失败。
	require.NoError(t, db.GetDB().Migrator().DropTable(&model.GroupItem{}))
	t.Cleanup(func() {
		require.NoError(t, db.GetDB().AutoMigrate(&model.GroupItem{}))
	})

	ranks := []model.ModelEvalRankSummary{
		{ID: stale.ID, ChannelModelID: 666666666, ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-rollback-stale", Outcome: model.ModelEvalOK, Position: 1},
		{ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-rollback-model", Outcome: model.ModelEvalOK, Position: 0},
	}
	_, _, _, err = GroupReplaceItemsByName(ctx, "grp-rollback-group", ranks)
	require.Error(t, err, "写库失败应返回错误")

	// 回滚后失效排序行仍在(未被部分提交删除)。
	var staleCount int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).Where("id = ?", stale.ID).Count(&staleCount).Error)
	assert.Equal(t, int64(1), staleCount, "失效排序应随事务回滚保留")

	// 分组缓存保持更新前状态(成员未被部分写入)。
	cached, err := GroupGetByName("grp-rollback-group")
	require.NoError(t, err)
	assert.Len(t, cached.Items, 1, "分组成员应保持更新前状态")
	assert.Equal(t, cm[0], cached.Items[0].ChannelModelID)
}

// TestGroupReplaceItemsByNameRevalidatesAllStaleInTx 验证事务内按库复核:
// 分类读缓存仍命中、但 channel_models 行已被并发删除事务(绕过缓存失效)落库删除的
// 成员在事务内被降级清理; 全部降级时仅清理并返回 ErrGroupReplaceNoRankable,
// 不留下悬空分组成员, 也不产生空分组。
// 对齐 spec 5.1.1 规则 6 的竞态闭合要求。
func TestGroupReplaceItemsByNameRevalidatesAllStaleInTx(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "grp-reval-stale", "grp-reval-stale-model")
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	rank := &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: cm[0], ChannelName: ch.Name,
		ModelName: "grp-reval-stale-model", Outcome: model.ModelEvalOK, Content: "c",
	}
	require.NoError(t, ModelEvalRankUpsert(ctx, rank))

	// 绕过 op 层直接删除 channel_models 行, 模拟「删除事务提交晚于缓存失效」:
	// 分类时 ChannelModelGet 仍命中缓存, 事务内按库复核才暴露失效。
	require.NoError(t, db.GetDB().Where("id = ?", cm[0]).Delete(&model.ChannelModel{}).Error)

	ranks := []model.ModelEvalRankSummary{
		{ID: rank.ID, ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-reval-stale-model", Outcome: model.ModelEvalOK, Position: 0},
	}
	g, report, created, err := GroupReplaceItemsByName(ctx, "grp-reval-stale-group", ranks)
	assert.ErrorIs(t, err, ErrGroupReplaceNoRankable)
	assert.Nil(t, g)
	assert.False(t, created)

	require.Len(t, report.CleanedStale, 1, "缓存命中但库中已删的成员应在事务内被降级清理")
	assert.Equal(t, cm[0], report.CleanedStale[0].ChannelModelID)
	assert.Empty(t, report.KeptDisabled)

	// 排序行被清理, 不产生空分组。
	var rankCount int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalRank{}).Where("channel_model_id = ?", cm[0]).Count(&rankCount).Error)
	assert.Zero(t, rankCount, "复核降级的排序行应被删除")
	var groupCount int64
	require.NoError(t, db.GetDB().Model(&model.Group{}).Where("name = ?", "grp-reval-stale-group").Count(&groupCount).Error)
	assert.Zero(t, groupCount, "不应产生空分组")
}

// TestGroupReplaceItemsByNameRevalidatesMixedInTx 验证复核降级只影响库中已删的成员:
// 存活成员按原顺序前移补位(priority 重排), 降级成员进入 CleanedStale 而不入组。
func TestGroupReplaceItemsByNameRevalidatesMixedInTx(t *testing.T) {
	ctx := context.Background()
	ch, cm := createChannelForEval(t, "grp-reval-mix", "grp-reval-mix-a", "grp-reval-mix-b")
	t.Cleanup(func() { cleanupRanksByChannel(t, ch.ID) })

	// cm[0] 库中删除(缓存仍命中), cm[1] 存活。
	require.NoError(t, db.GetDB().Where("id = ?", cm[0]).Delete(&model.ChannelModel{}).Error)

	ranks := []model.ModelEvalRankSummary{
		{ChannelModelID: cm[0], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-reval-mix-a", Outcome: model.ModelEvalOK, Position: 0},
		{ChannelModelID: cm[1], ChannelID: ch.ID, ChannelName: ch.Name, ModelName: "grp-reval-mix-b", Outcome: model.ModelEvalOK, Position: 1},
	}
	g, report, _, err := GroupReplaceItemsByName(ctx, "grp-reval-mix-group", ranks)
	require.NoError(t, err)
	require.NotNil(t, g)
	t.Cleanup(func() { cleanupGroup(t, g.ID) })

	// 仅存活成员入组并前移到 priority 1。
	require.Len(t, g.Items, 1)
	assert.Equal(t, cm[1], g.Items[0].ChannelModelID)
	assert.Equal(t, 1, g.Items[0].Priority)

	// 降级成员进入 CleanedStale。
	require.Len(t, report.CleanedStale, 1)
	assert.Equal(t, cm[0], report.CleanedStale[0].ChannelModelID)
	assert.Empty(t, report.KeptDisabled)
}
