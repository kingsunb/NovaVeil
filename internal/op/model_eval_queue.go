package op

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

// 队列操作的 sentinel 错误：handler 用 errors.Is 区分 4xx/5xx。
var (
	ErrEvalQueueModelUnavailable = errors.New("渠道或模型不可用")
	ErrEvalQueueTaskNotFound     = errors.New("队列任务不存在")
	ErrEvalQueueTaskConflict     = errors.New("任务状态冲突，仅待执行任务可操作")
	ErrEvalQueueMoveBounds       = errors.New("已到队首，无法继续上移")
)

// evalQueueRunningLease 是 running 任务的存活租约：worker 评估最长 10 分钟 +
// 10 秒保存余量，这里给 15 分钟。只有在租约过期后，ResetRunning 才会把遗留
// running 重置回 queued，避免多实例部署时新实例启动（或周期清扫）把另一个
// 仍在健康执行中的实例的任务抢走重复评估。
const evalQueueRunningLease = 15 * time.Minute

// evalQueueEnqueueMu 串行化本进程内的 Enqueue。多实例(MySQL/Postgres)下还由
// withEvalQueueEnqueueLock 取得数据库命名锁，保证跨实例也串行。
var evalQueueEnqueueMu sync.Mutex

const (
	// 评估队列入队锁的稳定名称 / PostgreSQL advisory lock key。
	evalQueueEnqueueLockName = "novaveil:model_eval_queue:enqueue"
	evalQueueEnqueueLockKey  = int64(0x4e_56_45_51_45) // "NVEQE" 的稳定算术 key。
)

// withEvalQueueEnqueueLock 在 enqueue 业务前取得跨实例串行锁：
//   - SQLite 单实例只需进程内互斥；SQLite 本身单写者，其它实例无法共享同一文件。
//   - MySQL 用 GET_LOCK 命名锁（与事务同一条连接，释放时机在事务提交后）。
//   - Postgres 用 pg_advisory_lock 会话锁（同样在同一条连接上释放）。
func withEvalQueueEnqueueLock(ctx context.Context, fn func(tx *gorm.DB) error) error {
	d := db.GetDB()
	evalQueueEnqueueMu.Lock()
	defer evalQueueEnqueueMu.Unlock()

	switch d.Dialector.Name() {
	case "mysql":
		return d.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var got int
			if err := conn.Raw("SELECT GET_LOCK(?, 10)", evalQueueEnqueueLockName).Scan(&got).Error; err != nil {
				return fmt.Errorf("acquire enqueue lock failed: %w", err)
			}
			if got != 1 {
				return fmt.Errorf("acquire enqueue lock timeout")
			}
			defer func() {
				// 连接尚未归还; 在同一会话内释放命名锁。
				if err := conn.Exec("SELECT RELEASE_LOCK(?)", evalQueueEnqueueLockName).Error; err != nil {
					log.Warnf("release enqueue lock failed: %v", err)
				}
			}()
			return conn.Transaction(fn)
		})
	case "postgres":
		return d.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			if err := conn.Exec("SELECT pg_advisory_lock(?)", evalQueueEnqueueLockKey).Error; err != nil {
				return fmt.Errorf("acquire enqueue lock failed: %w", err)
			}
			defer func() {
				if err := conn.Exec("SELECT pg_advisory_unlock(?)", evalQueueEnqueueLockKey).Error; err != nil {
					log.Warnf("release enqueue lock failed: %v", err)
				}
			}()
			return conn.Transaction(fn)
		})
	default:
		return d.WithContext(ctx).Transaction(fn)
	}
}

// ModelEvalQueueEnqueue 把一批渠道模型加入评估队列（追加末尾）。
// 逐个解析 channel_model_id 为渠道+模型快照，校验渠道启用且模型存在；
// 同 (channel_id, model_name) 已存在 queued/running 时应用层去重跳过。
// 审计 RELI-05/R-L2: position 的读取(MAX)与写入必须在同一事务内完成，
// 并持有跨实例入队锁 — 两个并发入队不再各自读到同一个 MAX 并生成重复 position。
func ModelEvalQueueEnqueue(ctx context.Context, channelModelIDs []int) ([]model.ModelEvalQueueTask, error) {
	if len(channelModelIDs) == 0 {
		return []model.ModelEvalQueueTask{}, nil
	}

	// 先解析渠道模型快照, 事务外失败不占事务; 事务内只做去重 + position 分配 + 写入。
	now := time.Now()
	type candidate struct {
		key  string
		task model.ModelEvalQueueTask
	}
	candidates := make([]candidate, 0, len(channelModelIDs))
	seenInput := make(map[string]struct{}, len(channelModelIDs))
	for _, cmID := range channelModelIDs {
		cm, err := ChannelModelGet(cmID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvalQueueModelUnavailable, err)
		}
		channel, err := ChannelGetCore(cm.ChannelID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvalQueueModelUnavailable, err)
		}
		if !channel.Enabled {
			return nil, fmt.Errorf("%w: %s", ErrEvalQueueModelUnavailable, cm.Name)
		}
		key := fmt.Sprintf("%d:%s", channel.ID, cm.Name)
		if _, exists := seenInput[key]; exists {
			continue
		}
		seenInput[key] = struct{}{}
		candidates = append(candidates, candidate{key: key, task: model.ModelEvalQueueTask{
			ChannelID:      channel.ID,
			ChannelModelID: cm.ID,
			ChannelName:    channel.Name,
			ChannelType:    channel.Type,
			ModelName:      cm.Name,
			Status:         model.QueueTaskQueued,
			CreatedAt:      now,
		}})
	}
	if len(candidates) == 0 {
		return []model.ModelEvalQueueTask{}, nil
	}

	tasks := make([]model.ModelEvalQueueTask, 0, len(candidates))
	err := withEvalQueueEnqueueLock(ctx, func(tx *gorm.DB) error {
		var active []model.ModelEvalQueueTask
		if err := tx.Model(&model.ModelEvalQueueTask{}).
			Where("status IN ?", []model.QueueTaskStatus{model.QueueTaskQueued, model.QueueTaskRunning}).
			Find(&active).Error; err != nil {
			return err
		}
		dup := make(map[string]struct{}, len(active))
		for _, t := range active {
			dup[fmt.Sprintf("%d:%s", t.ChannelID, t.ModelName)] = struct{}{}
		}

		var maxPos int
		if err := tx.Model(&model.ModelEvalQueueTask{}).
			Select("COALESCE(MAX(position), -1)").
			Scan(&maxPos).Error; err != nil {
			return err
		}

		for _, cand := range candidates {
			if _, exists := dup[cand.key]; exists {
				continue
			}
			dup[cand.key] = struct{}{}
			maxPos++
			cand.task.Position = maxPos
			tasks = append(tasks, cand.task)
		}
		if len(tasks) == 0 {
			return nil
		}
		return tx.Create(&tasks).Error
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// ModelEvalQueueList 返回活跃队列（仅 queued 与 running），按 position ASC, id ASC，
// 先入队先执行。done/stopped 任务完成或失败后即从队列视图消失，历史结果可在评估历史查看。
func ModelEvalQueueList(ctx context.Context) ([]model.ModelEvalQueueTask, error) {
	items := make([]model.ModelEvalQueueTask, 0)
	err := db.GetDB().WithContext(ctx).Model(&model.ModelEvalQueueTask{}).
		Where("status IN ?", []model.QueueTaskStatus{model.QueueTaskQueued, model.QueueTaskRunning}).
		Order("position ASC, id ASC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ModelEvalQueuePopNext 在事务内把 position 最小的若干 queued 任务置为 running，
// 返回本实例实际抢到的任务；无任务时返回空。
//
// 多实例并发下，条件 UPDATE 的 RowsAffected 可能小于候选数：部分候选被其它实例
// 抢走。若像批处理那样遇到 partial 就整批返回空，会连本实例已成功置 running 的
// 行一起丢弃，这些任务状态已变但无人执行，只能等下次启动 ResetRunning 兜底。
// 因此这里逐条条件更新，只把 RowsAffected == 1 的行算作本实例成功派发的任务。
func ModelEvalQueuePopNext(ctx context.Context, limit int) ([]model.ModelEvalQueueTask, error) {
	if limit <= 0 {
		limit = 1
	}
	var tasks []model.ModelEvalQueueTask
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ModelEvalQueueTask{}).
			Where("status = ?", model.QueueTaskQueued).
			Order("position ASC, id ASC").
			Limit(limit).
			Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		now := time.Now()
		kept := make([]model.ModelEvalQueueTask, 0, len(tasks))
		for i := range tasks {
			res := tx.Model(&model.ModelEvalQueueTask{}).
				Where("id = ? AND status = ?", tasks[i].ID, model.QueueTaskQueued).
				Updates(map[string]interface{}{"status": model.QueueTaskRunning, "started_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				// 该行已被其它实例抢走，不属于本实例。
				continue
			}
			tasks[i].Status = model.QueueTaskRunning
			tasks[i].StartedAt = now
			kept = append(kept, tasks[i])
		}
		tasks = kept
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// ModelEvalQueueResetRunning 把遗留 running 重置回 queued（进程崩溃/重启恢复）。
//
// 跨实例安全：只重置「没有租约信息」的历史 running（StartedAt 为零，旧版本升级
// 或租约概念引入前的行）以及租约已过期的 running。仍处于 evalQueueRunningLease
// 内的 running 视为其它实例（或本实例崩溃后很快重启）正在执行的任务，不重置，
// 否则多实例部署下新实例启动会把健康实例正在跑的评估重新入队导致重复执行。
// 过期任务由 Scheduler 每次回收周期调用本函数逐批清理。
func ModelEvalQueueResetRunning(ctx context.Context) error {
	cutoff := time.Now().Add(-evalQueueRunningLease)
	return db.GetDB().WithContext(ctx).Model(&model.ModelEvalQueueTask{}).
		Where("status = ? AND (started_at = ? OR started_at < ?)", model.QueueTaskRunning, time.Time{}, cutoff).
		Updates(map[string]interface{}{"status": model.QueueTaskQueued}).Error
}

// ModelEvalQueueMoveUp 仅 queued 且非队首任务与前一 queued 任务交换 position；
// running 返回冲突错误。
func ModelEvalQueueMoveUp(ctx context.Context, id int64) ([]model.ModelEvalQueueTask, error) {
	var target model.ModelEvalQueueTask
	if err := db.GetDB().WithContext(ctx).First(&target, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEvalQueueTaskNotFound
		}
		return nil, err
	}
	if target.Status != model.QueueTaskQueued {
		return nil, ErrEvalQueueTaskConflict
	}

	var queued []model.ModelEvalQueueTask
	if err := db.GetDB().WithContext(ctx).Model(&model.ModelEvalQueueTask{}).
		Where("status = ?", model.QueueTaskQueued).
		Order("position ASC, id ASC").
		Find(&queued).Error; err != nil {
		return nil, err
	}
	idx := -1
	for i := range queued {
		if queued[i].ID == target.ID {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return nil, ErrEvalQueueMoveBounds
	}
	neighbor := queued[idx-1]

	// 条件更新兜底 TOCTOU：两行都必须仍为 queued 才交换 position；
	// 否则回滚事务并返回冲突（PopNext 抢走 / Stop 停止均不再可调序）。
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ModelEvalQueueTask{}).
			Where("id = ? AND status = ?", target.ID, model.QueueTaskQueued).
			Update("position", neighbor.Position)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEvalQueueTaskConflict
		}
		res = tx.Model(&model.ModelEvalQueueTask{}).
			Where("id = ? AND status = ?", neighbor.ID, model.QueueTaskQueued).
			Update("position", target.Position)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEvalQueueTaskConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ModelEvalQueueList(ctx)
}

// ModelEvalQueueStop 仅 queued 任务置 stopped；running 返回冲突错误。
func ModelEvalQueueStop(ctx context.Context, id int64) ([]model.ModelEvalQueueTask, error) {
	var target model.ModelEvalQueueTask
	if err := db.GetDB().WithContext(ctx).First(&target, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEvalQueueTaskNotFound
		}
		return nil, err
	}
	if target.Status != model.QueueTaskQueued {
		return nil, ErrEvalQueueTaskConflict
	}
	// 条件更新兜底首个读与写之间的 TOCTOU：PopNext 若已把该行置 running，
	// 这里 RowsAffected == 0，不覆盖执行中任务，返回冲突让前端刷新队列。
	result := db.GetDB().WithContext(ctx).Model(&model.ModelEvalQueueTask{}).
		Where("id = ? AND status = ?", id, model.QueueTaskQueued).
		Updates(map[string]interface{}{"status": model.QueueTaskStopped, "completed_at": time.Now()})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrEvalQueueTaskConflict
	}
	return ModelEvalQueueList(ctx)
}

// ModelEvalQueueClear 移除全部 queued 任务（不中断 running），返回移除数量。
func ModelEvalQueueClear(ctx context.Context) (int, error) {
	result := db.GetDB().WithContext(ctx).
		Where("status = ?", model.QueueTaskQueued).
		Delete(&model.ModelEvalQueueTask{})
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// ModelEvalQueueMarkDone 把任务置 done，回填 EvalID 与脱敏后的 Error、更新 CompletedAt。
func ModelEvalQueueMarkDone(ctx context.Context, id int64, evalID int64, errMsg string) error {
	errMsg = truncateUTF8Bytes(redactSensitiveText(errMsg), 4096)
	return db.GetDB().WithContext(ctx).Model(&model.ModelEvalQueueTask{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":       model.QueueTaskDone,
			"eval_id":      evalID,
			"error":        errMsg,
			"completed_at": time.Now(),
		}).Error
}
