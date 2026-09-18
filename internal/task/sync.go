package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

var (
	syncModelsMu         sync.Mutex   // 保证同一时间只有一个模型同步任务运行。
	lastSyncModelsTimeMu sync.RWMutex // 最近同步时间的读写锁。
	lastSyncModelsTime   = time.Now() // 最近一次模型同步任务结束时间。
)

// SyncModelsTask 同步渠道模型并清理失效关联，返回本次同步遇到的首个错误。
func SyncModelsTask() error {
	if !syncModelsMu.TryLock() {
		return fmt.Errorf("模型同步正在进行中")
	}
	defer syncModelsMu.Unlock()

	log.Debugf("sync models task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("sync models task finished, sync time: %s", time.Since(startTime))
	}()
	defer func() {
		lastSyncModelsTimeMu.Lock()
		lastSyncModelsTime = time.Now()
		lastSyncModelsTimeMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(LifecycleContext(), 30*time.Minute)
	defer cancel()
	channels := op.ChannelList()
	var syncErr error
	for _, channel := range channels {
		if !channel.AutoSync {
			continue
		}
		// 单渠道级超时: 全部渠道共享总预算时, 一个挂死渠道会耗尽 30 分钟预算,
		// 其余渠道全部被饿死; 单渠道 3 分钟足够完成分页模型列表。
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 3*time.Minute)
		fetchModels, err := helper.FetchModels(fetchCtx, channel)
		if err != nil {
			log.Warnf("failed to sync models for channel %s: %v", channel.Name, err)
			if syncErr == nil {
				syncErr = fmt.Errorf("获取渠道 %s 的模型列表失败: %w", channel.Name, err)
			}
			fetchCancel()
			continue
		}

		// 防御(H-01): 上游返回空模型列表时, 若渠道已持有 auto 模型, 跳过本轮删除以免误删。
		// 上游 401/403/429 等错误已在 fetch 层转为 error 走上面的分支, 这里仅兜底上游合法
		// 返回空列表但渠道已存在 auto 模型的可疑场景: 删除全部 auto 模型会级联清理分组生效项、
		// 分组成员与评估排序, 属破坏性操作, 宁可不同步也不丢失既有模型。
		if len(fetchModels) == 0 {
			hasExistingAuto := false
			for _, channelModel := range channel.Models {
				if channelModel.Source == model.ChannelModelSourceAuto {
					hasExistingAuto = true
					break
				}
			}
			if hasExistingAuto {
				log.Warnf("skip syncing models for channel %s: upstream returned an empty model list while the channel already has auto models; deletion skipped to avoid data loss", channel.Name)
				fetchCancel()
				continue
			}
		}

		manualNames := make(map[string]struct{})
		oldAutoNames := make(map[string]struct{})
		models := make([]model.ChannelModel, 0, len(channel.Models)+len(fetchModels))
		for _, channelModel := range channel.Models {
			switch channelModel.Source {
			case model.ChannelModelSourceManual:
				manualNames[channelModel.Name] = struct{}{}
				models = append(models, channelModel)
			case model.ChannelModelSourceAuto:
				oldAutoNames[channelModel.Name] = struct{}{}
			}
		}
		// 外部返回的模型名只在进入内部流程时清洗一次，并由手动模型优先占用重复名称。
		seen := make(map[string]struct{}, len(fetchModels))
		autoModels := make([]model.ChannelModel, 0, len(fetchModels))
		for _, modelName := range fetchModels {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				continue
			}
			if _, ok := manualNames[modelName]; ok {
				continue
			}
			if _, ok := seen[modelName]; ok {
				continue
			}
			seen[modelName] = struct{}{}
			autoModels = append(autoModels, model.ChannelModel{Name: modelName, Source: model.ChannelModelSourceAuto})
		}
		addedModels := make([]string, 0)
		newAutoNames := make(map[string]struct{}, len(autoModels))
		for _, channelModel := range autoModels {
			newAutoNames[channelModel.Name] = struct{}{}
			if _, ok := oldAutoNames[channelModel.Name]; !ok {
				addedModels = append(addedModels, channelModel.Name)
			}
		}
		deletedModels := make([]string, 0)
		for name := range oldAutoNames {
			if _, ok := newAutoNames[name]; !ok {
				deletedModels = append(deletedModels, name)
			}
		}
		if len(deletedModels) == 0 && len(addedModels) == 0 {
			// 无增删的渠道同样要释放本轮 fetchCtx, 否则其定时器会挂到超时为止。
			fetchCancel()
			continue
		}
		models = append(models, autoModels...)

		if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{
			ID:     channel.ID,
			Models: &models,
		}, fetchCtx); err != nil {
			log.Warnf("failed to sync models for channel %s: %v", channel.Name, err)
			if syncErr == nil {
				syncErr = fmt.Errorf("更新渠道 %s 的模型失败: %w", channel.Name, err)
			}
			fetchCancel()
			continue
		}
		fetchCancel()
		if len(deletedModels) > 0 {
			log.Infof("deleted channel %s models: %v", channel.Name, deletedModels)
		}
	}
	return syncErr
}

// GetLastSyncModelsTime 返回最近一次模型同步任务结束时间。
func GetLastSyncModelsTime() time.Time {
	lastSyncModelsTimeMu.RLock()
	defer lastSyncModelsTimeMu.RUnlock()
	return lastSyncModelsTime
}
