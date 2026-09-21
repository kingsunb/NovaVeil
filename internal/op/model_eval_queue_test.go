package op

import (
	"context"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createChannelForEval 创建一个启用渠道并挂载指定模型，供队列/分组测试使用。
// 返回渠道与模型主键；测试结束自动清理渠道、模型与缓存。
func createChannelForEval(t *testing.T, name string, modelNames ...string) (model.Channel, []int) {
	t.Helper()
	ctx := context.Background()
	models := make([]model.ChannelModel, len(modelNames))
	for i, n := range modelNames {
		models[i] = model.ChannelModel{Name: n}
	}
	ch := model.Channel{
		Name:    name,
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://example.invalid",
		Keys:    []model.ChannelKey{{Key: "sk-test-" + name}},
		Models:  models,
	}
	require.NoError(t, ChannelCreate(&ch, ctx))
	cmIDs := make([]int, len(ch.Models))
	for i := range ch.Models {
		cmIDs[i] = ch.Models[i].ID
	}
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", ch.ID).Delete(&model.ChannelModel{}).Error
		_ = db.GetDB().Delete(&model.Channel{}, ch.ID).Error
		channelCache.Del(ch.ID)
		for _, id := range cmIDs {
			channelModelCache.Del(id)
		}
	})
	return ch, cmIDs
}

// cleanupQueueTasks 删除指定主键的队列任务。
func cleanupQueueTasks(t *testing.T, ids ...int64) {
	t.Helper()
	if len(ids) == 0 {
		return
	}
	require.NoError(t, db.GetDB().Where("id IN ?", ids).Delete(&model.ModelEvalQueueTask{}).Error)
}

// TestModelEvalQueueEnqueueCreatesTasks 验证入队按顺序分配 position 并返回新建任务。
func TestModelEvalQueueEnqueueCreatesTasks(t *testing.T) {
	ctx := context.Background()
	ch, cmIDs := createChannelForEval(t, "q-enqueue", "q-model-a", "q-model-b")

	tasks, err := ModelEvalQueueEnqueue(ctx, cmIDs)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	t.Cleanup(func() {
		ids := make([]int64, len(tasks))
		for i, tk := range tasks {
			ids[i] = tk.ID
		}
		cleanupQueueTasks(t, ids...)
	})

	assert.Equal(t, model.QueueTaskQueued, tasks[0].Status)
	assert.Equal(t, model.QueueTaskQueued, tasks[1].Status)
	assert.Less(t, tasks[0].Position, tasks[1].Position)
	assert.Equal(t, ch.ID, tasks[0].ChannelID)
}

// TestModelEvalQueueEnqueueEmpty 验证空入队返回空切片而非 nil。
func TestModelEvalQueueEnqueueEmpty(t *testing.T) {
	tasks, err := ModelEvalQueueEnqueue(context.Background(), nil)
	require.NoError(t, err)
	assert.NotNil(t, tasks)
	assert.Empty(t, tasks)
}

// TestModelEvalQueueEnqueueDedupActive 验证已存在 queued/running 的 (channel,model) 被跳过。
func TestModelEvalQueueEnqueueDedupActive(t *testing.T) {
	ctx := context.Background()
	_, cmIDs := createChannelForEval(t, "q-dedup", "q-dedup-model")

	first, err := ModelEvalQueueEnqueue(ctx, cmIDs)
	require.NoError(t, err)
	require.Len(t, first, 1)
	t.Cleanup(func() { cleanupQueueTasks(t, first[0].ID) })

	// 再次入队同一模型，应被去重跳过。
	second, err := ModelEvalQueueEnqueue(ctx, cmIDs)
	require.NoError(t, err)
	assert.Empty(t, second, "重复的 queued 任务应被跳过")
}

// TestModelEvalQueuePopNextOrdersByPosition 验证按 position 升序弹出并置 running。
func TestModelEvalQueuePopNextOrdersByPosition(t *testing.T) {
	ctx := context.Background()
	_, cmIDs := createChannelForEval(t, "q-pop", "q-pop-a", "q-pop-b", "q-pop-c")

	all, err := ModelEvalQueueEnqueue(ctx, cmIDs)
	require.NoError(t, err)
	require.Len(t, all, 3)
	t.Cleanup(func() {
		ids := make([]int64, len(all))
		for i, tk := range all {
			ids[i] = tk.ID
		}
		cleanupQueueTasks(t, ids...)
	})

	popped, err := ModelEvalQueuePopNext(ctx, 2)
	require.NoError(t, err)
	require.Len(t, popped, 2)
	// 应弹出 position 最小的两条。
	assert.Equal(t, all[0].ID, popped[0].ID)
	assert.Equal(t, all[1].ID, popped[1].ID)

	// 验证已置 running。
	for _, tk := range popped {
		var got model.ModelEvalQueueTask
		require.NoError(t, db.GetDB().First(&got, tk.ID).Error)
		assert.Equal(t, model.QueueTaskRunning, got.Status)
	}
}

// TestModelEvalQueuePopNextEmpty 验证无 queued 任务时返回空。
func TestModelEvalQueuePopNextEmpty(t *testing.T) {
	popped, err := ModelEvalQueuePopNext(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, popped)
}

// TestModelEvalQueueListExcludesFinished 验证列表只返回 queued/running，
// done/stopped 任务完成或失败后即从队列视图消失（结果仍可在评估历史查看）。
func TestModelEvalQueueListExcludesFinished(t *testing.T) {
	ctx := context.Background()
	queued := insertQueuedTask(t, 980080, 1, 100, "qlist-q")
	running := insertQueuedTask(t, 980081, 2, 200, "qlist-r")
	done := insertQueuedTask(t, 980082, 3, 300, "qlist-d")
	stopped := insertQueuedTask(t, 980083, 4, 400, "qlist-s")
	t.Cleanup(func() { cleanupQueueTasks(t, queued.ID, running.ID, done.ID, stopped.ID) })
	setStatus := func(id int64, st model.QueueTaskStatus) {
		require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", id).Update("status", st).Error)
	}
	setStatus(running.ID, model.QueueTaskRunning)
	setStatus(done.ID, model.QueueTaskDone)
	setStatus(stopped.ID, model.QueueTaskStopped)

	list, err := ModelEvalQueueList(ctx)
	require.NoError(t, err)
	gotIDs := make(map[int64]struct{}, len(list))
	for _, item := range list {
		gotIDs[item.ID] = struct{}{}
	}
	assert.Contains(t, gotIDs, queued.ID, "queued 任务应出现在列表中")
	assert.Contains(t, gotIDs, running.ID, "running 任务应出现在列表中")
	assert.NotContains(t, gotIDs, done.ID, "done 任务应从列表消失")
	assert.NotContains(t, gotIDs, stopped.ID, "stopped 任务应从列表消失")
}

// insertQueuedTask 直接落库一条 queued 任务，用于测试 move/stop/clear 而不经由 Enqueue。
func insertQueuedTask(t *testing.T, chID, cmID int, pos int, name string) *model.ModelEvalQueueTask {
	t.Helper()
	tk := &model.ModelEvalQueueTask{
		ChannelID:      chID,
		ChannelModelID: cmID,
		ChannelName:    name,
		ModelName:      name + "-model",
		Status:         model.QueueTaskQueued,
		Position:       pos,
	}
	require.NoError(t, db.GetDB().Create(tk).Error)
	return tk
}

// TestModelEvalQueueMoveUpSwapsWithPrevious 验证上移与前一个 queued 任务交换 position。
func TestModelEvalQueueMoveUpSwapsWithPrevious(t *testing.T) {
	ctx := context.Background()
	a := insertQueuedTask(t, 980001, 1, 100, "qmu-a")
	b := insertQueuedTask(t, 980002, 2, 200, "qmu-b")
	t.Cleanup(func() { cleanupQueueTasks(t, a.ID, b.ID) })

	list, err := ModelEvalQueueMoveUp(ctx, b.ID)
	require.NoError(t, err)
	var afterA, afterB model.ModelEvalQueueTask
	for _, tk := range list {
		if tk.ID == a.ID {
			afterA = tk
		}
		if tk.ID == b.ID {
			afterB = tk
		}
	}
	assert.Equal(t, b.Position, afterA.Position)
	assert.Equal(t, a.Position, afterB.Position)
}

// TestModelEvalQueueMoveUpAtHead 验证队首上移返回边界错误。
func TestModelEvalQueueMoveUpAtHead(t *testing.T) {
	ctx := context.Background()
	a := insertQueuedTask(t, 980010, 1, 100, "qmu-head")
	t.Cleanup(func() { cleanupQueueTasks(t, a.ID) })

	_, err := ModelEvalQueueMoveUp(ctx, a.ID)
	assert.ErrorIs(t, err, ErrEvalQueueMoveBounds)
}

// TestModelEvalQueueMoveUpRejectsRunning 验证 running 任务不可上移。
func TestModelEvalQueueMoveUpRejectsRunning(t *testing.T) {
	ctx := context.Background()
	tk := insertQueuedTask(t, 980020, 1, 100, "qmu-running")
	tk.Status = model.QueueTaskRunning
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", tk.ID).Update("status", model.QueueTaskRunning).Error)
	t.Cleanup(func() { cleanupQueueTasks(t, tk.ID) })

	_, err := ModelEvalQueueMoveUp(ctx, tk.ID)
	assert.ErrorIs(t, err, ErrEvalQueueTaskConflict)
}

// TestModelEvalQueueStopQueued 验证停止 queued 任务置 stopped，且从队列列表消失。
func TestModelEvalQueueStopQueued(t *testing.T) {
	ctx := context.Background()
	tk := insertQueuedTask(t, 980030, 1, 100, "qstop")
	t.Cleanup(func() { cleanupQueueTasks(t, tk.ID) })

	list, err := ModelEvalQueueStop(ctx, tk.ID)
	require.NoError(t, err)
	// 队列列表仅返回 queued/running，stopped 任务应已消失。
	for _, item := range list {
		assert.NotEqual(t, tk.ID, item.ID, "stopped 任务不应出现在队列列表中")
	}
	// 落库状态仍为 stopped，供审计追溯。
	var got model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&got, tk.ID).Error)
	assert.Equal(t, model.QueueTaskStopped, got.Status)
}

// TestModelEvalQueueStopRejectsRunning 验证 running 任务不可停止。
func TestModelEvalQueueStopRejectsRunning(t *testing.T) {
	ctx := context.Background()
	tk := insertQueuedTask(t, 980040, 1, 100, "qstop-run")
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", tk.ID).Update("status", model.QueueTaskRunning).Error)
	t.Cleanup(func() { cleanupQueueTasks(t, tk.ID) })

	_, err := ModelEvalQueueStop(ctx, tk.ID)
	assert.ErrorIs(t, err, ErrEvalQueueTaskConflict)
}

// TestModelEvalQueueClearRemovesOnlyQueued 验证清空只删 queued，不影响 running/done/stopped。
func TestModelEvalQueueClearRemovesOnlyQueued(t *testing.T) {
	ctx := context.Background()
	queued := insertQueuedTask(t, 980050, 1, 100, "qclear-q")
	running := insertQueuedTask(t, 980051, 2, 200, "qclear-r")
	t.Cleanup(func() { cleanupQueueTasks(t, running.ID) })
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", running.ID).Update("status", model.QueueTaskRunning).Error)

	deleted, err := ModelEvalQueueClear(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, 1)

	var queuedExists int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", queued.ID).Count(&queuedExists).Error)
	assert.Equal(t, int64(0), queuedExists, "queued 应被删除")

	var runningExists int64
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", running.ID).Count(&runningExists).Error)
	assert.Equal(t, int64(1), runningExists, "running 不应被删除")
}

// TestModelEvalQueueMarkDone 验证标记完成写入状态、eval_id 并脱敏错误。
func TestModelEvalQueueMarkDone(t *testing.T) {
	ctx := context.Background()
	tk := insertQueuedTask(t, 980060, 1, 100, "qdone")
	t.Cleanup(func() { cleanupQueueTasks(t, tk.ID) })

	require.NoError(t, ModelEvalQueueMarkDone(ctx, tk.ID, 12345, "Bearer sk-leak done"))

	var got model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&got, tk.ID).Error)
	assert.Equal(t, model.QueueTaskDone, got.Status)
	assert.Equal(t, int64(12345), got.EvalID)
	assert.Contains(t, got.Error, "[REDACTED]")
	assert.NotContains(t, got.Error, "sk-leak")
}

// TestModelEvalQueueResetRunning 验证启动恢复把遗留 running 重置回 queued。
func TestModelEvalQueueResetRunning(t *testing.T) {
	ctx := context.Background()
	tk := insertQueuedTask(t, 980070, 1, 100, "qreset")
	t.Cleanup(func() { cleanupQueueTasks(t, tk.ID) })
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", tk.ID).Update("status", model.QueueTaskRunning).Error)

	require.NoError(t, ModelEvalQueueResetRunning(ctx))

	var got model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&got, tk.ID).Error)
	assert.Equal(t, model.QueueTaskQueued, got.Status)
}

// TestModelEvalQueueRestartResetRecyclesUnexpiredLease 验证：租约还没过期时，
// 周期/多实例 ResetRunning 不会把 running 收回；单实例「进程重启后的 Reset」
// 会立刻退回 queued，而不是留到 15 分钟后再入队打第二次上游。
// 已 done 的行保持 done。本测试显式写入 StartedAt；只改 status 的旧用例不受影响。
func TestModelEvalQueueRestartResetRecyclesUnexpiredLease(t *testing.T) {
	ctx := context.Background()
	running := insertQueuedTask(t, 980090, 1, 100, "qrestart-run")
	done := insertQueuedTask(t, 980091, 2, 200, "qrestart-done")
	t.Cleanup(func() { cleanupQueueTasks(t, running.ID, done.ID) })

	started := time.Now().Add(-time.Minute)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", running.ID).Updates(map[string]interface{}{
		"status":     model.QueueTaskRunning,
		"started_at": started,
	}).Error)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", done.ID).Updates(map[string]interface{}{
		"status":     model.QueueTaskDone,
		"eval_id":    int64(77),
		"started_at": started,
	}).Error)

	var before model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&before, running.ID).Error)
	require.False(t, before.StartedAt.IsZero(), "租约测试必须写上 StartedAt；零值会被 ResetRunning 回收")
	require.Equal(t, model.QueueTaskRunning, before.Status)

	require.NoError(t, ModelEvalQueueResetRunning(ctx))
	var leased model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&leased, running.ID).Error)
	assert.Equal(t, model.QueueTaskRunning, leased.Status, "未过期租约不应被 ResetRunning 回收，否则 15 分钟内就会再跑一次")

	require.NoError(t, ModelEvalQueueResetRunningOnStart(ctx))
	var gotRun, gotDone model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&gotRun, running.ID).Error)
	require.NoError(t, db.GetDB().First(&gotDone, done.ID).Error)
	assert.Equal(t, model.QueueTaskDone, gotDone.Status, "已完成任务不能被启动回收再次入队")
	assert.Equal(t, int64(77), gotDone.EvalID)

	if evalQueueMultiInstance() {
		assert.Equal(t, model.QueueTaskRunning, gotRun.Status, "多实例启动不得抢走未过期 running")
		return
	}
	assert.Equal(t, model.QueueTaskQueued, gotRun.Status, "单实例重启应立即回收未过期 running，而不是留到 15 分钟后再跑第二次")
}

// TestModelEvalQueueRequeueRunningSkipsCompleted 验证停机退回只改仍为 running 的指定行。
func TestModelEvalQueueRequeueRunningSkipsCompleted(t *testing.T) {
	ctx := context.Background()
	running := insertQueuedTask(t, 980092, 1, 100, "qrequeue-run")
	done := insertQueuedTask(t, 980093, 2, 200, "qrequeue-done")
	other := insertQueuedTask(t, 980094, 3, 300, "qrequeue-other")
	t.Cleanup(func() { cleanupQueueTasks(t, running.ID, done.ID, other.ID) })

	started := time.Now().Add(-time.Minute)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", running.ID).Updates(map[string]interface{}{
		"status":     model.QueueTaskRunning,
		"started_at": started,
	}).Error)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", done.ID).Updates(map[string]interface{}{
		"status":     model.QueueTaskDone,
		"eval_id":    int64(88),
		"started_at": started,
	}).Error)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", other.ID).Updates(map[string]interface{}{
		"status":     model.QueueTaskRunning,
		"started_at": started,
	}).Error)

	require.NoError(t, ModelEvalQueueRequeueRunning(ctx, nil))
	require.NoError(t, ModelEvalQueueRequeueRunning(ctx, []int64{running.ID, done.ID}))

	var gotRun, gotDone, gotOther model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&gotRun, running.ID).Error)
	require.NoError(t, db.GetDB().First(&gotDone, done.ID).Error)
	require.NoError(t, db.GetDB().First(&gotOther, other.ID).Error)
	assert.Equal(t, model.QueueTaskQueued, gotRun.Status)
	assert.True(t, gotRun.StartedAt.IsZero(), "退回 queued 后不应留下未过期租约")
	assert.Equal(t, model.QueueTaskDone, gotDone.Status)
	assert.Equal(t, int64(88), gotDone.EvalID)
	assert.Equal(t, model.QueueTaskRunning, gotOther.Status, "未列入的 running 不属于本次停机")
}
