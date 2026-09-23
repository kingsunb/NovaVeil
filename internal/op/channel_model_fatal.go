package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

// ChannelModelFatalEvalLimit 免费渠道模型的评估确定性失败(401/403/404)累计达到该次数后,
// 自动删除该渠道模型, 让失效的免费模型不再在历次评估中反复报错。仅对确定性失败计数,
// 429/限流/超时/网络/5xx 等瞬时或非必然失败不计数。
const ChannelModelFatalEvalLimit = 3

// ChannelModelRecordFatalEvalFailure 记录一次确定性评估失败并递增该模型的累计失败计数。
// 达到 ChannelModelFatalEvalLimit 时在事务内级联删除该渠道模型(清理分组当前项/分组成员/
// 评估排序)并返回 deleted=true, 事务提交后同步刷新进程内缓存。
// 幂等: 模型已被删除时静默返回; channelModelID 为 0 表示不存在渠道模型, 直接跳过。
func ChannelModelRecordFatalEvalFailure(ctx context.Context, channelModelID int) (deleted bool, err error) {
	if channelModelID == 0 {
		return false, nil
	}

	err = db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cm model.ChannelModel
		if err := tx.First(&cm, channelModelID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 已被并发删除, 幂等返回。
			}
			return fmt.Errorf("加载渠道模型失败: %w", err)
		}

		next := cm.EvalFatalFailures + 1
		if err := tx.Model(&model.ChannelModel{}).
			Where("id = ?", channelModelID).
			Update("eval_fatal_failures", next).Error; err != nil {
			return fmt.Errorf("累计确定性失败次数失败: %w", err)
		}

		if next < ChannelModelFatalEvalLimit {
			return nil
		}

		ids := []int{channelModelID}
		if err := clearActiveItemsByChannelModels(tx, ids); err != nil {
			return err
		}
		if err := deleteItemsByChannelModels(tx, ids); err != nil {
			return err
		}
		if err := deleteEvalRanksByChannelModels(tx, ids); err != nil {
			return err
		}
		if err := tx.Delete(&model.ChannelModel{}, ids).Error; err != nil {
			return fmt.Errorf("删除失效渠道模型失败: %w", err)
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !deleted {
		return false, nil
	}

	// 同步进程内缓存: 移除已删渠道模型条目, 并重建分组缓存(分组当前项/成员可能引用该模型)。
	channelModelCache.Del(channelModelID)
	if err := groupRefreshCache(ctx); err != nil {
		// 删除已落库, 分组缓存刷新失败只影响进程内一致性, 下次启动或下次缓存刷新会自愈。
		log.Warnf("channel model fatal-delete committed but group cache refresh failed: %v", err)
	}
	return true, nil
}
