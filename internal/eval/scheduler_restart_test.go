package eval

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	os.Exit(m.Run())
}

// TestSchedulerStopRequeuesUnfinishedNotCompleted 验证停机：
// 上下文已取消时不再打上游、也不把任务 MarkDone；Stop 只把本进程仍为 running
// 的任务退回 queued。已完成的任务保持 done，其它实例的 running 不动。
func TestSchedulerStopRequeuesUnfinishedNotCompleted(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	running := insertEvalTask(t, 980100, 1, model.QueueTaskRunning, started)
	done := insertEvalTask(t, 980101, 2, model.QueueTaskDone, started)
	other := insertEvalTask(t, 980102, 3, model.QueueTaskRunning, started)
	require.NoError(t, db.GetDB().Model(&model.ModelEvalQueueTask{}).Where("id = ?", done.ID).Update("eval_id", int64(91)).Error)

	s := NewScheduler()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.cancel()
	require.True(t, s.executeTask(*running), "停机取消应留下 running，而不是定稿")

	var mid model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&mid, running.ID).Error)
	assert.Equal(t, model.QueueTaskRunning, mid.Status)
	var evals int64
	require.NoError(t, db.GetDB().Model(&model.ModelEval{}).Where("channel_id = ?", running.ChannelID).Count(&evals).Error)
	assert.Zero(t, evals, "未发出的评估不能记一条失败历史再重跑")

	s.started = true
	s.inflight = map[int64]struct{}{running.ID: {}, done.ID: {}}
	require.NoError(t, s.Stop())
	assert.False(t, s.started)

	var gotRun, gotDone, gotOther model.ModelEvalQueueTask
	require.NoError(t, db.GetDB().First(&gotRun, running.ID).Error)
	require.NoError(t, db.GetDB().First(&gotDone, done.ID).Error)
	require.NoError(t, db.GetDB().First(&gotOther, other.ID).Error)
	assert.Equal(t, model.QueueTaskQueued, gotRun.Status, "本进程未完成的 running 应退回 queued")
	assert.Equal(t, model.QueueTaskDone, gotDone.Status, "已完成任务不能在停机时再次入队")
	assert.Equal(t, int64(91), gotDone.EvalID)
	assert.Equal(t, model.QueueTaskRunning, gotOther.Status, "停机不能退回其它实例的 running")
}

func insertEvalTask(t *testing.T, chID, pos int, status model.QueueTaskStatus, started time.Time) *model.ModelEvalQueueTask {
	t.Helper()
	tk := &model.ModelEvalQueueTask{
		ChannelID:      chID,
		ChannelModelID: chID,
		ChannelName:    "sched-restart",
		ModelName:      "sched-restart-model",
		Status:         status,
		Position:       pos,
		StartedAt:      started,
	}
	require.NoError(t, db.GetDB().Create(tk).Error)
	t.Cleanup(func() {
		_ = db.GetDB().Where("id = ?", tk.ID).Delete(&model.ModelEvalQueueTask{}).Error
	})
	return tk
}
