// Package builtin 维护 NovaVeil 出厂内置的固定渠道/提供商。
//
// 设计原则与 OmniRoute/9router 的 provider registry 一致：
//   - 内置免费渠道只出现在代码里，不依赖管理员手工创建；
//   - 服务启动时自动补建缺失的内置渠道；
//   - 用户/API 新建的渠道一律是自定义渠道（IsFree=false、Builtin=false），
//     不参与内置免费渠道清单。
package builtin

import (
	"context"
	"errors"
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

// BuiltinFreeChannels 是出厂自带的固定免费渠道清单，由代码维护。
//
// 后续确认具体免费提供商后，直接在此追加 model.Channel 条目即可：
//   - Name/Type/BaseURL/Models 必须填；
//   - IsFree 与 Builtin 由 EnsureBuiltinFreeChannels 强制写入 true，
//     不需要在条目里重复设置；
//   - Enabled 默认 true；
//   - 自定义请求头/OpencodeCompat 等按渠道需要填写。
//
// BuiltinFreeChannels 当前收录以下固定免费渠道（来自 OmniRoute/9router 的 no-auth
// 免费 LLM 供应商中，适合直接对接 OpenAI 兼容协议且当前仍可用的条目）：
//
//   - OpenCode Free：公开免费端点，OpenAI Compatible；需要 OpenCode 会话头，
//     后端 OpencodeCompat 机制负责注入 x-opencode-session。
//   - UncloseAI Free：公开免注册端点，OpenAI Compatible；模型列表变动频繁，
//     开启 AutoSync 跟随 /v1/models。
//   - AI Horde Free：社区志愿算力，OpenAI Compatible 匿名端点；匿名 key 固定为
//     0000000000，队列式响应较慢，不保证工具调用。
//
// 其他候选（DuckDuckGo/Cloudflare Playground 等）依赖浏览器/WebSocket/专用协议，
// 或已停服（9router 的 MiMo Free），暂不纳入。
var BuiltinFreeChannels = []model.Channel{
	{
		Name:           "OpenCode Free",
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://opencode.ai/zen",
		Key:            "public",
		AutoSync:       true,
		OpencodeCompat: true,
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "x-opencode-client", HeaderValue: "desktop"},
			{HeaderKey: "User-Agent", HeaderValue: "opencode/1.18.31"},
		},
		Models: []model.ChannelModel{
			{Name: "deepseek-v4-flash-free"},
			{Name: "mimo-v2.5-free"},
			{Name: "hy3-free"},
			{Name: "nemotron-3-ultra-free"},
			{Name: "nemotron-3.5-lightning-free"},
			{Name: "laguna-s-2.1-free"},
		},
	},
	{
		Name:     "UncloseAI Free",
		Type:     model.ChannelProviderOpenAI,
		BaseURL:  "https://hermes.ai.unturf.com",
		Key:      "builtin",
		AutoSync: true,
		Models: []model.ChannelModel{
			{Name: "Lorbus/Qwen3.6-27B-int4-AutoRound"},
		},
	},
	{
		Name:     "AI Horde Free",
		Type:     model.ChannelProviderOpenAI,
		BaseURL:  "https://oai.aihorde.net",
		Key:      "0000000000",
		AutoSync: true,
		Models: []model.ChannelModel{
			{Name: "aphrodite/TheDrummer/Cydonia-24B-v4.3"},
			{Name: "aphrodite/TheDrummer/Skyfall-31B-v4.2"},
			{Name: "google/gemma-4-31b"},
		},
	},
}

// EnsureBuiltinFreeChannels 启动时补齐所有缺失的内置免费渠道。
//
// 幂等：已存在的渠道（无论是否为内置）不会重复创建，也不会覆盖管理员对
// 已有内置渠道的启停、Key、Header 等个性化修改；被删除的内置渠道会在下一次
// 启动时重新补建，保证“固定提供商”始终可用。
func EnsureBuiltinFreeChannels(ctx context.Context) error {
	database := db.GetDB()
	if database == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB := database.WithContext(ctx)

	// 强制不变量：免费分类只允许出现在内置渠道上；历史版本可能通过编辑器把
	// 用户渠道标记为免费，启动时统一收归，避免自定义渠道继续占用免费优先级。
	// opencode 兼容头同理，只由内置 OpenCode Free 渠道使用，自定义渠道不允许开启。
	if err := gormDB.Model(&model.Channel{}).
		Where("builtin = ?", false).
		Where("is_free = ?", true).
		Updates(map[string]interface{}{"is_free": false}).Error; err != nil {
		return fmt.Errorf("清理自定义渠道免费标记失败: %w", err)
	}
	if err := gormDB.Model(&model.Channel{}).
		Where("builtin = ?", true).
		Where("is_free = ?", false).
		Updates(map[string]interface{}{"is_free": true}).Error; err != nil {
		return fmt.Errorf("修复内置渠道免费标记失败: %w", err)
	}
	if err := gormDB.Model(&model.Channel{}).
		Where("builtin = ?", false).
		Where("opencode_compat = ?", true).
		Updates(map[string]interface{}{"opencode_compat": false}).Error; err != nil {
		return fmt.Errorf("清理自定义渠道 opencode 兼容标记失败: %w", err)
	}
	for _, def := range BuiltinFreeChannels {
		channel := builtinChannelFromDefinition(def)
		if channel.Name == "" {
			return fmt.Errorf("内置免费渠道缺少名称")
		}
		if channel.Type == "" {
			return fmt.Errorf("内置免费渠道 %q 缺少 Type", channel.Name)
		}
		if channel.Type != model.ChannelProviderCustom {
			if channel.BaseURL == "" {
				return fmt.Errorf("内置免费渠道 %q 缺少 BaseURL", channel.Name)
			}
		}

		var existing model.Channel
		err := gormDB.Where("name = ?", channel.Name).First(&existing).Error
		if err == nil {
			// 同名渠道已存在（管理员自定义或已补建的内置渠道），不覆盖。
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("检查内置免费渠道 %q 失败: %w", channel.Name, err)
		}

		if err := gormDB.Create(&channel).Error; err != nil {
			return fmt.Errorf("创建内置免费渠道 %q 失败: %w", channel.Name, err)
		}
	}
	return nil
}

// builtinChannelFromDefinition 返回一份可安全写入数据库的渠道副本：
// 强制 IsFree/Builtin/Enabled，并清空 ID 与模型关联 ID，避免污染全局清单。
func builtinChannelFromDefinition(def model.Channel) model.Channel {
	channel := def
	channel.ID = 0
	channel.Enabled = true
	channel.IsFree = true
	channel.Builtin = true

	channel.Keys = append([]model.ChannelKey(nil), channel.Keys...)
	channel.Models = append([]model.ChannelModel(nil), channel.Models...)
	for i := range channel.Models {
		channel.Models[i].ID = 0
		channel.Models[i].ChannelID = 0
		if channel.Models[i].Source == "" {
			channel.Models[i].Source = model.ChannelModelSourceManual
		}
	}
	channel.CustomHeader = append([]model.CustomHeader(nil), channel.CustomHeader...)
	channel.Tags = append([]string(nil), channel.Tags...)
	return channel
}
