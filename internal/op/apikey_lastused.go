package op

import (
	"context"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

const (
	// apiKeyTouchQueueSize 是 last_used_at 异步写入队列上限。队列满时新的
	// 触碰事件直接丢弃: last_used_at 仅是管理展示字段, 不影响鉴权决策。
	apiKeyTouchQueueSize = 1024
	// apiKeyTouchBatchSize 是单次 DB UPDATE 最多合并的 key 数量。
	apiKeyTouchBatchSize = 256
	// apiKeyTouchFlushInterval 是待写批次的最大停留时间(去抖窗口)。
	apiKeyTouchFlushInterval = 2 * time.Second
)

var (
	apiKeyTouchStartOnce sync.Once
	apiKeyTouchStopOnce  sync.Once
	apiKeyTouchMu        sync.Mutex
	apiKeyTouchPending   = make(map[int]struct{})
	apiKeyTouchCh        = make(chan int, apiKeyTouchQueueSize)
	apiKeyTouchStopCh    = make(chan struct{})
	apiKeyTouchDone      = make(chan struct{})
	apiKeyTouchStopped   bool
)

// APIKeyTouchLastUsed 异步更新密钥的最后使用时间, 不阻塞请求、不回写缓存。
// 通过去重 + 批量合并 + 有界队列 + 非阻塞入队, 避免每请求一个 goroutine 与
// 数据库写放大的旧行为(审计 RELI-07/R-M5)。
func APIKeyTouchLastUsed(id int) {
	if id <= 0 {
		return
	}
	apiKeyTouchStartOnce.Do(startAPIKeyTouchWriter)

	apiKeyTouchMu.Lock()
	if apiKeyTouchStopped {
		apiKeyTouchMu.Unlock()
		return
	}
	if _, dup := apiKeyTouchPending[id]; dup {
		apiKeyTouchMu.Unlock()
		return
	}
	// 去重 map 与队列等长, 满则丢弃本次触碰, 防止 map 无限增长。
	if len(apiKeyTouchPending) >= apiKeyTouchQueueSize {
		apiKeyTouchMu.Unlock()
		return
	}
	apiKeyTouchPending[id] = struct{}{}
	apiKeyTouchMu.Unlock()

	select {
	case apiKeyTouchCh <- id:
	default:
		// 队列满: 移除刚插入的 pending 并丢弃事件。
		apiKeyTouchMu.Lock()
		delete(apiKeyTouchPending, id)
		apiKeyTouchMu.Unlock()
	}
}

func startAPIKeyTouchWriter() {
	go apiKeyTouchWriter()
}

func apiKeyTouchWriter() {
	defer close(apiKeyTouchDone)
	ticker := time.NewTicker(apiKeyTouchFlushInterval)
	defer ticker.Stop()

	batch := make([]int, 0, apiKeyTouchBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		apiKeyTouchFlushBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case id := <-apiKeyTouchCh:
			batch = append(batch, id)
			if len(batch) >= apiKeyTouchBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-apiKeyTouchStopCh:
			// 停机先排空已入队的触碰事件。随后必须把 pending map 中已有但可能
			// 尚未发送(或发送后队列已满回滚前时序窗口)的事件一起收编：
			// Touch 先写 pending 再投递 channel，若其在投递前被调度挂起，而
			// writer 在这里把 channel 排空后直接退出，该事件会永远滞留 pending。
			drained := true
			for drained {
				select {
				case id := <-apiKeyTouchCh:
					batch = append(batch, id)
					if len(batch) >= apiKeyTouchBatchSize {
						flush()
					}
				default:
					drained = false
				}
			}
			apiKeyTouchMu.Lock()
			for id := range apiKeyTouchPending {
				batch = append(batch, id)
				delete(apiKeyTouchPending, id)
			}
			apiKeyTouchMu.Unlock()
			flush()
			return
		}
	}
}

func apiKeyTouchFlushBatch(batch []int) {
	// 先释放去重占位, 让同一 key 在写库期间可重新入队; 写入失败由后续触碰
	// 补记, last_used_at 允许少量延迟与丢失。
	apiKeyTouchMu.Lock()
	for _, id := range batch {
		delete(apiKeyTouchPending, id)
	}
	apiKeyTouchMu.Unlock()

	now := time.Now().Unix()
	// 批量写 private field? Update 每次触碰走一个 IN 更新。
	_ = db.GetDB().Model(&model.APIKey{}).
		Where("id IN ?", batch).
		Update("last_used_at", now).Error
}

// APIKeyTouchLastUsedFlush 在停机时停写入协程并等待最后一批落库。
// 必须在 DB 关闭前调用; 幂等, 未启动过写入协程时立即返回。
func APIKeyTouchLastUsedFlush(ctx context.Context) error {
	apiKeyTouchStartOnce.Do(startAPIKeyTouchWriter)
	apiKeyTouchStopOnce.Do(func() {
		apiKeyTouchMu.Lock()
		apiKeyTouchStopped = true
		apiKeyTouchMu.Unlock()
		close(apiKeyTouchStopCh)
	})
	select {
	case <-apiKeyTouchDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
