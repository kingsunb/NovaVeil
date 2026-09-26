package relay

// routableLeafCount 的单元测试: 直接钉死"全部满载"分母的语义——
// 沿引用链展开数可达叶子、跳过禁用渠道成员、按分组 ID 去重重复/环形引用,
// 而不直接取 len(group.Items)(会虚高, 导致 503 永不触发)。

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestRoutableLeafCountExpandsRefsAndSkipsDisabled(t *testing.T) {
	// 渠道查询: 奇数渠道 ID 启用, 偶数渠道 ID 视为禁用。
	oldChannelLookup := channelLookupFunc
	channelLookupFunc = func(id int) (model.Channel, error) {
		return model.Channel{ID: id, Enabled: id%2 != 0}, nil
	}
	t.Cleanup(func() { channelLookupFunc = oldChannelLookup })

	// 分组图:
	// top[1] = { leaf(cm=1, enabled), ref->sub, ref->sub(重复), leaf(cm=2, disabled) }
	// sub[2] = { leaf(cm=3, enabled), leaf(cm=4, disabled) }
	// 可达且非禁用的叶子 = top 的 cm1 + sub 的 cm3 = 2(重复引用按 sub 去重, cm3 只计一次)。
	top := model.Group{ID: 1, Mode: model.GroupModeFailover, Items: []model.GroupItem{
		{ID: 11, GroupID: 1, ChannelModelID: 1, ChannelModel: &model.ChannelModel{ID: 1, ChannelID: 1}},
		{ID: 12, GroupID: 1, RefGroupName: "sub"},
		{ID: 13, GroupID: 1, RefGroupName: "sub"},
		{ID: 14, GroupID: 1, ChannelModelID: 2, ChannelModel: &model.ChannelModel{ID: 2, ChannelID: 2}},
	}}
	sub := model.Group{ID: 2, Mode: model.GroupModeFailover, Items: []model.GroupItem{
		{ID: 21, GroupID: 2, ChannelModelID: 3, ChannelModel: &model.ChannelModel{ID: 3, ChannelID: 3}},
		{ID: 22, GroupID: 2, ChannelModelID: 4, ChannelModel: &model.ChannelModel{ID: 4, ChannelID: 4}},
	}}
	oldGroupLookup := groupLookupFunc
	groupLookupFunc = func(name string) (model.Group, error) { return sub, nil }
	t.Cleanup(func() { groupLookupFunc = oldGroupLookup })

	if got := routableLeafCount(top); got != 2 {
		t.Fatalf("routableLeafCount = %d, 期望 2(仅 cm1 与 cm3 非禁用; 重复引用按 sub 去重)", got)
	}
}

func TestRoutableLeafCountZeroForEmptyOrAllDisabled(t *testing.T) {
	oldChannelLookup := channelLookupFunc
	channelLookupFunc = func(id int) (model.Channel, error) {
		return model.Channel{ID: id, Enabled: false}, nil // 全部禁用
	}
	t.Cleanup(func() { channelLookupFunc = oldChannelLookup })

	empty := model.Group{ID: 1, Mode: model.GroupModeFailover}
	if got := routableLeafCount(empty); got != 0 {
		t.Fatalf("空分组的 routableLeafCount = %d, 期望 0", got)
	}

	allDisabled := model.Group{ID: 2, Mode: model.GroupModeFailover, Items: []model.GroupItem{
		{ID: 21, GroupID: 2, ChannelModelID: 1, ChannelModel: &model.ChannelModel{ID: 1, ChannelID: 1}},
		{ID: 22, GroupID: 2, ChannelModelID: 2, ChannelModel: &model.ChannelModel{ID: 2, ChannelID: 2}},
	}}
	if got := routableLeafCount(allDisabled); got != 0 {
		t.Fatalf("全部禁用分组的 routableLeafCount = %d, 期望 0", got)
	}
}