package task

import (
	"context"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
)

// cleanErrorLogsInterval 错误日志清理任务的执行周期: 每天一次。
const cleanErrorLogsInterval = 24 * time.Hour

const (
	TaskBackgroundProbe   = "background_probe"
	TaskSyncLLM           = "sync_llm"
	TaskCleanErrorLogs    = "clean_error_logs"
	TaskCleanUsageBuckets = "clean_usage_buckets"
)

func Init() {
	// 后台定时探测无条件注册, 是否实际探测由每次执行时读取的最新分组配置决定, 无启用分组时空转返回。
	Register(TaskBackgroundProbe, backgroundProbeCheckPeriod, false, BackgroundProbe)

	// 每天清理一次超过保留天数的错误日志(保留天数见 error_retention_days 设置, 0=永久保留);
	// 注册在 LLM 同步之前, 避免同步间隔读取失败提前返回时连带丢失清理任务。
	// 客户端统计定期刷入数据库(5 分钟), 与优雅关停时的最终 flush 互补。
	Register("prune_ephemeral_state", time.Minute, true, relay.PruneEphemeralState)

	Register("client_stat_flush", 5*time.Minute, true, func() {
		ctx, cancel := context.WithTimeout(LifecycleContext(), time.Minute)
		defer cancel()
		if err := op.ClientStatFlush(ctx); err != nil {
			log.Warnf("client stat flush: %v", err)
		}
	})

	Register(TaskCleanErrorLogs, cleanErrorLogsInterval, true, func() {
		ctx, cancel := context.WithTimeout(LifecycleContext(), time.Minute)
		defer cancel()
		removed, err := op.ErrorLogCleanExpired(ctx)
		if err != nil {
			log.Warnf("failed to clean expired error logs: %v", err)
			return
		}
		if removed > 0 {
			log.Infof("cleaned %d expired error logs", removed)
		}
	})

	// 每天清理一次超过保留天数的用量分桶(保留天数见 usage_retention_days 设置, 0=永久保留);
	// 与错误日志清理同周期, 避免趋势图/KPI 把已过期数据计入。
	Register(TaskCleanUsageBuckets, cleanErrorLogsInterval, true, func() {
		ctx, cancel := context.WithTimeout(LifecycleContext(), time.Minute)
		defer cancel()
		removed, err := op.UsageBucketCleanExpired(ctx)
		if err != nil {
			log.Warnf("failed to clean expired usage buckets: %v", err)
			return
		}
		if removed > 0 {
			log.Infof("cleaned %d expired usage buckets", removed)
		}
	})

	// 注册LLM同步任务
	syncLLMIntervalHours, err := op.SettingGetInt(model.SettingKeySyncLLMInterval)
	if err != nil {
		log.Warnf("failed to get sync LLM interval: %v", err)
		return
	}
	// 历史坏值兜底: 校验上线前落库的 0/负值/溢出值一律回退默认 24h, 避免负间隔或任务丢失。
	if syncLLMIntervalHours < 1 || syncLLMIntervalHours > 8760 {
		log.Warnf("invalid sync LLM interval %d, fallback to 24h", syncLLMIntervalHours)
		syncLLMIntervalHours = 24
	}
	syncLLMInterval := time.Duration(syncLLMIntervalHours) * time.Hour
	Register(string(model.SettingKeySyncLLMInterval), syncLLMInterval, true, func() {
		warnSyncFailure(SyncModelsTask())
	})
}
