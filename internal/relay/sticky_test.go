package relay

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// stickyTestItem 构造一个分组成员。
func stickyTestItem(id int) model.GroupItem {
	return model.GroupItem{ID: id, GroupID: 1, ChannelModel: &model.ChannelModel{ChannelID: 100 + id, Name: "m"}, Priority: id}
}

// resetStickyState 清空粘合与路由全局状态, 避免用例间相互污染。
// 顺带归零顺带清理的节流时间戳: 生产上清理按 60s 间隔节流, 用例依赖
// "bind 立即清理已删除成员残留"的语义, 归零后首次 bind 必然触发清理。
func resetStickyState() {
	routeMu.Lock()
	defer routeMu.Unlock()
	sessionStickies = make(map[int]map[string]stickyEntry)
	routes = make(map[int]*RouteState)
	stickyLastPrune.Store(0)
}

// stickyEntryOf 在锁内读取指定会话的粘合记录。
func stickyEntryOf(t *testing.T, groupID int, key string) (stickyEntry, bool) {
	t.Helper()
	routeMu.Lock()
	defer routeMu.Unlock()
	entry, ok := sessionStickies[groupID][key]
	return entry, ok
}

// stickyTestGroup 构造启用会话粘合的故障转移分组。
func stickyTestGroup(seconds int, items ...model.GroupItem) model.Group {
	return model.Group{
		ID:    1,
		Name:  "g",
		Mode:  model.GroupModeFailover,
		Items: items,
		RelayConfig: model.GroupRelayConfig{
			SessionStickyEnabled: true,
			SessionStickySeconds: seconds,
		},
	}
}

func TestSessionSticky(t *testing.T) {
	t.Run("命中", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))

		bindSessionSticky(group, "s1", 12)
		item := pickSessionSticky(group, "s1")
		if item.ID != 12 {
			t.Fatalf("粘合成员 = %d, 期望 12", item.ID)
		}
	})

	t.Run("过期失效", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))
		bindSessionSticky(group, "s1", 12)

		// 直接把过期时间拨到过去, 模拟粘合超时。
		routeMu.Lock()
		entry := sessionStickies[1]["s1"]
		entry.ExpireAtUnixMilli = time.Now().UnixMilli() - 1
		sessionStickies[1]["s1"] = entry
		routeMu.Unlock()

		if item := pickSessionSticky(group, "s1"); item.ID != 0 {
			t.Fatalf("过期后仍返回成员 %d, 期望零值", item.ID)
		}
		if _, ok := stickyEntryOf(t, 1, "s1"); ok {
			t.Fatal("过期粘合未被清除")
		}
	})

	t.Run("目标冷却中失效", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))
		bindSessionSticky(group, "s1", 12)

		// 目标成员进入冷却(OPEN), 粘合应失效并被清除。
		routeMu.Lock()
		routes[1] = &RouteState{GroupID: 1, Cooldowns: map[int]int64{12: time.Now().UnixMilli() + 60_000}}
		routeMu.Unlock()

		if item := pickSessionSticky(group, "s1"); item.ID != 0 {
			t.Fatalf("冷却中的粘合成员 %d 未失效", item.ID)
		}
		if _, ok := stickyEntryOf(t, 1, "s1"); ok {
			t.Fatal("冷却失效的粘合未被清除")
		}
	})

	t.Run("滑动续期时间正确", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11))
		bindSessionSticky(group, "s1", 11)
		first, _ := stickyEntryOf(t, 1, "s1")

		time.Sleep(20 * time.Millisecond)
		bindSessionSticky(group, "s1", 11)
		renewed, _ := stickyEntryOf(t, 1, "s1")

		if renewed.ExpireAtUnixMilli <= first.ExpireAtUnixMilli {
			t.Fatalf("续期后过期时间 %d 未晚于首次 %d", renewed.ExpireAtUnixMilli, first.ExpireAtUnixMilli)
		}
		delta := renewed.ExpireAtUnixMilli - time.Now().UnixMilli()
		if delta < 299_000 || delta > 300_000 {
			t.Fatalf("续期剩余时长 = %dms, 期望约 300000ms", delta)
		}
	})

	t.Run("clearSessionStickyByItem清除", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12), stickyTestItem(13))
		bindSessionSticky(group, "s-a", 12)
		bindSessionSticky(group, "s-b", 12)
		bindSessionSticky(group, "s-c", 13)

		clearSessionStickyByItem(1, 12)

		for _, key := range []string{"s-a", "s-b"} {
			if _, ok := stickyEntryOf(t, 1, key); ok {
				t.Fatalf("指向成员 12 的会话 %s 未被清除", key)
			}
		}
		if _, ok := stickyEntryOf(t, 1, "s-c"); !ok {
			t.Fatal("指向其他成员的会话 s-c 被误清除")
		}
		if item := pickSessionSticky(group, "s-c"); item.ID != 13 {
			t.Fatalf("未受影响会话返回成员 %d, 期望 13", item.ID)
		}
	})

	t.Run("空会话键不绑定", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11))

		bindSessionSticky(group, "", 11)

		routeMu.Lock()
		_, exists := sessionStickies[1]
		routeMu.Unlock()
		if exists {
			t.Fatal("空会话键不应产生粘合记录")
		}
	})

	t.Run("成员删除残留清理", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))
		bindSessionSticky(group, "s1", 12)

		// 成员 12 被删除后, 清理节流间隔到期时的下一次建立粘合顺带清掉指向它的残留。
		// 生产上清理按 60s 节流, 残留在读取路径始终被即时忽略, 这里归零时间戳
		// 覆盖"到期后必清理"的分支。
		group.Items = []model.GroupItem{stickyTestItem(11)}
		stickyLastPrune.Store(0)
		bindSessionSticky(group, "s2", 11)

		if _, ok := stickyEntryOf(t, 1, "s1"); ok {
			t.Fatal("已删除成员的粘合残留未被清理")
		}
	})

	t.Run("单分组条目上限", func(t *testing.T) {
		resetStickyState()
		group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))

		// 达到单分组上限: 每个 session key 都能正常绑定。
		for i := 0; i < maxSessionStickiesPerGroup; i++ {
			key := strconv.Itoa(i)
			bindSessionSticky(group, key, 11)
		}
		routeMu.Lock()
		got := len(sessionStickies[1])
		routeMu.Unlock()
		if got != maxSessionStickiesPerGroup {
			t.Fatalf("cap 未生效: 条目数 = %d, 期望 %d", got, maxSessionStickiesPerGroup)
		}

		// 满后新增会话不建立粘合, 已有会话续期不受影响。
		bindSessionSticky(group, "overflow-session", 11)
		if _, ok := stickyEntryOf(t, 1, "overflow-session"); ok {
			t.Fatal("满后新会话不应建立粘合")
		}
		existingKey := "0"
		if _, ok := stickyEntryOf(t, 1, existingKey); !ok {
			t.Fatal("已有会话粘合不应被满拒绝")
		}
	})
}

func TestSessionScopeKeyIsolatesAPIKeyAndConsole(t *testing.T) {
	if sessionScopeKey(1, "room") == sessionScopeKey(2, "room") {
		t.Fatal("不同 API Key 的同名会话不得共用隔离键")
	}
	if got, want := sessionScopeKey(7, "room"), "7:room"; got != want {
		t.Fatalf("apiKeyID>0 的键 = %q, 期望 %q", got, want)
	}
	console := sessionScopeKey(0, "room")
	if console != "console:room" {
		t.Fatalf("控制台键 = %q, 期望 console:room", console)
	}
	if console == sessionScopeKey(7, "room") || console == "room" {
		t.Fatal("控制台前缀不得退回裸会话键, 也不得撞上数字前缀")
	}
	// 管理员会话名恰好等于别的 Key 的隔离键时, 仍不得共用脱敏/粘合表。
	if sessionScopeKey(0, "7:room") == sessionScopeKey(7, "room") {
		t.Fatal("控制台同名会话不得与 API Key 隔离键重合")
	}
	if sessionScopeKey(3, "") != "" {
		t.Fatal("空会话不得加前缀")
	}
	longKey := strings.Repeat("k", maxSessionKeyBytes+1)
	if sessionScopeKey(3, longKey) != "" {
		t.Fatal("超长会话键应视为无会话, 而不是截断后入表")
	}
}

func TestSessionStickyScopedKeysDoNotShareMember(t *testing.T) {
	resetStickyState()
	group := stickyTestGroup(300, stickyTestItem(11), stickyTestItem(12))
	bindSessionSticky(group, sessionScopeKey(1, "room"), 11)
	bindSessionSticky(group, sessionScopeKey(2, "room"), 12)
	if item := pickSessionSticky(group, sessionScopeKey(1, "room")); item.ID != 11 {
		t.Fatalf("key 1 粘合成员 = %d, 期望 11", item.ID)
	}
	if item := pickSessionSticky(group, sessionScopeKey(2, "room")); item.ID != 12 {
		t.Fatalf("key 2 粘合成员 = %d, 期望 12", item.ID)
	}
	if item := pickSessionSticky(group, "room"); item.ID != 0 {
		t.Fatal("裸会话键不得命中已隔离的粘合")
	}
}

func TestSessionStickyRejectsOverlongKeyAndTotalCap(t *testing.T) {
	resetStickyState()
	group := stickyTestGroup(300, stickyTestItem(11))
	bindSessionSticky(group, strings.Repeat("s", maxStickyKeyBytes+1), 11)
	routeMu.Lock()
	_, exists := sessionStickies[1]
	routeMu.Unlock()
	if exists {
		t.Fatal("超长粘合键不应入表")
	}

	prev := maxSessionStickiesTotal
	maxSessionStickiesTotal = 2
	t.Cleanup(func() { maxSessionStickiesTotal = prev })
	resetStickyState()
	other := stickyTestGroup(300, stickyTestItem(11))
	other.ID = 2
	bindSessionSticky(group, "a", 11)
	bindSessionSticky(other, "b", 11)
	bindSessionSticky(group, "c", 11)
	if _, ok := stickyEntryOf(t, 1, "c"); ok {
		t.Fatal("跨组条数已满时新会话不应建立粘合")
	}
	bindSessionSticky(group, "a", 11)
	if _, ok := stickyEntryOf(t, 1, "a"); !ok {
		t.Fatal("已有会话续期不应受跨组上限影响")
	}
}
