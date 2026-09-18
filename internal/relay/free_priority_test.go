package relay

import (
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
)

// stubFreeChannels 把渠道查询桩替换为免费/付费标记表, 未登记的渠道按不存在处理;
// 每个成员都启用, 保证选路只受优先级影响。测试结束自动还原。
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

// TestPickGroupItemFreeAndPaidTreatedEqually 验证免费渠道与付费渠道在选路中一视同仁:
// 选路 strictly 按优先级顺序, 免费渠道不会因分类而被前移或优先选中。
// 成员按 Priority 升序排列(passthroughGroup 构造时 [2]int{itemID, channelID} 的顺序即为优先级顺序),
// 第一个成员应被选中, 无论它指向的渠道是免费还是付费。
func TestPickGroupItemFreeAndPaidTreatedEqually(t *testing.T) {
	stubRelayEnv(t)

	// 场景 1: 付费渠道在前(优先级更高), 免费渠道在后。
	// 旧逻辑会选中免费渠道; 新逻辑应选中付费渠道(第一个)。
	stubFreeChannels(t, map[int]bool{101: false, 102: true})
	group := passthroughGroup(1, backoffConfig(10, 2, 30), [2]int{11, 101}, [2]int{12, 102})
	item := pickGroupItem(group, 0)
	if item.ID != 11 {
		t.Fatalf("选路应按优先级顺序选中第一个成员(付费), 实际选中 %d", item.ID)
	}

	// 场景 2: 免费渠道在前(优先级更高), 付费渠道在后。
	// 两种分类一视同仁, 第一个(免费)应被选中。
	stubFreeChannels(t, map[int]bool{101: true, 102: false})
	group2 := passthroughGroup(2, backoffConfig(10, 2, 30), [2]int{21, 101}, [2]int{22, 102})
	item2 := pickGroupItem(group2, 0)
	if item2.ID != 21 {
		t.Fatalf("选路应按优先级顺序选中第一个成员(免费), 实际选中 %d", item2.ID)
	}
}

// TestPickGroupItemFreeChannelFallbackOnCooldown 验证免费渠道冷却时回退到下一个成员:
// 与付费渠道行为完全一致——冷却的成员被跳过, 优先级顺序中的下一个可用成员被选中。
func TestPickGroupItemFreeChannelFallbackOnCooldown(t *testing.T) {
	stubRelayEnv(t)
	stubFreeChannels(t, map[int]bool{101: true, 102: false})
	group := passthroughGroup(1, backoffConfig(10, 2, 30), [2]int{11, 101}, [2]int{12, 102})

	// 第一个成员(免费)冷却中时, 回退到第二个成员(付费)。
	route := seedCooling(t, group, 11)
	route.Cooldowns[11] = time.Now().UnixMilli() + 60_000
	if item := pickGroupItem(group, 0); item.ID != 12 {
		t.Fatalf("第一个成员冷却时应回退到第二个成员, 实际选中 %d", item.ID)
	}

	// 分组快照自身顺序不被污染。
	if group.Items[0].ID != 11 || group.Items[1].ID != 12 {
		t.Fatalf("分组快照顺序被污染: %d, %d", group.Items[0].ID, group.Items[1].ID)
	}
}

// TestPickGroupItemManualModeIgnoresFreeClassification 验证手动模式按 ActiveItemID 返回,
// 与免费/付费分类无关, 保持历史行为一致。
func TestPickGroupItemManualModeIgnoresFreeClassification(t *testing.T) {
	stubRelayEnv(t)
	stubFreeChannels(t, map[int]bool{101: false, 102: true})
	group := passthroughGroup(1, backoffConfig(10, 2, 30), [2]int{11, 101}, [2]int{12, 102})
	group.Mode = model.GroupModeManual
	group.ActiveItemID = 11

	if item := pickGroupItem(group, 0); item.ID != 11 {
		t.Fatalf("手动模式应返回 ActiveItemID 11, 实际 %d", item.ID)
	}
}

// TestPickGroupItemPassthroughOrderPreservedRegardlessOfFreeStatus 验证 PreferPassthrough
// 协议重排不受免费/付费分类影响: 同协议成员整体前移, 分类不参与重排。
func TestPickGroupItemPassthroughOrderPreservedRegardlessOfFreeStatus(t *testing.T) {
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
		[2]int{13, 103}, // paid openai
		[2]int{14, 104}, // free anthropic
	)

	// anthropic 客户端: 协议重排后顺序为 [12 paid anth, 14 free anth, 11 free openai, 13 paid openai]。
	// 免费分类不再参与重排, 第一个同协议成员(12, 付费)应被选中。
	item := pickGroupItem(group, 0, llm.APIFormatAnthropicMessage)
	if item.ID != 12 {
		t.Fatalf("协议重排应选中第一个同协议成员(付费), 实际选中 %d", item.ID)
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
