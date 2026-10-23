package op

import (
	"context"
	"fmt"
	"time"
)

func InitCache() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := settingRefreshCache(ctx); err != nil {
		return fmt.Errorf("刷新设置缓存失败: %w", err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		return fmt.Errorf("刷新渠道缓存失败: %w", err)
	}
	if err := groupRefreshCache(ctx); err != nil {
		return fmt.Errorf("刷新分组缓存失败: %w", err)
	}
	if err := apiKeyRefreshCache(ctx); err != nil {
		return fmt.Errorf("刷新 API key 缓存失败: %w", err)
	}
	// 错误日志保留天数键不在 DefaultSettings 内, 随缓存初始化惰性补种默认值。
	if err := ErrorLogEnsureRetentionSetting(ctx); err != nil {
		return fmt.Errorf("初始化错误保留设置失败: %w", err)
	}
	// OpenCode 账号自动体检开关同样惰性补种, 默认开启(台账为空时任务空转)。
	if err := EnsureOpencodeAccountAutoProbeSetting(ctx); err != nil {
		return fmt.Errorf("初始化账号自动体检开关失败: %w", err)
	}
	// 客户端统计从数据库加载到内存缓存。
	if err := ClientStatLoad(ctx); err != nil {
		return fmt.Errorf("加载调用方统计失败: %w", err)
	}
	return nil
}
