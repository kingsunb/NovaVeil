package op

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
	"github.com/kingsunb/NovaVeil/internal/utils/cache"
	"gorm.io/gorm"
)

var (
	channelCache      = cache.New[int, model.Channel](16)      // 渠道配置的进程内副本。
	channelModelCache = cache.New[int, model.ChannelModel](16) // 渠道模型的进程内副本。
)

// ChannelList 返回缓存中的全部渠道及其模型。
func ChannelList() []model.Channel {
	channels := make([]model.Channel, 0, channelCache.Len())
	for _, channel := range channelCache.GetAll() {
		channels = append(channels, channelSnapshot(channel))
	}
	return channels
}

// ChannelCreate 创建渠道及其模型并写入缓存。
func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	if channel == nil {
		return fmt.Errorf("缺少渠道数据")
	}
	channel.ID = 0
	// 用户/API 创建的渠道一律视为自定义渠道: 免费分类与内置标记只能由代码内置的数据源维护。
	channel.IsFree = false
	channel.Builtin = false
	channel.OpencodeCompat = false
	keys, err := normalizeChannelKeys(channel.Keys)
	if err != nil {
		return err
	}
	channel.Keys = keys
	channel.Tags = normalizeChannelTags(channel.Tags)
	if channel.Type != model.ChannelProviderCustom {
		if err := validateChannelBaseURL(channel.BaseURL); err != nil {
			return err
		}
	}
	for i := range channel.Models {
		channel.Models[i].ID = 0
		channel.Models[i].ChannelID = 0
		channel.Models[i].Name = strings.TrimSpace(channel.Models[i].Name)
		if channel.Models[i].Source == "" {
			channel.Models[i].Source = model.ChannelModelSourceManual
		}
		if channel.Models[i].Name == "" {
			return fmt.Errorf("渠道模型名称不能为空")
		}
	}
	dbChannel, err := sealChannelForDB(*channel)
	if err != nil {
		return err
	}
	if err := db.GetDB().WithContext(ctx).Create(&dbChannel).Error; err != nil {
		return err
	}
	// 写库完成后把自增主键写回明文请求体; 缓存保持明文供进程内转发与返回使用。
	channel.ID = dbChannel.ID
	channelCache.Set(channel.ID, cacheableChannel(*channel))
	for _, channelModel := range channel.Models {
		channelModelCache.Set(channelModel.ID, channelModel)
	}
	return nil
}

// splitImportBlocks 把导出文本按空行切分为块, 每块是去除了首尾空白的非空行集合。
// 连续的非空行属于同一块, 仅空白的行作为块分隔, 首尾的空行被忽略。
func splitImportBlocks(text string) [][]string {
	var blocks [][]string
	var current []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

// ChannelImportFromText 解析导出格式文本并批量创建渠道。
// 导出格式: 每个渠道一段(首行 "# 渠道名", 其次上游地址, 之后每行一把密钥), 渠道间空行分隔。
// 逐渠道创建, 单条失败(如名称已存在的唯一约束冲突)记录原因并继续后续渠道,
// 不因个别渠道失败而中断整批导入。
func ChannelImportFromText(ctx context.Context, text string) (success, failed int, errors []string) {
	errors = make([]string, 0)
	for _, block := range splitImportBlocks(text) {
		// 首行须以 "#" 起头作为渠道名, 次行为上游地址, 其余每行一把密钥。
		name := ""
		if strings.HasPrefix(block[0], "#") {
			name = strings.TrimSpace(strings.TrimPrefix(block[0], "#"))
		}
		if name == "" || len(block) < 2 {
			failed++
			label := name
			if label == "" {
				label = block[0]
			}
			errors = append(errors, fmt.Sprintf("%s: 格式不正确，缺少渠道名或上游地址", label))
			continue
		}
		baseURL := block[1]
		keys := make([]model.ChannelKey, 0, len(block)-2)
		maskedKey := false
		for _, keyLine := range block[2:] {
			if keyLine == "" {
				continue
			}
			// 管理端导出的文本对 Key 做固定掩码(如 ****1234)。掩码不是可用
			// 凭据, 直接把掩码当作上游 Key 导入会创建坏渠道; 这里整块拒绝。
			if strings.Contains(keyLine, "****") {
				maskedKey = true
				break
			}
			keys = append(keys, model.ChannelKey{Key: keyLine})
		}
		if maskedKey {
			failed++
			errors = append(errors, fmt.Sprintf("%s: 文本密钥已脱敏(含 ****)，无法作为上游凭据导入，请使用未脱敏的完整密钥重新导出或手动填写", name))
			continue
		}
		channel := model.Channel{
			Name:    name,
			Type:    model.ChannelProviderOpenAI,
			Enabled: true,
			BaseURL: baseURL,
			Keys:    keys,
			Sort:    0,
		}
		if err := ChannelCreate(&channel, ctx); err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("%s: %s", name, err.Error()))
			continue
		}
		success++
	}
	return success, failed, errors
}

// ChannelUpdate 更新渠道配置和模型行，并删除不再提供的模型。
func ChannelUpdate(req *model.ChannelUpdateRequest, ctx context.Context) (*model.Channel, error) {
	existingChannel, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("渠道不存在，请刷新页面后重试")
	}

	// 内置渠道由 internal/builtin 的固定清单维护，身份字段（名称/上游类型/Base URL）
	// 不允许通过更新接口修改，否则改名后下次启动会按原名称重复补建出第二条内置渠道
	//（H-07，审计 2026-09-19 发现）。Key、Header、启停、模型等个性化字段仍可修改。
	if existingChannel.Builtin {
		if req.Name != nil && *req.Name != existingChannel.Name {
			return nil, fmt.Errorf("内置渠道不可修改名称")
		}
		if req.Type != nil && *req.Type != existingChannel.Type {
			return nil, fmt.Errorf("内置渠道不可修改上游类型")
		}
		if req.BaseURL != nil && *req.BaseURL != existingChannel.BaseURL {
			return nil, fmt.Errorf("内置渠道不可修改 Base URL")
		}
	}

	var selectFields []string
	updates := model.Channel{ID: req.ID}
	var sortUpdateVal *int
	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Type != nil {
		selectFields = append(selectFields, "type")
		updates.Type = *req.Type
	}
	if req.Enabled != nil {
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.BaseURL != nil {
		channelType := existingChannel.Type
		if req.Type != nil {
			channelType = *req.Type
		}
		if channelType != model.ChannelProviderCustom {
			if err := validateChannelBaseURL(*req.BaseURL); err != nil {
				return nil, err
			}
		}
		selectFields = append(selectFields, "base_url")
		updates.BaseURL = *req.BaseURL
	}
	if req.Key != nil {
		selectFields = append(selectFields, "key")
		updates.Key = *req.Key
	}
	if req.FixedReply != nil {
		selectFields = append(selectFields, "fixed_reply")
		updates.FixedReply = *req.FixedReply
	}
	// 多 Key 集合按整体替换。管理列表不回传明文，因此 id+空 key 表示保留该 ID 对应的旧 secret；
	// 新增或更换 secret 时必须提交非空 key。完成 secret 恢复后再统一归一化与唯一性校验。
	if req.Keys != nil {
		oldByID := make(map[string]string, len(existingChannel.Keys))
		for _, key := range existingChannel.Keys {
			if key.ID != "" && key.Key != "" {
				oldByID[key.ID] = key.Key
			}
		}
		restored := make([]model.ChannelKey, 0, len(*req.Keys))
		for _, key := range *req.Keys {
			key.Key = strings.TrimSpace(key.Key)
			if key.Key == "" && key.ID != "" {
				key.Key = oldByID[key.ID]
			}
			if key.Key == "" {
				return nil, fmt.Errorf("新增密钥缺少密钥明文，请填写或删除对应行")
			}
			// id 与密文的一致性校验防御过期页面/非正常提交: 前端已在输入新明文时省略 id。
			if key.ID != "" && stableChannelKeyID(key.Key) != key.ID {
				return nil, fmt.Errorf("密钥标识与密钥明文不匹配，请刷新页面后重试")
			}
			restored = append(restored, key)
		}
		keys, err := normalizeChannelKeys(restored)
		if err != nil {
			return nil, err
		}
		selectFields = append(selectFields, "keys")
		updates.Keys = keys
	}
	if req.Proxy != nil {
		selectFields = append(selectFields, "proxy")
		updates.Proxy = *req.Proxy
	}
	if req.AutoSync != nil {
		selectFields = append(selectFields, "auto_sync")
		updates.AutoSync = *req.AutoSync
	}
	if req.CustomHeader != nil {
		selectFields = append(selectFields, "custom_header")
		updates.CustomHeader = *req.CustomHeader
	}
	if req.ChannelProxy != nil {
		selectFields = append(selectFields, "channel_proxy")
		updates.ChannelProxy = req.ChannelProxy
	}
	if req.ParamOverride != nil {
		selectFields = append(selectFields, "param_override")
		updates.ParamOverride = req.ParamOverride
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = req.MatchRegex
	}
	// 按模型限制配置整体替换; 传空对象表示清除全部。
	if req.ModelLimits != nil {
		selectFields = append(selectFields, "model_limits")
		updates.ModelLimits = *req.ModelLimits
	}
	// 标签集合按整体替换; 归一化去除空白与重复, 传空数组表示清除全部。
	if req.Tags != nil {
		selectFields = append(selectFields, "tags")
		updates.Tags = normalizeChannelTags(*req.Tags)
	}
	// 优先级允许重复、零值与负值; 用 map 更新(而非 struct Select)可靠写入任意
	// int 值(含 0), 避免 GORM struct Updates 零值跳过的历史坑(BUG-004)。
	if req.Sort != nil {
		sortUpdateVal = req.Sort
	}
	// 限速与并发上限: nil 不覆盖, 负值归零(0 即不限制)。
	if req.RateLimitRPM != nil {
		rateLimitRPM := *req.RateLimitRPM
		if rateLimitRPM < 0 {
			rateLimitRPM = 0
		}
		selectFields = append(selectFields, "rate_limit_rpm")
		updates.RateLimitRPM = rateLimitRPM
	}
	if req.MaxConcurrent != nil {
		maxConcurrent := *req.MaxConcurrent
		if maxConcurrent < 0 {
			maxConcurrent = 0
		}
		selectFields = append(selectFields, "max_concurrent")
		updates.MaxConcurrent = maxConcurrent
	}
	if req.PassThroughBodyEnabled != nil {
		selectFields = append(selectFields, "pass_through_body_enabled")
		updates.PassThroughBodyEnabled = *req.PassThroughBodyEnabled
	}

	// 敏感字段在写入前落为密文: 进程内存缓存与返回继续使用明文。
	if updates.Key != "" {
		sealedKey, sealErr := seal.Seal(updates.Key)
		if sealErr != nil {
			return nil, fmt.Errorf("加密渠道密钥失败: %w", sealErr)
		}
		updates.Key = sealedKey
	}
	if len(updates.Keys) > 0 {
		for i := range updates.Keys {
			if updates.Keys[i].Key == "" {
				continue
			}
			sealedKey, sealErr := seal.Seal(updates.Keys[i].Key)
			if sealErr != nil {
				return nil, fmt.Errorf("加密渠道密钥失败: %w", sealErr)
			}
			updates.Keys[i].Key = sealedKey
		}
	}
	if updates.ChannelProxy != nil && *updates.ChannelProxy != "" {
		sealedProxy, sealErr := seal.Seal(*updates.ChannelProxy)
		if sealErr != nil {
			return nil, fmt.Errorf("加密渠道代理凭据失败: %w", sealErr)
		}
		updates.ChannelProxy = &sealedProxy
	}

	// 请求未携带任何可更新字段时显式报错而非静默成功: 该形态历史上会直接返回
	// 200 且不写任何列, 前端 toast「已保存」但读回旧值, 排查成本极高。当前前端
	// 全量编辑器、行内优先级与模型同步任务都不会发出空更新, 此守卫为异常客户端
	// 与未来回归兜底。
	if len(selectFields) == 0 && sortUpdateVal == nil && req.Models == nil {
		return nil, fmt.Errorf("请求未包含任何需要更新的字段")
	}

	var currentModels []model.ChannelModel
	var channel model.Channel
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(selectFields) > 0 {
			if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
				return fmt.Errorf("更新渠道失败: %w", err)
			}
		}
		if sortUpdateVal != nil {
			// map 更新可靠写入任意 int(含 0 与负值), 不受 GORM struct 零值跳过影响。
			if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Update("sort", *sortUpdateVal).Error; err != nil {
				return fmt.Errorf("更新优先级失败: %w", err)
			}
		}
		if req.Models != nil {
			if err := syncChannelModels(tx, req.ID, *req.Models); err != nil {
				return err
			}
		}
		if req.Models != nil {
			if err := tx.Where("channel_id = ?", req.ID).Find(&currentModels).Error; err != nil {
				return fmt.Errorf("加载渠道模型失败: %w", err)
			}
		}
		if err := tx.First(&channel, req.ID).Error; err != nil {
			return fmt.Errorf("加载更新后的渠道失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// DB 读回为密文; 解密后再进缓存, 确保转发/列表/导出路径拿到明文。
	if err := openChannelForCache(&channel); err != nil {
		return nil, err
	}

	channelCache.Set(channel.ID, cacheableChannel(channel))
	if req.Models != nil {
		currentModelsByID := make(map[int]model.ChannelModel, len(currentModels))
		for _, currentModel := range currentModels {
			currentModelsByID[currentModel.ID] = currentModel
		}

		// 仅增删发生变化的缓存项。
		for _, cachedModel := range channelModelCache.GetAll() {
			if cachedModel.ChannelID != req.ID {
				continue
			}
			currentModel, exists := currentModelsByID[cachedModel.ID]
			if !exists {
				channelModelCache.Del(cachedModel.ID)
				continue
			}
			cachedModel.Source = currentModel.Source
			channelModelCache.Set(cachedModel.ID, cachedModel)
			delete(currentModelsByID, cachedModel.ID)
		}
		for _, addedModel := range currentModelsByID {
			channelModelCache.Set(addedModel.ID, addedModel)
		}

		if err := groupRefreshCache(ctx); err != nil {
			log.Warnf("channel update succeeded but group cache refresh failed: %v", err)
		}
	}
	snapshot := channelSnapshot(channel)
	return &snapshot, nil
}

// ChannelEnabled 更新渠道启用状态。
func ChannelEnabled(id int, enabled bool, ctx context.Context) error {
	if _, ok := channelCache.Get(id); !ok {
		return fmt.Errorf("渠道不存在，请刷新页面后重试")
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	if channel, ok := channelCache.Get(id); ok {
		channel.Enabled = enabled
		channelCache.Set(id, cacheableChannel(channel))
	}
	return nil
}

// ChannelDel 删除渠道及其模型，关联分组成员与评估排序由应用层级联清理。
func ChannelDel(id int, ctx context.Context) error {
	channel, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("渠道不存在，请刷新页面后重试")
	}
	if channel.Builtin {
		return fmt.Errorf("内置渠道不允许删除；如不需要可先停用")
	}
	var modelIDs []int
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ChannelModel{}).Where("channel_id = ?", id).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("查找渠道模型失败: %w", err)
		}
		if len(modelIDs) > 0 {
			if err := clearActiveItemsByChannelModels(tx, modelIDs); err != nil {
				return err
			}
			if err := deleteItemsByChannelModels(tx, modelIDs); err != nil {
				return err
			}
			if err := deleteEvalRanksByChannelModels(tx, modelIDs); err != nil {
				return err
			}
		}
		if err := tx.Delete(&model.Channel{}, id).Error; err != nil {
			return fmt.Errorf("删除渠道失败: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	channelCache.Del(id)
	channelModelCache.Del(modelIDs...)
	if err := groupRefreshCache(ctx); err != nil {
		log.Warnf("channel delete succeeded but group cache refresh failed: %v", err)
	}
	return nil
}

// ChannelGet 返回指定渠道的缓存副本及其模型。
// 管理端接口使用; 转发热路径请改用 ChannelGetCore, 避免每次调用重建全量渠道模型列表。
func ChannelGet(id int) (model.Channel, error) {
	channel, ok := channelCache.Get(id)
	if !ok {
		return model.Channel{}, fmt.Errorf("渠道不存在，请刷新页面后重试")
	}
	return channelSnapshot(channel), nil
}

// ChannelGetCore 返回指定渠道的缓存副本(不含 Models 关联)。
// 转发热路径(选路/派发/探测)只需要 Enabled/Type/Name/Keys/代理与限流配置,
// 不需要模型列表: channelSnapshot 每次调用都会 GetAll 全部渠道模型并排序,
// 高并发下是选路临界区内的主要开销, 本函数以 O(1) 浅克隆替代。
func ChannelGetCore(id int) (model.Channel, error) {
	channel, ok := channelCache.Get(id)
	if !ok {
		return model.Channel{}, fmt.Errorf("渠道不存在，请刷新页面后重试")
	}
	return channel, nil
}

// ChannelModelGet 返回指定渠道模型的缓存副本。
func ChannelModelGet(id int) (model.ChannelModel, error) {
	channelModel, ok := channelModelCache.Get(id)
	if !ok {
		return model.ChannelModel{}, fmt.Errorf("渠道模型不存在，请刷新页面后重试")
	}
	return channelModel, nil
}

// channelRefreshCache 从数据库刷新渠道和渠道模型缓存。
func channelRefreshCache(ctx context.Context) error {
	channels := []model.Channel{}
	if err := db.GetDB().WithContext(ctx).Find(&channels).Error; err != nil {
		log.Warnf("failed to get channels: %v", err)
		return err
	}
	channelModels := []model.ChannelModel{}
	if err := db.GetDB().WithContext(ctx).Find(&channelModels).Error; err != nil {
		return err
	}
	// 以 RefreshAll 原子替换两个缓存，消除先 Clear 再 Set 的空窗口。
	for i := range channels {
		if err := openChannelForCache(&channels[i]); err != nil {
			return err
		}
	}
	channelMap := make(map[int]model.Channel, len(channels))
	for _, channel := range channels {
		channel.Models = nil
		channelMap[channel.ID] = cacheableChannel(channel)
	}
	channelModelMap := make(map[int]model.ChannelModel, len(channelModels))
	for _, channelModel := range channelModels {
		channelModelMap[channelModel.ID] = channelModel
	}
	channelCache.RefreshAll(channelMap)
	channelModelCache.RefreshAll(channelModelMap)
	return nil
}

// channelSnapshot 将渠道缓存与当前渠道模型合并为读取副本。
func channelSnapshot(channel model.Channel) model.Channel {
	snapshot := cacheableChannel(channel)
	models := make([]model.ChannelModel, 0)
	for _, channelModel := range channelModelCache.GetAll() {
		if channelModel.ChannelID == channel.ID {
			models = append(models, channelModel)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	snapshot.Models = models
	return snapshot
}

// ModelTokenUsage 按上游模型名聚合的 token 用量。
type ModelTokenUsage struct {
	Name   string `json:"name" gorm:"column:name"`
	Input  int64  `json:"input" gorm:"column:input"`
	Output int64  `json:"output" gorm:"column:output"`
}

// statsColumnsAvailability 缓存统计列存在性判定结果:
// 该形态只随 012 迁移一次性变化(旧库保留统计列/新库不存在), 运行期不会再变,
// 而 HasColumn 每次调用都是一轮 information_schema/pragma 往返, 该函数挂在面板轮询端点上,
// 用 sync.Once 固化首次判定即可。
var statsColumnsOnce sync.Once
var statsColumnsCached struct {
	channelStats bool
	modelStats   bool
}

// ChannelTokenUsageStats 返回渠道维度的 input/output token 总和,
// 以及按 channel_models.name 聚合的用量 top 10。
// 历史版本统计列已随 012 迁移删除, 列不存在时(新装库)返回零值与空切片而非报错,
// 保证 now-version 统计在任意库形态下可用; 若旧库仍保留统计列则如实聚合。
func ChannelTokenUsageStats(ctx context.Context) (totalInput, totalOutput int64, byModel []ModelTokenUsage, err error) {
	byModel = make([]ModelTokenUsage, 0)
	gormDB := db.GetDB()
	if gormDB == nil {
		return 0, 0, byModel, fmt.Errorf("数据库未初始化")
	}
	gormDB = gormDB.WithContext(ctx)

	statsColumnsOnce.Do(func() {
		statsColumnsCached.channelStats = gormDB.Migrator().HasColumn("channels", "input_token") && gormDB.Migrator().HasColumn("channels", "output_token")
		statsColumnsCached.modelStats = gormDB.Migrator().HasColumn("channel_models", "input_token") && gormDB.Migrator().HasColumn("channel_models", "output_token")
	})

	if statsColumnsCached.channelStats {
		if err := gormDB.Raw("SELECT COALESCE(SUM(input_token), 0) FROM channels").Scan(&totalInput).Error; err != nil {
			return 0, 0, byModel, fmt.Errorf("统计渠道输入 token 失败: %w", err)
		}
		if err := gormDB.Raw("SELECT COALESCE(SUM(output_token), 0) FROM channels").Scan(&totalOutput).Error; err != nil {
			return 0, 0, byModel, fmt.Errorf("统计渠道输出 token 失败: %w", err)
		}
	}
	if statsColumnsCached.modelStats {
		if err := gormDB.Raw(
			"SELECT cm.name AS name, COALESCE(SUM(c.input_token), 0) AS input, COALESCE(SUM(c.output_token), 0) AS output " +
				"FROM channel_models cm JOIN channels c ON c.id = cm.channel_id " +
				"GROUP BY cm.name ORDER BY (COALESCE(SUM(c.input_token), 0) + COALESCE(SUM(c.output_token), 0)) DESC LIMIT 10",
		).Scan(&byModel).Error; err != nil {
			return 0, 0, byModel, fmt.Errorf("汇总模型 token 用量失败: %w", err)
		}
	}
	return totalInput, totalOutput, byModel, nil
}

// normalizeChannelKeys 补齐缺失的 Key ID 并校验同一渠道内非空 Key 明文唯一。
// ID 取 sha256(Key明文) 前 8 位 hex, 同一密钥在多次提交间保持稳定。
// 代理模板 {account} 占位符由使用处现场生成随机别名填充, 不随 Key 保存任何账号字段。
// Remark 是密钥备注, 纯展示用途, 服务端只做 trim。
// 空明文条目静默丢弃: 上游为免认证服务时渠道可以不配置任何密钥,
// 全部条目为空(或输入为空切片)时返回空切片表示无密钥渠道。
func normalizeChannelKeys(keys []model.ChannelKey) ([]model.ChannelKey, error) {
	if len(keys) == 0 {
		if keys == nil {
			return nil, nil
		}
		return []model.ChannelKey{}, nil
	}
	normalized := make([]model.ChannelKey, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key.Key = strings.TrimSpace(key.Key)
		key.Remark = strings.TrimSpace(key.Remark)
		if key.Key == "" {
			continue
		}
		if _, duplicated := seen[key.Key]; duplicated {
			return nil, fmt.Errorf("存在重复密钥，请合并或删除重复项")
		}
		seen[key.Key] = struct{}{}
		key.ID = stableChannelKeyID(key.Key)
		normalized = append(normalized, key)
	}
	return normalized, nil
}

func stableChannelKeyID(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])[:8]
}

// normalizeChannelTags 去除首尾空白、剔除空串并去重, 保持首次出现的顺序;
// nil 与空切片语义保持不变(与 normalizeChannelKeys 一致)。
func normalizeChannelTags(tags []string) []string {
	if len(tags) == 0 {
		if tags == nil {
			return nil
		}
		return []string{}
	}
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, duplicated := seen[tag]; duplicated {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	return normalized
}

// validateChannelBaseURL 校验渠道上游地址。生产环境执行完整出口校验(语法 + DNS
// 解析后的私网/环回/metadata 拒绝); Go 测试二进制放宽为仅语法校验, 方便 httptest
// 等回环上游跑集成测试。fetch-model 等接受未保存渠道表单的入口必须调用
// validateChannelEgressBaseURL 强制完整校验, 不能退化成仅语法校验(审计 SEC-01)。
func validateChannelBaseURL(raw string) error {
	if isTestBinary() {
		return validateChannelBaseURLSyntax(raw)
	}
	return validateChannelEgressBaseURL(raw)
}

// ValidateChannelEgressBaseURL 对未保存渠道表单/已存渠道回退路径执行完整出口校验。
// 导出给 handlers 使用, 避免私网地址经 fetch-model 绕过 ChannelCreate 的校验。
func ValidateChannelEgressBaseURL(raw string) error {
	return validateChannelEgressBaseURL(raw)
}

// validateChannelEgressBaseURL 对渠道地址做完整出口校验:
// 必须是 http/https 且不含 userinfo, 并且域名解析后的所有 IP 都不是内网/环回/
// 链路本地/metadata 等禁止直连地址。
func validateChannelEgressBaseURL(raw string) error {
	if err := validateChannelBaseURLSyntax(raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(raw))
	if err := validateEgressHost(parsed.Hostname()); err != nil {
		return fmt.Errorf("渠道地址 %s 不允许访问: %w", strings.TrimSpace(raw), err)
	}
	return nil
}

func validateChannelBaseURLSyntax(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("渠道地址不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("渠道地址必须以 http:// 或 https:// 开头")
	}
	if parsed.User != nil {
		return fmt.Errorf("渠道地址不能包含 userinfo")
	}
	return nil
}

// validateEgressHost 校验出站目标主机名: 先按字面 IP 判定, 否则做 DNS 解析并逐一
// 拒绝落入禁止范围的地址。需要访问真实内网域名(如自建网关)的部署, 可由管理员改用
// 该内网服务的公网地址或显式代理; 渠道出口不允许默认连通任意内网主机。
func validateEgressHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("主机不能为空")
	}
	// host 形如 [::1]:8080 时剥掉括号; 普通 host 不会带括号。
	literal := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(literal); ip != nil {
		// 过时的 IPv4-compatible/translated IPv6 字面量(::127.0.0.1、
		// ::ffff:0:127.0.0.1)可编码 IPv4 环回/内网地址, 而 Go 对这类 16 字节
		// 表示 IsLoopback/IsPrivate 均返回 false。含点分十进制的 IPv6 字面量
		// 一律拒绝, 不试图猜测其意图; 正规 IPv6 公网地址不会写成这种形式。
		if strings.Contains(literal, ":") && strings.Contains(literal, ".") && ip.To4() == nil {
			return fmt.Errorf("渠道地址不允许使用 IPv4 兼容的 IPv6 字面量: %s", literal)
		}
		return validateEgressIP(ip)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("域名未解析到任何 IP")
	}
	for _, addr := range addrs {
		if err := validateEgressIP(addr.IP); err != nil {
			return fmt.Errorf("域名 %s 解析到禁止地址 %s: %w", host, addr.IP, err)
		}
	}
	return nil
}

// reservedIPv4Blocks 是 Go 标准库 IsPrivate/IsLoopback 等未覆盖、但不应作为
// 渠道上游直连目标的 IPv4 保留段(CGN、基准测试、文档示例、未来保留段)。
// 这些地址要么不可公网路由, 要么仅用于特殊场景, 直连它们属于 SSRF 面。
var reservedIPv4Blocks = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",       // "本网络" 保留段
		"100.64.0.0/10",   // CGNAT 共享地址空间
		"192.0.0.0/24",    // IETF 协议分配保留段
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // 网络基准测试
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // 未来保留段(含广播)
	}
	blocks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks
}()

// validateEgressIP 拒绝私网、环回、链路本地、未指定地址、组播与广播地址, 以及
// IPv4 保留段(CGN/测试网段/未来保留段)。IPv4-mapped IPv6 先归一化到 4 字节再判。
func validateEgressIP(raw net.IP) error {
	ip := raw
	if ip4 := raw.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		!ip.IsGlobalUnicast() {
		return fmt.Errorf("禁止访问内网/环回/链路本地/保留地址")
	}
	if ip.To4() != nil {
		for _, block := range reservedIPv4Blocks {
			if block.Contains(ip) {
				return fmt.Errorf("禁止访问内网/环回/链路本地/保留地址")
			}
		}
	}
	return nil
}

// isTestBinary 检测当前二进制是否为 go test 测试二进制: testing 包注册的 test.v
// flag 仅存在于测试二进制。仅用于放宽渠道出口校验, 生产二进制不受影响。
func isTestBinary() bool {
	return flag.Lookup("test.v") != nil
}

// cacheableChannel 返回可写入缓存或对外发布的渠道副本:
// 缓存按值存储但 slice/map 仅拷贝头部, JSON 序列化切片必须深拷贝,
// 防止调用方改写返回值时与缓存底层数组构成并发读写。
func cacheableChannel(channel model.Channel) model.Channel {
	cached := channel
	cached.Models = nil
	cached.Keys = cloneChannelKeys(channel.Keys)
	cached.CustomHeader = slices.Clone(channel.CustomHeader)
	cached.ModelLimits = maps.Clone(channel.ModelLimits)
	cached.Tags = slices.Clone(channel.Tags)
	return cached
}

// cloneChannelKeys 深拷贝渠道 Key 切片; nil 与空切片语义保持不变。
func cloneChannelKeys(keys []model.ChannelKey) []model.ChannelKey {
	if keys == nil {
		return nil
	}
	cloned := make([]model.ChannelKey, len(keys))
	copy(cloned, keys)
	return cloned
}

// syncChannelModels 按提交的模型集合新增、删除渠道模型，并更新变化的来源。
func syncChannelModels(tx *gorm.DB, channelID int, requested []model.ChannelModel) error {
	var existing []model.ChannelModel
	if err := tx.Where("channel_id = ?", channelID).Find(&existing).Error; err != nil {
		return fmt.Errorf("加载渠道模型失败: %w", err)
	}
	existingByName := make(map[string]model.ChannelModel, len(existing))
	for _, channelModel := range existing {
		existingByName[channelModel.Name] = channelModel
	}
	for _, requestedModel := range requested {
		name := strings.TrimSpace(requestedModel.Name)
		if name == "" {
			return fmt.Errorf("渠道模型名称不能为空")
		}
		source := requestedModel.Source
		if source == "" {
			source = model.ChannelModelSourceManual
		}
		if current, ok := existingByName[name]; ok {
			if current.Source != source {
				if err := tx.Model(&model.ChannelModel{}).Where("id = ?", current.ID).Update("source", source).Error; err != nil {
					return fmt.Errorf("更新渠道模型失败: %w", err)
				}
			}
			delete(existingByName, name)
			continue
		}
		if err := tx.Create(&model.ChannelModel{ChannelID: channelID, Name: name, Source: source}).Error; err != nil {
			return fmt.Errorf("创建渠道模型失败: %w", err)
		}
	}
	deletedModelIDs := make([]int, 0, len(existingByName))
	for _, channelModel := range existingByName {
		deletedModelIDs = append(deletedModelIDs, channelModel.ID)
	}
	if len(deletedModelIDs) == 0 {
		return nil
	}
	if err := clearActiveItemsByChannelModels(tx, deletedModelIDs); err != nil {
		return err
	}
	if err := deleteItemsByChannelModels(tx, deletedModelIDs); err != nil {
		return err
	}
	if err := deleteEvalRanksByChannelModels(tx, deletedModelIDs); err != nil {
		return err
	}
	if err := tx.Delete(&model.ChannelModel{}, deletedModelIDs).Error; err != nil {
		return fmt.Errorf("删除渠道模型失败: %w", err)
	}
	return nil
}

// clearActiveItemsByChannelModels 清理引用待删除渠道模型的分组当前项。
func clearActiveItemsByChannelModels(tx *gorm.DB, channelModelIDs []int) error {
	if len(channelModelIDs) == 0 {
		return nil
	}
	itemIDs := tx.Model(&model.GroupItem{}).
		Select("id").Where("channel_model_id IN ?", channelModelIDs)
	if err := tx.Model(&model.Group{}).
		Where("active_item_id IN (?)", itemIDs).
		Update("active_item_id", 0).Error; err != nil {
		return fmt.Errorf("清除生效成员失败: %w", err)
	}
	return nil
}

// deleteItemsByChannelModels 删除指向待删除渠道模型的分组成员行。
// 引用成员的渠道模型 ID 为 0, group_items 上不能建指向 channel_models 的外键,
// 上游依赖的外键级联删除改由本函数在应用层完成。
func deleteItemsByChannelModels(tx *gorm.DB, channelModelIDs []int) error {
	if len(channelModelIDs) == 0 {
		return nil
	}
	if err := tx.Where("channel_model_id IN ?", channelModelIDs).Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("按渠道模型删除分组成员失败: %w", err)
	}
	return nil
}

// deleteEvalRanksByChannelModels 删除引用待删除渠道模型的评估排序条目。
// model_eval_ranks.channel_model_id 不建外键(与历史记录解耦), 渠道模型删除时不会级联,
// 残留条目由「更新 auto 分组」的容错兜底清理, 故在此应用层级联清理。
func deleteEvalRanksByChannelModels(tx *gorm.DB, channelModelIDs []int) error {
	if len(channelModelIDs) == 0 {
		return nil
	}
	if err := tx.Where("channel_model_id IN ?", channelModelIDs).Delete(&model.ModelEvalRank{}).Error; err != nil {
		return fmt.Errorf("按渠道模型删除评估排序失败: %w", err)
	}
	return nil
}

// deleteEvalRanksByIDs 按主键删除失效的评估排序条目, 仅在事务内调用。
// 空集合(nil 或空切片)直接返回 nil, 保证幂等。
func deleteEvalRanksByIDs(tx *gorm.DB, rankIDs []int64) error {
	if len(rankIDs) == 0 {
		return nil
	}
	if err := tx.Where("id IN ?", rankIDs).Delete(&model.ModelEvalRank{}).Error; err != nil {
		return fmt.Errorf("按主键删除失效评估排序失败: %w", err)
	}
	return nil
}
