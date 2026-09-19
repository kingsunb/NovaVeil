package op

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/utils/cache"
	"gorm.io/gorm"
)

// group 操作的 sentinel 错误: handler 用 errors.Is 区分 4xx(校验/冲突)与 5xx(系统),
// 让前端 toast 与 i18n 文案对得上。
var (
	ErrGroupNameRequired      = errors.New("group name is required")
	ErrGroupNotFound          = errors.New("group not found")
	ErrGroupRefTargetMissing  = errors.New("referenced group not found")
	ErrGroupReferencedBy      = errors.New("group is referenced and cannot be deleted")
	ErrGroupItemMissingTarget = errors.New("group item must reference a channel model or a group")
	ErrGroupDuplicateRef      = errors.New("duplicate reference to group in group items")
	ErrGroupDuplicateMember   = errors.New("duplicate channel model in group items")
	ErrGroupSelfReference     = errors.New("group cannot reference itself")
	ErrGroupRefDepthExceeded  = errors.New("reference chain exceeds max depth")
	ErrGroupRefCycle          = errors.New("reference cycle detected")
)

var (
	groupCache     = cache.New[int, model.Group](16) // 按主键保存完整分组配置。
	groupNameIndex = cache.New[string, int](16)      // 客户端模型名对应的分组主键。
)

// GroupList 返回缓存中的全部分组。
// 展示顺序：有自定义 DisplayOrder（≠0）的分组在前按值升序；其余按名称字典序，
// 保证多次调用与多实例之间顺序稳定，前端排序选项也可依赖这一基线。
func GroupList() []model.Group {
	groups := make([]model.Group, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		groups = append(groups, groupSnapshot(group))
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		oa, ob := a.DisplayOrder != 0, b.DisplayOrder != 0
		if oa != ob {
			return oa
		}
		if oa && ob {
			if a.DisplayOrder != b.DisplayOrder {
				return a.DisplayOrder < b.DisplayOrder
			}
		}
		return a.Name < b.Name
	})
	return groups
}

// GroupListModel 返回缓存中的全部分组模型名。
func GroupListModel() []string {
	models := make([]string, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		models = append(models, group.Name)
	}
	return models
}

// GroupGetByName 返回客户端模型名称对应的完整分组配置。
func GroupGetByName(name string) (model.Group, error) {
	groupID, ok := groupNameIndex.Get(name)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	group, ok := groupCache.Get(groupID)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return groupSnapshot(group), nil
}

// GroupCreate 创建分组及其成员并刷新缓存。
func GroupCreate(group *model.Group, ctx context.Context) error {
	if group == nil {
		return fmt.Errorf("group is required")
	}
	group.ID = 0
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		return ErrGroupNameRequired
	}
	group.ActiveItemID = 0
	// 未显式指定模式时默认故障转移: 新分组大多需要自动选路, 手动模式可以创建后再切。
	if group.Mode == "" {
		group.Mode = model.GroupModeFailover
	}
	model.NormalizeGroupRelayConfig(&group.RelayConfig)
	for i := range group.Items {
		group.Items[i].ID = 0
		group.Items[i].GroupID = 0
		group.Items[i].RefGroupName = strings.TrimSpace(group.Items[i].RefGroupName)
		group.Items[i].ChannelModel = nil
	}
	if err := normalizeAndValidateGroupItems(group.Items); err != nil {
		return err
	}
	// 成员防重复校验: 同组内同一渠道模型只能出现一次, 被引用分组名也不能重复;
	// 拆表后引用成员的渠道模型 ID 为 0 无法再依赖数据库唯一索引, 改由应用层保证。
	if err := validateGroupItemDuplicates(group.Items); err != nil {
		return err
	}
	// 引用成员校验: 目标存在、禁止自引、沿链防环限深; 创建中的分组尚未入库, 以名称参与自引判定。
	if err := validateGroupReferences(0, group.Name, group.Items); err != nil {
		return err
	}
	if err := db.GetDB().WithContext(ctx).Create(group).Error; err != nil {
		return err
	}
	sortGroupItems(group.Items)
	groupCache.Set(group.ID, groupSnapshot(*group))
	groupNameIndex.Set(group.Name, group.ID)
	return nil
}

// GroupUpdate 更新分组配置和成员，并返回刷新后的分组。
func GroupUpdate(req *model.GroupUpdateRequest, ctx context.Context) (*model.Group, error) {
	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	oldName := oldGroup.Name

	var selectFields []string
	updates := model.Group{ID: req.ID}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("group name is required")
		}
		// 被其他分组的引用成员指向时禁止重命名, 否则引用按名称解析会悬空。
		if name != oldGroup.Name {
			if referrer := firstGroupReferrer(oldGroup.Name, req.ID); referrer != "" {
				return nil, fmt.Errorf("group %q is referenced by group %q and cannot be renamed", oldGroup.Name, referrer)
			}
		}
		selectFields = append(selectFields, "name")
		updates.Name = name
	}
	if req.Mode != nil {
		selectFields = append(selectFields, "mode")
		updates.Mode = *req.Mode
	}
	if req.RelayConfig != nil {
		config := *req.RelayConfig
		model.NormalizeGroupRelayConfig(&config)
		selectFields = append(selectFields, "relay_config")
		updates.RelayConfig = config
	}
	if req.DisplayOrder != nil {
		selectFields = append(selectFields, "display_order")
		updates.DisplayOrder = *req.DisplayOrder
	}

	newItems := make([]model.GroupItem, len(req.ItemsToAdd))
	for i, item := range req.ItemsToAdd {
		newItems[i] = model.GroupItem{
			GroupID:        req.ID,
			ChannelModelID: item.ChannelModelID,
			RefGroupName:   strings.TrimSpace(item.RefGroupName),
			Priority:       item.Priority,
		}
	}
	if err := normalizeAndValidateGroupItems(newItems); err != nil {
		return nil, err
	}

	// 校验针对变更后生效的完整成员集合: 存量成员减去待删除项, 再并入待新增项。
	effectiveItems := make([]model.GroupItem, 0, len(oldGroup.Items)+len(newItems))
	if len(req.ItemsToDelete) > 0 {
		deleted := make(map[int]struct{}, len(req.ItemsToDelete))
		for _, id := range req.ItemsToDelete {
			deleted[id] = struct{}{}
		}
		for _, item := range oldGroup.Items {
			if _, gone := deleted[item.ID]; !gone {
				effectiveItems = append(effectiveItems, item)
			}
		}
	} else {
		effectiveItems = append(effectiveItems, oldGroup.Items...)
	}
	effectiveItems = append(effectiveItems, newItems...)
	if err := validateGroupItemDuplicates(effectiveItems); err != nil {
		return nil, err
	}
	if err := validateGroupReferences(req.ID, oldGroup.Name, effectiveItems); err != nil {
		return nil, err
	}

	var group model.Group
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(selectFields) > 0 {
			if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
				return fmt.Errorf("failed to update group: %w", err)
			}
		}

		// 删除 items
		if len(req.ItemsToDelete) > 0 {
			var deletedIDs []int
			if err := tx.Model(&model.GroupItem{}).
				Where("id IN ? AND group_id = ?", req.ItemsToDelete, req.ID).
				Pluck("id", &deletedIDs).Error; err != nil {
				return fmt.Errorf("failed to find deleted items: %w", err)
			}
			if len(deletedIDs) > 0 {
				if err := tx.Model(&model.Group{}).
					Where("id = ? AND active_item_id IN ?", req.ID, deletedIDs).
					Update("active_item_id", 0).Error; err != nil {
					return fmt.Errorf("failed to clear active item: %w", err)
				}
				if err := tx.Where("id IN ?", deletedIDs).Delete(&model.GroupItem{}).Error; err != nil {
					return fmt.Errorf("failed to delete items: %w", err)
				}
			}
		}

		// 批量更新 items
		if len(req.ItemsToUpdate) > 0 {
			ids := make([]int, len(req.ItemsToUpdate))
			priorityCase := "CASE id"
			for i, item := range req.ItemsToUpdate {
				ids[i] = item.ID
				priorityCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Priority)
			}
			priorityCase += " END"

			if err := tx.Model(&model.GroupItem{}).
				Where("id IN ? AND group_id = ?", ids, req.ID).
				Updates(map[string]interface{}{
					"priority": gorm.Expr(priorityCase),
				}).Error; err != nil {
				return fmt.Errorf("failed to update items: %w", err)
			}
		}

		// 批量新增 items
		if len(newItems) > 0 {
			if err := tx.Create(&newItems).Error; err != nil {
				return fmt.Errorf("failed to create items: %w", err)
			}
		}

		if err := tx.Preload("Items").First(&group, req.ID).Error; err != nil {
			return fmt.Errorf("failed to load updated group: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortGroupItems(group.Items)
	snapshot := groupSnapshot(group)
	groupCache.Set(group.ID, snapshot)
	groupNameIndex.Set(group.Name, group.ID)
	if oldName != group.Name {
		groupNameIndex.Del(oldName)
	}
	return &snapshot, nil
}

// GroupActiveItemUpdate 更新或清空分组当前手动指定的成员。
func GroupActiveItemUpdate(groupID int, req *model.GroupActiveItemUpdateRequest, ctx context.Context) (*model.Group, error) {
	group, ok := groupCache.Get(groupID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	itemID := 0
	if req.ItemID != nil && *req.ItemID != 0 {
		itemID = *req.ItemID
		found := false
		for _, item := range group.Items {
			if item.ID == itemID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("group item not found")
		}
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Group{}).Where("id = ?", groupID).Update("active_item_id", itemID).Error; err != nil {
		return nil, fmt.Errorf("failed to update active item: %w", err)
	}
	group.ActiveItemID = itemID
	snapshot := groupSnapshot(group)
	groupCache.Set(group.ID, snapshot)
	return &snapshot, nil
}

// GroupDel 删除分组及其成员，成员删除不会影响被其他分组引用的渠道模型。
// 先校验没有其它分组仍在引用本分组的 ref_group_name, 否则会留下悬空引用,
// 引用方请求会落到 errNoAvailableChannels, 无审计日志与告警。
func GroupDel(id int, ctx context.Context) error {
	group, ok := groupCache.Get(id)
	if !ok {
		return ErrGroupNotFound
	}
	if referrer := firstGroupReferrer(group.Name, id); referrer != "" {
		return fmt.Errorf("%w: %q is referenced by group %q", ErrGroupReferencedBy, group.Name, referrer)
	}
	if err := db.GetDB().WithContext(ctx).Delete(&model.Group{}, id).Error; err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}
	groupCache.Del(id)
	groupNameIndex.Del(group.Name)
	return nil
}

// normalizeAndValidateGroupItems 规范化分组成员并按成员类型分流校验:
// 渠道成员验证引用的渠道模型真实存在, 引用成员交由 validateGroupReferences 统一校验。
func normalizeAndValidateGroupItems(items []model.GroupItem) error {
	for i := range items {
		items[i].RefGroupName = strings.TrimSpace(items[i].RefGroupName)
		item := &items[i]
		if item.IsGroupRef() {
			continue
		}
		if item.ChannelModelID == 0 {
			return fmt.Errorf("%w (item %d)", ErrGroupItemMissingTarget, i)
		}
		if _, err := ChannelModelGet(item.ChannelModelID); err != nil {
			return fmt.Errorf("channel model %d not found: %w", item.ChannelModelID, ErrGroupItemMissingTarget)
		}
	}
	return nil
}

// validateGroupItemDuplicates 保证同组内成员不重复: 渠道成员的渠道模型 ID 唯一,
// 引用成员的被引用分组名唯一。拆表后 group_items 的旧 (group_id, channel_id, model_name)
// 数据库唯一索引已移除(引用成员双零值会撞约束), 防重复职责由本函数在应用层承担。
func validateGroupItemDuplicates(items []model.GroupItem) error {
	channelModelSeen := make(map[int]struct{}, len(items))
	refSeen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.IsGroupRef() {
			if _, dup := refSeen[item.RefGroupName]; dup {
				return fmt.Errorf("%w: %q", ErrGroupDuplicateRef, item.RefGroupName)
			}
			refSeen[item.RefGroupName] = struct{}{}
			continue
		}
		if item.ChannelModelID == 0 {
			return fmt.Errorf("%w (item missing channel model)", ErrGroupDuplicateMember)
		}
		if _, dup := channelModelSeen[item.ChannelModelID]; dup {
			return fmt.Errorf("%w: %d", ErrGroupDuplicateMember, item.ChannelModelID)
		}
		channelModelSeen[item.ChannelModelID] = struct{}{}
	}
	return nil
}

// groupRefTargetNames 返回成员列表中全部引用成员指向的分组名, 按出现顺序去重。
func groupRefTargetNames(items []model.GroupItem) []string {
	names := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !item.IsGroupRef() || item.RefGroupName == "" {
			continue
		}
		if _, dup := seen[item.RefGroupName]; dup {
			continue
		}
		seen[item.RefGroupName] = struct{}{}
		names = append(names, item.RefGroupName)
	}
	return names
}

// validateGroupReferences 校验变更后生效成员里的全部引用成员:
// 目标分组必须存在; 禁止自引; 沿引用链 DFS 防环并限制深度。
// selfID 为被校验分组的主键(创建时为 0), selfName 为其名称,
// 用于识别"引用到改名前/创建中的自己"这类经名称解析仍会落回自身的自引形态。
func validateGroupReferences(selfID int, selfName string, items []model.GroupItem) error {
	for _, name := range groupRefTargetNames(items) {
		target, err := GroupGetByName(name)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrGroupRefTargetMissing, name)
		}
		visited := map[int]bool{target.ID: true}
		if err := walkGroupRefChain(target, selfID, selfName, visited, 1); err != nil {
			return fmt.Errorf("reference to group %q invalid: %w", name, err)
		}
	}
	return nil
}

// walkGroupRefChain 从当前分组沿引用边深度优先搜索: 命中被编辑分组自身即判定自引/成环,
// 链上分组数超过 model.MaxGroupRefDepth 判定超限。visited 记录途中分组主键,
// 既防环死循环也避免菱形引用重复展开; 链上悬空引用不再延伸, 由运行期按本轮失败处理。
func walkGroupRefChain(current model.Group, selfID int, selfName string, visited map[int]bool, depth int) error {
	if current.ID == selfID || (selfID == 0 && selfName != "" && current.Name == selfName) {
		return fmt.Errorf("%w: %q", ErrGroupSelfReference, current.Name)
	}
	if depth > model.MaxGroupRefDepth {
		return fmt.Errorf("%w (max %d, current depth %d)", ErrGroupRefDepthExceeded, model.MaxGroupRefDepth, depth)
	}
	for _, name := range groupRefTargetNames(current.Items) {
		next, err := GroupGetByName(name)
		if err != nil {
			continue
		}
		if visited[next.ID] {
			return fmt.Errorf("%w: %q", ErrGroupRefCycle, next.Name)
		}
		visited[next.ID] = true
		if err := walkGroupRefChain(next, selfID, selfName, visited, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// firstGroupReferrer 返回除 excludeID 外第一个引用了 groupName 的分组名, 无引用者时返回空串。
func firstGroupReferrer(groupName string, excludeID int) string {
	for _, group := range groupCache.GetAll() {
		if group.ID == excludeID {
			continue
		}
		for _, item := range group.Items {
			if item.IsGroupRef() && item.RefGroupName == groupName {
				return group.Name
			}
		}
	}
	return ""
}

// GroupClearCooldownResult 描述一次分组冷却清理动作的统计信息, 用于响应体与日志。
type GroupClearCooldownResult struct {
	GroupID      int `json:"group_id"`      // 被清理的分组主键
	Channels     int `json:"channels"`      // 命中的渠道数量(含引用成员间接命中的渠道)
	MemberItems  int `json:"member_items"`  // 清理的成员级冷却条目数(Cooldowns/Levels/PostCommitStrikes/emergencyBlocks 任一非零)
	KeyCooldowns int `json:"key_cooldowns"` // 已清理的渠道 Key 冷却条目数
	RateWindows  int `json:"rate_windows"`  // 已清理的 Key 速率窗口条目数
}

// collectGroupChannelIDs 按引用链展开, 收集分组下全部渠道成员关联的 channel ID。
// visited 防自引/成环; 引用悬空按运行时失败语义静默忽略, 不阻断其他成员。
func collectGroupChannelIDs(groupID int) (map[int]struct{}, error) {
	channelIDs := make(map[int]struct{})
	group, ok := groupCache.Get(groupID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	visitedGroups := map[int]bool{groupID: true}

	var collect func(items []model.GroupItem) error
	collect = func(items []model.GroupItem) error {
		for _, item := range items {
			if item.IsGroupRef() {
				target, err := GroupGetByName(item.RefGroupName)
				if err != nil {
					continue
				}
				if visitedGroups[target.ID] {
					continue
				}
				visitedGroups[target.ID] = true
				if err := collect(target.Items); err != nil {
					return err
				}
				continue
			}
			if item.ChannelModelID == 0 {
				continue
			}
			channelModel, err := ChannelModelGet(item.ChannelModelID)
			if err != nil {
				continue
			}
			if channelModel.ChannelID == 0 {
				continue
			}
			channelIDs[channelModel.ChannelID] = struct{}{}
		}
		return nil
	}
	if err := collect(group.Items); err != nil {
		return nil, err
	}
	return channelIDs, nil
}

// GroupClearKeyCooldown 清空指定分组下全部渠道成员的 Key 冷却与限速窗口, 含引用成员间接命中的渠道。
// 只清内存中的 Key 冷却表与限速窗口(进程内 map); 成员级冷却由调用方另行调用 relay.ResetGroupCooldown,
// 避免 op 与 relay 之间出现相互 import。
// 返回的 channelIDs 同时暴露给调用方, 以便 handler 直接对每个渠道执行 relay 层的清 Key 操作,
// 无需让 op 越过包边界去调用 relay。
func GroupClearKeyCooldown(groupID int) (GroupClearCooldownResult, []int, error) {
	result := GroupClearCooldownResult{GroupID: groupID}
	channelIDSet, err := collectGroupChannelIDs(groupID)
	if err != nil {
		return result, nil, err
	}
	channelIDs := make([]int, 0, len(channelIDSet))
	for id := range channelIDSet {
		channelIDs = append(channelIDs, id)
	}
	sort.Ints(channelIDs)
	result.Channels = len(channelIDs)
	return result, channelIDs, nil
}

// groupRefreshCache 从数据库刷新完整分组缓存和名称索引。
func groupRefreshCache(ctx context.Context) error {
	groups := []model.Group{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Find(&groups).Error; err != nil {
		return err
	}
	// 以 RefreshAll 原子替换缓存和名称索引，消除先 Clear 再 Set 的空窗口。
	groupMap := make(map[int]model.Group, len(groups))
	nameIndexMap := make(map[string]int, len(groups))
	for _, group := range groups {
		sortGroupItems(group.Items)
		groupMap[group.ID] = groupSnapshot(group)
		nameIndexMap[group.Name] = group.ID
	}
	groupCache.RefreshAll(groupMap)
	groupNameIndex.RefreshAll(nameIndexMap)
	return nil
}

// sortGroupItems 按优先级和主键生成稳定的成员顺序。
func sortGroupItems(items []model.GroupItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
}

// groupSnapshot 从渠道模型缓存补齐成员关联对象。
func groupSnapshot(group model.Group) model.Group {
	group.Items = slices.Clone(group.Items)
	for i := range group.Items {
		channelModel, err := ChannelModelGet(group.Items[i].ChannelModelID)
		if err != nil {
			group.Items[i].ChannelModel = nil
			continue
		}
		group.Items[i].ChannelModel = &channelModel
	}
	return group
}

// 以当前排序整体替换 auto 分组的 sentinel 错误。
var ErrGroupReplaceNoRankable = errors.New("没有可用于分组的评估结果")

// GroupReplaceEntry 描述分组容错更新中单条排序记录的处理明细:
// 渠道停用但模型仍在而保留, 或模型删除/改名而清理, 不含任何渠道凭据字段。
type GroupReplaceEntry struct {
	ChannelID      int    `json:"channel_id"`
	ChannelModelID int    `json:"channel_model_id"`
	ChannelName    string `json:"channel_name"`
	ModelName      string `json:"model_name"`
}

// GroupReplaceReport 汇总一次分组容错更新的处理说明:
// KeptDisabled = 渠道停用但模型仍在而保留入组的记录;
// CleanedStale = 模型删除/改名而清理的失效记录。
type GroupReplaceReport struct {
	KeptDisabled []GroupReplaceEntry `json:"kept_disabled"`
	CleanedStale []GroupReplaceEntry `json:"cleaned_stale"`
}

// GroupReplaceItemsByName 以当前排序整体替换指定名称的分组成员：
// 仅纳入请求成功的评估（ok 和 violation）、逐条分类渠道与模型状态后事务内原子写入。
// 渠道停用但模型仍在的记录保留入组并保持原排名；模型删除/改名的失效记录在同事务内清理，
// 不因单个记录不可用而整体中止。分类读缓存, 命中项在事务内按库复核, 消除「分类后
// 被并发删除留下悬空分组成员」的竞态。分组不存在则按 failover + 默认 Relay 配置创建。
// 返回分组快照、处理说明与是否新建。
func GroupReplaceItemsByName(ctx context.Context, name string, ranks []model.ModelEvalRankSummary) (*model.Group, GroupReplaceReport, bool, error) {
	name = strings.TrimSpace(name)
	report := GroupReplaceReport{
		KeptDisabled: make([]GroupReplaceEntry, 0),
		CleanedStale: make([]GroupReplaceEntry, 0),
	}
	rankable := make([]model.ModelEvalRankSummary, 0, len(ranks))
	for _, r := range ranks {
		if isRankableOutcome(r.Outcome) {
			rankable = append(rankable, r)
		}
	}
	if len(rankable) == 0 {
		return nil, report, false, ErrGroupReplaceNoRankable
	}
	// position 越小越靠前，映射到分组 priority 也越小越靠前（路由优先选）。
	sort.SliceStable(rankable, func(i, j int) bool {
		if rankable[i].Position != rankable[j].Position {
			return rankable[i].Position < rankable[j].Position
		}
		return rankable[i].ID < rankable[j].ID
	})

	// 逐条分类「有效入组 / 失效清理」，不因单条不可用而中止。分类读的是缓存:
	// 渠道/模型删除事务可能恰在分类之后提交, 命中结果可能已过期, 故命中项在
	// 下方事务内再按库复核。report 明细待复核后组装, 回滚路径不污染明细。
	resolvedRankIDs := make([]int64, 0, len(rankable))
	resolvedModelIDs := make([]int, 0, len(rankable))
	resolvedEntries := make([]GroupReplaceEntry, 0, len(rankable))
	resolvedKeptDisabled := make([]bool, 0, len(rankable))
	staleRankIDs := make([]int64, 0)
	staleEntries := make([]GroupReplaceEntry, 0)
	for _, r := range rankable {
		entry := GroupReplaceEntry{
			ChannelID:      r.ChannelID,
			ChannelModelID: r.ChannelModelID,
			ChannelName:    r.ChannelName,
			ModelName:      r.ModelName,
		}
		cm, err := ChannelModelGet(r.ChannelModelID)
		if err != nil {
			// 模型删除/改名: 失效, 标记清理。
			log.Warnf("eval rank stale (channel model missing): %s / %s, will be cleaned", r.ChannelName, r.ModelName)
			staleRankIDs = append(staleRankIDs, r.ID)
			staleEntries = append(staleEntries, entry)
			continue
		}
		channel, err := ChannelGetCore(cm.ChannelID)
		if err != nil {
			// 渠道整体已删(模型应已级联缺失, 防御分支): 失效, 标记清理。
			log.Warnf("eval rank stale (channel missing): %s / %s, will be cleaned", r.ChannelName, r.ModelName)
			staleRankIDs = append(staleRankIDs, r.ID)
			staleEntries = append(staleEntries, entry)
			continue
		}
		resolvedModelIDs = append(resolvedModelIDs, cm.ID)
		resolvedRankIDs = append(resolvedRankIDs, r.ID)
		resolvedEntries = append(resolvedEntries, entry)
		resolvedKeptDisabled = append(resolvedKeptDisabled, !channel.Enabled)
	}

	// 删失效排序与后续成员替换共用同一把锁, 避免与并发写入 position 交错。
	modelEvalRankMu.Lock()
	defer modelEvalRankMu.Unlock()

	created := false
	var group model.Group
	allStale := false
	// demotedIdx 记录事务内复核降级的 resolved 下标; 明细在事务提交成功后并入
	// report, 降级成员不再计入 KeptDisabled。
	var demotedIdx []int
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 事务内按库复核缓存命中的成员, 悬空分组成员的窗口在此闭合: 复核与删
		// 失效、清旧成员、写新成员同事务, SQLite 写事务全库串行, 复核之后到提
		// 交之前不会再有删除事务提交; MySQL/PG 下窗口亦缩至近零。渠道删除与其
		// 模型删除同事务级联, 故只复核模型存在性即可覆盖渠道删除。
		var liveModelIDs []int
		if len(resolvedModelIDs) > 0 {
			if err := tx.Model(&model.ChannelModel{}).Where("id IN ?", resolvedModelIDs).
				Pluck("id", &liveModelIDs).Error; err != nil {
				return fmt.Errorf("复核渠道模型存在性失败: %w", err)
			}
		}
		liveSet := make(map[int]struct{}, len(liveModelIDs))
		for _, id := range liveModelIDs {
			liveSet[id] = struct{}{}
		}
		for i, cmID := range resolvedModelIDs {
			if _, ok := liveSet[cmID]; ok {
				continue
			}
			log.Warnf("eval rank stale (revalidated missing): %s / %s, will be cleaned",
				resolvedEntries[i].ChannelName, resolvedEntries[i].ModelName)
			staleRankIDs = append(staleRankIDs, resolvedRankIDs[i])
			demotedIdx = append(demotedIdx, i)
		}

		// 先在同事务内清理失效排序, 保证「删失效 + 清旧成员 + 写新成员」原子完成。
		if err := deleteEvalRanksByIDs(tx, staleRankIDs); err != nil {
			return err
		}

		// 全部失效(rankable 非空但无复核存活成员): 仅清理失效排序, 不产生空分组。
		liveCount := len(resolvedModelIDs) - len(demotedIdx)
		if liveCount == 0 {
			allStale = true
			return nil
		}

		// 复核存活的成员按分类顺序入组, priority 递减、首项最高。
		newItems := make([]model.GroupItem, 0, liveCount)
		for _, cmID := range resolvedModelIDs {
			if _, ok := liveSet[cmID]; !ok {
				continue
			}
			newItems = append(newItems, model.GroupItem{
				ChannelModelID: cmID,
				RefGroupName:   "",
				Priority:       len(newItems) + 1,
			})
		}

		existingID, exists := groupNameIndex.Get(name)
		if exists {
			// 清空可能指向被删成员的 active_item。
			if err := tx.Model(&model.Group{}).Where("id = ? AND active_item_id > 0", existingID).
				Update("active_item_id", 0).Error; err != nil {
				return fmt.Errorf("failed to clear active item: %w", err)
			}
			if err := tx.Where("group_id = ?", existingID).Delete(&model.GroupItem{}).Error; err != nil {
				return fmt.Errorf("failed to clear group items: %w", err)
			}
			for i := range newItems {
				newItems[i].ID = 0
				newItems[i].GroupID = existingID
			}
			if err := tx.Create(&newItems).Error; err != nil {
				return fmt.Errorf("failed to create group items: %w", err)
			}
			if err := tx.Preload("Items").First(&group, existingID).Error; err != nil {
				return fmt.Errorf("failed to load group: %w", err)
			}
			return nil
		}

		created = true
		g := model.Group{
			Name:         name,
			Mode:         model.GroupModeFailover,
			ActiveItemID: 0,
			RelayConfig:  model.DefaultGroupRelayConfig(),
			Items:        newItems,
		}
		for i := range g.Items {
			g.Items[i].ID = 0
			g.Items[i].GroupID = 0
		}
		if err := tx.Create(&g).Error; err != nil {
			return fmt.Errorf("failed to create group: %w", err)
		}
		group = g
		return nil
	})
	if err != nil {
		return nil, report, false, err
	}

	// 事务已提交: 组装最终处理明细 = 缓存未命中的失效 + 事务内复核降级的失效 +
	// 复核存活者里渠道停用的保留。降级成员不再计入 KeptDisabled。
	demoted := make(map[int]struct{}, len(demotedIdx))
	for _, i := range demotedIdx {
		demoted[i] = struct{}{}
	}
	report.CleanedStale = staleEntries
	for i, entry := range resolvedEntries {
		if _, ok := demoted[i]; ok {
			report.CleanedStale = append(report.CleanedStale, entry)
			continue
		}
		if resolvedKeptDisabled[i] {
			report.KeptDisabled = append(report.KeptDisabled, entry)
		}
	}
	if allStale {
		return nil, report, false, ErrGroupReplaceNoRankable
	}

	sortGroupItems(group.Items)
	snapshot := groupSnapshot(group)
	groupCache.Set(group.ID, snapshot)
	groupNameIndex.Set(group.Name, group.ID)
	return &snapshot, report, created, nil
}
