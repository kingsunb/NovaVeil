package relay

import (
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// stubFreeChannels 把渠道查询桩替换为免费/付费标记表, 未登记的渠道按不存在处理;
// 每个成员都启用, 保证选路只受分类与优先级影响。测试结束自动还原。
func stubFreeChannels(t *testing.T, freeByID map[int]bool) {
	t.Helper()
	channelLookupFunc = func(id int) (model.Channel, error) {
		isFree, ok := freeByID[id]
		if !ok {
			return model.Channel{}, fmt.Errorf("channel not found: %d", id)
		}
		return model.Channel{ID: id, Enabled: true, IsFree: isFree}, nil
	}
}

// stubFreeTypedChannels 在 free/paid 标记之外同时提供渠道类型, 供与 PreferPassthrough 叠加的测试使用。
func stubFreeTypedChannels(t *testing.T, freeByID map[int]bool, providers map[int]model.ChannelProvider) {
	t.Helper()
	channelLookupFunc = func(id int) (model.Channel, error) {
		isFree, ok := freeByID[id]
		if !ok {
			return model.Channel{}, fmt.Errorf("channel not found: %d", id)
		}
		provider, ok := providers[id]
		if !ok {
			return model.Channel{}, fmt.Errorf("provider not found: %d", id)
		}
		return model.Channel{ID: id, Enabled: true, Type: provider, IsFree: isFree}, nil
	}
}

// TestFreePriorityOrderedItemsStablePartition 验证免费渠道整体前移、付费渠道保持在后,
// 且各自内部顺序不变; 全同分类时原样返回。
func TestFreePriorityOrderedItemsStablePartition(t *testing.T) {
	stubRelayEnv(t)
	stubFreeChannels(t, map[int]bool{101: true, 102: false, 103: true, 104: false})
	group := passthroughGroup(1, backoffConfig(10, 2, 30),
		[2]int{11, 101}, [2]int{12, 102}, [2]int{13, 103}, [2]int{14, 104})

	ordered := freePriorityOrderedItems(group.Items)
	got := make([]int, 0, len(ordered))
	for _, item := range ordered {
		got = append(got, item.ID)
	}
	want := []int{11, 13, 12, 14}
	if !equalInts(got, want) {
		t.Fatalf("freePriorityOrderedItems = %v, want %v", got, want)
	}

	// 全部免费或全部付费时不产生新切片内容(仍返回原切片)。
	allFree := passthroughGroup(2, backoffConfig(10, 2, 30), [2]int{21, 101}, [2]int{22, 103})
	if ordered := freePriorityOrderedItems(allFree.Items); &ordered[0] != &allFree.Items[0] {
		t.Fatal("全部免费时不应重排复制")
	}
	allPaid := passthroughGroup(3, backoffConfig(10, 2, 30), [2]int{31, 102}, [2]int{32, 104})
	if ordered := freePriorityOrderedItems(allPaid.Items); &ordered[0] != &allPaid.Items[0] {
		t.Fatal("全部付费时不应重排复制")
	}
}

// TestPickGroupItemFreeChannelFirst 验证免费渠道在故障转移中优先于付费渠道:
// 即使免费渠道的优先级数值更低(排在付费渠道后面), 也会被优先选中。
func TestPickGroupItemFreeChannelFirst(t *testing.T) {
	stubRelayEnv(t)
	stubFreeChannels(t, map[int]bool{101: false, 102: true})
	group := passthroughGroup(1, backoffConfig(10, 2, 30), [2]int{11, 101}, [2]int{12, 102})

	item := pickGroupItem(group, 0)
	if item.ID != 12 {
		t.Fatalf("免费渠道应优先于付费渠道, 实际选中成员 %d", item.ID)
	}

	// 免费渠道冷却中时回退到付费渠道, 不因分类优先而空等。
	route := seedCooling(t, group, 12)
	route.Cooldowns[12] = time.Now().UnixMilli() + 60_000
	if item := pickGroupItem(group, 0); item.ID != 11 {
		t.Fatalf("免费渠道不可用时应回退付费渠道, 实际选中成员 %d", item.ID)
	}

	// 重排不污染分组快照自身顺序。
	if group.Items[0].ID != 11 || group.Items[1].ID != 12 {
		t.Fatalf("分组快照顺序被免费重排污染: %d, %d", group.Items[0].ID, group.Items[1].ID)
	}
}

// TestPickGroupItemManualModeIgnoresFreeOrdering 验证手动模式不受免费分类重排影响:
// 手动指定激活成员仍按 ActiveItemID 返回, 与历史行为一致。
func TestPickGroupItemManualModeIgnoresFreeOrdering(t *testing.T) {
	stubRelayEnv(t)
	stubFreeChannels(t, map[int]bool{101: false, 102: true})
	group := passthroughGroup(1, backoffConfig(10, 2, 30), [2]int{11, 101}, [2]int{12, 102})
	group.Mode = model.GroupModeManual
	group.ActiveItemID = 11

	if item := pickGroupItem(group, 0); item.ID != 11 {
		t.Fatalf("手动模式应返回 ActiveItemID 11, 实际 %d", item.ID)
	}
}

// TestPickGroupItemFreePriorityWorksWithPreferPassthrough 验证免费分类与同协议优先叠加:
// 先按透传偏好重排, 再把免费渠道整体前移。免费跨协议渠道会优先于付费同协议渠道,
// 但同分类内部仍保持透传偏好(付费同协议在前)。
func TestPickGroupItemFreePriorityWorksWithPreferPassthrough(t *testing.T) {
	stubRelayEnv(t)
	stubFreeTypedChannels(t,
		map[int]bool{101: true, 102: false, 103: false, 104: true},
		map[int]model.ChannelProvider{
			101: model.ChannelProviderOpenAI,
			102: model.ChannelProviderAnthropic,
			103: model.ChannelProviderOpenAI,
			104: model.ChannelProviderAnthropic,
		},
	)
	group := passthroughGroup(1, passthroughConfig(true),
		[2]int{11, 101}, // free openai
		[2]int{12, 102}, // paid anthropic
		[2]int{13, 103}, // paid openai (higher priority than 12 after protocol reorder? keep original)
		[2]int{14, 104}, // free anthropic
	)

	// anthropic 客户端: 协议重排后顺序为 [12 paid anth, 14 free anth, 11 free openai, 13 paid openai],
	// 免费再前移为 [14 free anth, 11 free openai, 12 paid anth, 13 paid openai]。
	item := pickGroupItem(group, 0, llm.APIFormatAnthropicMessage)
	if item.ID != 14 {
		t.Fatalf("免费渠道应优先于付费渠道, 且免费分类内同协议优先, 实际选中成员 %d", item.ID)
	}
}

// equalInts 报告两个 int 切片是否逐元素相等。
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
