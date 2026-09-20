// Package builtin 维护 NovaVeil 出厂内置的固定渠道/提供商，分为两类：
//
//   - 免费渠道（BuiltinFreeChannels）：IsFree=true，出厂启用、内置 Key、预置模型
//     列表，开箱即用但 AutoSync/Proxy 默认关闭，管理员按需开启。
//   - 官方渠道（BuiltinOfficialChannels）：IsFree=false，出厂禁用、无 Key、无
//     模型，管理员需自行输入 API Key、拉取模型并启用后参与路由。
//
// 设计原则与 OmniRoute/9router 的 provider registry 一致：
//   - 内置渠道只出现在代码里，不依赖管理员手工创建；
//   - 服务启动时 EnsureBuiltinChannels 自动补建缺失的内置渠道；
//   - 用户/API 新建的渠道一律是自定义渠道（IsFree=false、Builtin=false），
//     不参与内置渠道清单。
package builtin

import (
	"context"
	"errors"
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 免费渠道
// ---------------------------------------------------------------------------

// BuiltinFreeChannels 是出厂自带的固定免费渠道清单，由代码维护。
//
// 后续确认具体免费提供商后，直接在此追加 model.Channel 条目即可：
//   - Name/Type/BaseURL/Models 必须填；
//   - IsFree 与 Builtin 由 freeChannelFromDefinition 强制写入 true，
//     不需要在条目里重复设置；
//   - Enabled 默认 true；
//   - AutoSync 默认 false：内置免费渠道出厂不自动同步模型列表，避免每次启动
//     自动向第三方域名发 outbound 请求暴露服务器 IP。管理员按需在渠道编辑器
//     手动开启 AutoSync 后，同步任务才会对该渠道拉取 /v1/models；
//   - Proxy 默认 false：内置免费渠道出厂不走代理（直连）。如需通过代理同步模型
//     或转发请求，管理员需在渠道编辑器手动开启代理开关（系统代理或渠道专属代理）；
//   - 自定义请求头/OpencodeCompat 等按渠道需要填写。
//
// BuiltinFreeChannels 当前收录以下固定免费渠道（来自 OmniRoute/9router 的 no-auth
// 免费 LLM 供应商中，适合直接对接 OpenAI 兼容协议且当前仍可用的条目）：
//
//   - OpenCode Free：公开免费端点，OpenAI Compatible；需要 OpenCode 会话头，
//     后端 OpencodeCompat 机制负责注入 x-opencode-session。
//   - UncloseAI Free：公开免注册端点，OpenAI Compatible；模型列表变动频繁，
//     管理员可按需开启 AutoSync 跟随 /v1/models。
//   - AI Horde Free：社区志愿算力，OpenAI Compatible 匿名端点；匿名 key 固定为
//     0000000000，队列式响应较慢，不保证工具调用。
//   - Pollinations Free：免 key OpenAI Compatible 端点，模型列表较大，
//     管理员可按需开启 AutoSync 跟随 /v1/models。
//   - OVH AI Endpoints Free：免 key OpenAI Compatible 端点，按 IP 限流，
//     管理员可按需开启 AutoSync 跟随 /v1/models。
//   - LLM7 Free：免 key OpenAI Compatible 聚合端点，管理员可按需开启 AutoSync。
//   - Kilo Gateway Free：免 key OpenAI Compatible 网关，200 req/hr/IP，
//     管理员可按需开启 AutoSync 跟随 /v1/models。
//   - Airforce Free：免 key OpenAI Compatible 聚合端点，管理员可按需开启 AutoSync。
//   - G4F Space NVIDIA Free：免 key OpenAI Compatible 的 NVIDIA 代理端点，
//     管理员可按需开启 AutoSync 跟随 /v1/models。
//
// 其他候选（DuckDuckGo/Cloudflare Playground 等）依赖浏览器/WebSocket/专用协议，
// 或已停服（9router 的 MiMo Free），暂不纳入。
var BuiltinFreeChannels = []model.Channel{
	{
		Name:           "OpenCode Free",
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://opencode.ai/zen",
		Key:            "public",
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
		Name:    "UncloseAI Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://hermes.ai.unturf.com",
		Key:     "builtin",
		Models: []model.ChannelModel{
			{Name: "Lorbus/Qwen3.6-27B-int4-AutoRound"},
		},
	},
	{
		Name:    "AI Horde Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://oai.aihorde.net",
		Key:     "0000000000",
		Models: []model.ChannelModel{
			{Name: "aphrodite/TheDrummer/Cydonia-24B-v4.3"},
			{Name: "aphrodite/TheDrummer/Skyfall-31B-v4.2"},
			{Name: "google/gemma-4-31b"},
		},
	},
	{
		Name:    "Pollinations Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://gen.pollinations.ai",
		Models: []model.ChannelModel{
			{Name: "openai/gpt-5.4-nano"},
			{Name: "openai/gpt-4o-mini"},
			{Name: "tencent/hy3"},
			{Name: "deepseek/deepseek-v4.1-flash"},
			{Name: "qwen/qwen3.8-flash"},
		},
	},
	{
		Name:    "OVH AI Endpoints Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://oai.endpoints.kepler.ai.cloud.ovh.net",
		Models: []model.ChannelModel{
			{Name: "Mistral-Nemo-Instruct-2407"},
			{Name: "Mistral-Small-3.2-24B-Instruct-2506"},
			{Name: "Qwen3.6-27B"},
			{Name: "Meta-Llama-3_3-70B-Instruct"},
			{Name: "gpt-oss-20b"},
		},
	},
	{
		Name:    "LLM7 Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://api.llm7.io",
		Models: []model.ChannelModel{
			{Name: "DeepSeek-V4.1-Flash"},
			{Name: "GLM-5.3-Flash"},
			{Name: "gemini-3-flash"},
			{Name: "gpt-5.5"},
		},
	},
	{
		Name:    "Kilo Gateway Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://api.kilo.ai/api/gateway",
		Models: []model.ChannelModel{
			{Name: "kilo-auto/free"},
			{Name: "deepseek/deepseek-v4.1-flash"},
			{Name: "openai/gpt-5.6-sol"},
			{Name: "nvidia/nemotron-3-ultra-550b-a55b:free"},
		},
	},
	{
		Name:    "Airforce Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://api.airforce",
		Models: []model.ChannelModel{
			{Name: "gpt-oss-20b"},
			{Name: "gemini-3.6-flash"},
			{Name: "glm-5.3-flash"},
			{Name: "kimi-k3"},
		},
	},
	{
		Name:    "G4F Space NVIDIA Free",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://g4f.space/api/nvidia",
		Models: []model.ChannelModel{
			{Name: "meta/llama-3.3-70b-instruct"},
			{Name: "deepseek-ai/deepseek-r1"},
			{Name: "qwen/qwen2.5-coder-32b-instruct"},
			{Name: "microsoft/phi-4"},
		},
	},
}

// ---------------------------------------------------------------------------
// 官方渠道
// ---------------------------------------------------------------------------

// BuiltinOfficialChannels 是出厂自带的官方渠道清单，由代码维护。
//
// 与免费渠道的区别：
//   - 用户需要自行输入 API Key（Key 出厂为空）；
//   - 用户需要自行拉取模型（AutoSync 出厂为 false，Models 出厂为空）；
//   - 出厂禁用（Enabled = false），用户配置 Key 并启用后才参与路由。
//
// 后续确认更多官方提供商后，直接在此追加 model.Channel 条目即可：
//   - Name/Type/BaseURL 必须填；
//   - IsFree/Builtin/Enabled 由 officialChannelFromDefinition 强制写入；
//   - Key/Models 出厂留空，由管理员通过渠道编辑器配置。
//
// BuiltinOfficialChannels 当前收录以下官方渠道：
//
//   - OpenRouter：聚合数百个模型的 OpenAI Compatible 网关，用户需在 openrouter.ai
//     注册并获取 API Key；支持 Anthropic/OpenAI/Google 等多家模型统一转发。
//   - OpenCode：OpenCode 官方付费端点，OpenAI Compatible；用户需在 opencode.ai
//     获取 API Key，开启 AutoSync 后可拉取可用模型列表。与免费版一样需要
//     OpencodeCompat 注入 x-opencode-session 以及 x-opencode-client / User-Agent
//     请求头，由定义内联携带，officialChannelFromDefinition 原样保留。
//   - NVIDIA NIM：NVIDIA integrate API 端点，OpenAI Compatible；用户需在
//     integrate.api.nvidia.com 获取 API Key，提供 NVIDIA 托管的开源模型推理。
var BuiltinOfficialChannels = []model.Channel{
	{
		Name:    "OpenRouter",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://openrouter.ai/api",
	},
	{
		Name:           "OpenCode",
		Type:           model.ChannelProviderOpenAI,
		BaseURL:        "https://opencode.ai/zen/go",
		OpencodeCompat: true,
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "x-opencode-client", HeaderValue: "desktop"},
			{HeaderKey: "User-Agent", HeaderValue: "opencode/1.18.31"},
		},
	},
	{
		Name:    "NVIDIA NIM",
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://integrate.api.nvidia.com",
	},
}

// ---------------------------------------------------------------------------
// 补建入口
// ---------------------------------------------------------------------------

// EnsureBuiltinChannels 在启动时补建所有缺失的内置渠道（免费 + 官方）。
//
// 幂等：已存在的渠道（无论是否为内置）不会重复创建，也不会覆盖管理员对已有
// 内置渠道的启停、Key、Header 等个性化修改；被删除的内置渠道会在下一次启动时
// 重新补建，保证固定提供商始终可用。
func EnsureBuiltinChannels(ctx context.Context) error {
	if err := ensureFreeBuiltinChannels(ctx); err != nil {
		return err
	}
	if err := ensureOfficialBuiltinChannels(ctx); err != nil {
		return err
	}
	return nil
}

// ensureFreeBuiltinChannels 补建缺失的内置免费渠道，并清理历史脏数据。
func ensureFreeBuiltinChannels(ctx context.Context) error {
	database := db.GetDB()
	if database == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB := database.WithContext(ctx)

	// 强制不变量：免费分类只允许出现在内置渠道上；历史版本可能通过编辑器把
	// 用户渠道标记为免费，启动时统一收归，避免自定义渠道保留过期免费标记。
	// opencode 兼容头同理，只由内置 OpenCode 渠道（免费 + 官方）使用，自定义
	// 渠道不允许开启。
	if err := gormDB.Model(&model.Channel{}).
		Where("builtin = ?", false).
		Where("is_free = ?", true).
		Updates(map[string]interface{}{"is_free": false}).Error; err != nil {
		return fmt.Errorf("清理自定义渠道免费标记失败: %w", err)
	}
	if err := gormDB.Model(&model.Channel{}).
		Where("builtin = ?", false).
		Where("opencode_compat = ?", true).
		Updates(map[string]interface{}{"opencode_compat": false}).Error; err != nil {
		return fmt.Errorf("清理自定义渠道 opencode 兼容标记失败: %w", err)
	}
	for _, def := range BuiltinFreeChannels {
		channel := freeChannelFromDefinition(def)
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

		sealedChannel, sErr := sealBuiltinChannelForDB(channel)
		if sErr != nil {
			return fmt.Errorf("加密内置免费渠道 %q 失败: %w", channel.Name, sErr)
		}
		if err := gormDB.Create(&sealedChannel).Error; err != nil {
			return fmt.Errorf("创建内置免费渠道 %q 失败: %w", channel.Name, err)
		}
	}
	return nil
}

// ensureOfficialBuiltinChannels 补建缺失的内置官方渠道。已存在的同名渠道不覆盖，
// 尊重管理员已做的修改（填入 Key、启用、同步模型等）。与免费渠道一样受删除保护。
func ensureOfficialBuiltinChannels(ctx context.Context) error {
	database := db.GetDB()
	if database == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB := database.WithContext(ctx)

	for _, def := range BuiltinOfficialChannels {
		channel := officialChannelFromDefinition(def)
		if channel.Name == "" {
			return fmt.Errorf("内置官方渠道缺少名称")
		}
		if channel.Type == "" {
			return fmt.Errorf("内置官方渠道 %q 缺少 Type", channel.Name)
		}
		if channel.Type != model.ChannelProviderCustom {
			if channel.BaseURL == "" {
				return fmt.Errorf("内置官方渠道 %q 缺少 BaseURL", channel.Name)
			}
		}

		var existing model.Channel
		err := gormDB.Where("name = ?", channel.Name).First(&existing).Error
		if err == nil {
			// 同名渠道已存在（管理员自定义或已补建的内置渠道），不覆盖。
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("检查内置官方渠道 %q 失败: %w", channel.Name, err)
		}

		sealedChannel, sErr := sealBuiltinChannelForDB(channel)
		if sErr != nil {
			return fmt.Errorf("加密内置官方渠道 %q 失败: %w", channel.Name, sErr)
		}
		if err := gormDB.Create(&sealedChannel).Error; err != nil {
			return fmt.Errorf("创建内置官方渠道 %q 失败: %w", channel.Name, err)
		}
	}
	return nil
}

// sealBuiltinChannelForDB 返回敏感字段已加密的内置渠道副本, 与 op.sealChannelForDB
// 同一规则; 内置免费渠道带公开 Key(如 "public"), 也必须以密文落库, 不能因
// “公开”就在数据库里明文存放。库里旧明文由 openChannelForCache 按迁移兼容读取。
func sealBuiltinChannelForDB(channel model.Channel) (model.Channel, error) {
	dbChannel := channel
	if channel.Key != "" {
		sealed, err := seal.Seal(channel.Key)
		if err != nil {
			return model.Channel{}, fmt.Errorf("加密渠道 Key 失败: %w", err)
		}
		dbChannel.Key = sealed
	}
	if channel.ChannelProxy != nil && *channel.ChannelProxy != "" {
		sealed, err := seal.Seal(*channel.ChannelProxy)
		if err != nil {
			return model.Channel{}, fmt.Errorf("加密渠道代理凭据失败: %w", err)
		}
		dbChannel.ChannelProxy = &sealed
	}
	if len(channel.Keys) > 0 {
		dbChannel.Keys = append([]model.ChannelKey(nil), channel.Keys...)
		for i := range dbChannel.Keys {
			if dbChannel.Keys[i].Key == "" {
				continue
			}
			sealed, err := seal.Seal(dbChannel.Keys[i].Key)
			if err != nil {
				return model.Channel{}, fmt.Errorf("加密渠道 Key 失败: %w", err)
			}
			dbChannel.Keys[i].Key = sealed
		}
	}
	return dbChannel, nil
}

// ---------------------------------------------------------------------------
// 渠道定义 → 可写入副本
// ---------------------------------------------------------------------------

// freeChannelFromDefinition 返回一份可安全写入数据库的免费渠道副本：
// 强制 IsFree=true/Builtin=true/Enabled=true，并清空 ID 与模型关联 ID。
func freeChannelFromDefinition(def model.Channel) model.Channel {
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

// officialChannelFromDefinition 返回一份可安全写入数据库的官方渠道副本：
// 强制 IsFree=false/Builtin=true/Enabled=false，并清空 ID 与模型关联 ID。
// 与免费渠道的 freeChannelFromDefinition 不同：官方渠道出厂禁用且非免费，
// Key 与 Models 均为空，由管理员通过渠道编辑器自行配置。
func officialChannelFromDefinition(def model.Channel) model.Channel {
	channel := def
	channel.ID = 0
	channel.Enabled = false
	channel.IsFree = false
	channel.Builtin = true

	channel.Keys = append([]model.ChannelKey(nil), channel.Keys...)
	channel.Models = append([]model.ChannelModel(nil), channel.Models...)
	channel.CustomHeader = append([]model.CustomHeader(nil), channel.CustomHeader...)
	channel.Tags = append([]string(nil), channel.Tags...)
	return channel
}
