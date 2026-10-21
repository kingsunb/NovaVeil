package relay

// routableLeafCount 的单元测试: 直接钉死"全部满载"分母的语义——
// 沿引用链展开数可达叶子、跳过禁用渠道成员、按分组 ID 去重重复/环形引用,
// 而不直接取 len(group.Items)(会虚高, 导致 503 永不触发)。
// 手动模式引用只计其激活成员。

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

// TestRoutableLeafCountManualRefCountsActiveOnly 故障转移引用手动分组时只计激活成员。
func TestRoutableLeafCountManualRefCountsActiveOnly(t *testing.T) {
	stubRelayEnv(t)
	manual := model.Group{ID: 2, Name: "manual", Mode: model.GroupModeManual, ActiveItemID: 21}
	manual.Items = []model.GroupItem{
		refTestLeafItem(21, 2001, "active"),
		refTestLeafItem(22, 2002, "idle-b"),
		refTestLeafItem(23, 2003, "idle-c"),
	}
	top := model.Group{ID: 1, Name: "top", Mode: model.GroupModeFailover}
	top.Items = []model.GroupItem{refTestRefItem(11, "manual"), refTestLeafItem(12, 2004, "direct")}
	refStubGroups(t, map[string]model.Group{"manual": manual})

	if got := routableLeafCount(top); got != 2 {
		t.Fatalf("引用手动分组应只计激活成员: 1 引用 + 1 直连 = 2, 实际 %d", got)
	}
}

// TestRoutableLeafCountManualRefExpandsFailoverTarget 手动激活成员引用故障转移分组时展开全部叶子。
func TestRoutableLeafCountManualRefExpandsFailoverTarget(t *testing.T) {
	stubRelayEnv(t)
	sub := testFailoverGroup(3, backoffConfig(10, 2, 30), 31, 32)
	manual := model.Group{ID: 2, Name: "manual", Mode: model.GroupModeManual, ActiveItemID: 41}
	manual.Items = []model.GroupItem{refTestRefItem(41, sub.Name), refTestLeafItem(42, 2002, "idle")}
	refStubGroups(t, map[string]model.Group{sub.Name: sub})

	if got := routableCandidateCount(manual); got != 2 {
		t.Fatalf("手动激活成员引用故障转移分组应展开 2 个叶子, 实际 %d", got)
	}
	top := model.Group{ID: 1, Name: "top", Mode: model.GroupModeFailover}
	top.Items = []model.GroupItem{refTestRefItem(11, "manual")}
	if got := routableLeafCount(top); got != 2 {
		t.Fatalf("故障转移引用手动分组再引用故障转移分组应计 2 个叶子, 实际 %d", got)
	}
}

// TestRoutableLeafCountManualChainCountsFinalActive 手动两级引用只计最终激活成员。
func TestRoutableLeafCountManualChainCountsFinalActive(t *testing.T) {
	stubRelayEnv(t)
	c := model.Group{ID: 4, Name: "c", Mode: model.GroupModeManual, ActiveItemID: 43}
	c.Items = []model.GroupItem{refTestLeafItem(43, 2003, "final"), refTestLeafItem(44, 2004, "idle-c")}
	b := model.Group{ID: 3, Name: "b", Mode: model.GroupModeManual, ActiveItemID: 33}
	b.Items = []model.GroupItem{refTestRefItem(33, "c"), refTestLeafItem(34, 2005, "idle-b")}
	a := model.Group{ID: 2, Name: "a", Mode: model.GroupModeManual, ActiveItemID: 22}
	a.Items = []model.GroupItem{refTestRefItem(22, "b"), refTestLeafItem(23, 2006, "idle-a")}
	refStubGroups(t, map[string]model.Group{"a": a, "b": b, "c": c})

	if got := routableCandidateCount(a); got != 1 {
		t.Fatalf("手动两级引用应只计最终激活成员, 实际 %d", got)
	}
	top := model.Group{ID: 1, Name: "top", Mode: model.GroupModeFailover}
	top.Items = []model.GroupItem{refTestRefItem(11, "a")}
	if got := routableLeafCount(top); got != 1 {
		t.Fatalf("故障转移引用手动两级链应计 1 个叶子, 实际 %d", got)
	}
}

// TestRoutableLeafCountRefCycleAvoidsDoubleCount 故障转移互引环按分组去重且不 panic。
func TestRoutableLeafCountRefCycleAvoidsDoubleCount(t *testing.T) {
	stubRelayEnv(t)
	f := model.Group{ID: 1, Name: "f", Mode: model.GroupModeFailover}
	f.Items = []model.GroupItem{refTestLeafItem(11, 2001, "f-leaf"), refTestRefItem(12, "g")}
	g := model.Group{ID: 2, Name: "g", Mode: model.GroupModeFailover}
	g.Items = []model.GroupItem{refTestLeafItem(21, 2002, "g-leaf"), refTestRefItem(22, "f")}
	refStubGroups(t, map[string]model.Group{"f": f, "g": g})

	if got := routableLeafCount(f); got != 2 {
		t.Fatalf("互引环应按分组去重各计 1 个叶子, 实际 %d", got)
	}
}

// TestRoutableLeafCountManualCycleCountsZero 手动自引环无可达叶子, 重复引用不重复计数。
func TestRoutableLeafCountManualCycleCountsZero(t *testing.T) {
	stubRelayEnv(t)
	loop := model.Group{ID: 3, Name: "loop", Mode: model.GroupModeManual, ActiveItemID: 31}
	loop.Items = []model.GroupItem{refTestRefItem(31, "loop"), refTestLeafItem(32, 2003, "idle")}
	refStubGroups(t, map[string]model.Group{"loop": loop})
	top := model.Group{ID: 4, Name: "top", Mode: model.GroupModeFailover}
	top.Items = []model.GroupItem{refTestRefItem(41, "loop"), refTestRefItem(42, "loop")}

	if got := routableLeafCount(top); got != 0 {
		t.Fatalf("手动自引环无可达叶子且重复引用不得重复计数, 实际 %d", got)
	}
}