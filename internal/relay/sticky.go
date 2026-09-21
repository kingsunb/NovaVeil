package relay

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// stickyEntry 是一个会话的粘合记录, 在滑动续期到期前固定使用同一成员。
type stickyEntry struct {
	ItemID            int   // 粘合指向的成员 ID。
	ExpireAtUnixMilli int64 // 过期 Unix 毫秒时间, 到期即视为无粘合。
}

// sessionStickies 按分组 ID 保存各会话的粘合记录, 与分组路由状态共用 routeMu 保护。
var sessionStickies = make(map[int]map[string]stickyEntry)

// maxSessionStickiesPerGroup 单个分组最多保留的会话粘合条目数。会话粘合默认开启,
// 客户端可任意指定 X-Session-Id; 不设上限等于允许无限增长进程内存(审计 REL-01)。
// 选 4096 足够覆盖大规模租户, 又给每个分组的内存量设了有界上限; 满时新会话
// 不再建立粘合, 已有会话的粘合/续期不受影响。
const maxSessionStickiesPerGroup = 4096

// maxSessionStickiesTotal 全部组加起来的粘合条数上限。单组 4096 挡不住很多组一起灌。
// 满时先清过期, 仍满则新会话不建立粘合; 已有会话续期不受影响。测试可临时调低。
var maxSessionStickiesTotal = 16384

// maxSessionKeyBytes 客户端会话标识的字节上限。超限视为没有会话, 不进入粘合、
// 上游会话号缓存或脱敏表。不截断, 避免不同长键被切成同一个键。
const maxSessionKeyBytes = 256

// maxStickyKeyBytes 进入粘合表或会话号缓存的键(含隔离前缀)的字节上限。
// 合法隔离键是数字或 console 前缀加上不超过 maxSessionKeyBytes 的原文, 远小于此值。
const maxStickyKeyBytes = 512

// consoleSessionPrefix 是 api_key_id<=0(控制台 chat)的稳定前缀。
// 它不以数字开头, 因此不会和 `apiKeyID:sessionKey` 撞车; 管理员同名会话
// 也不会再以裸会话键写进脱敏表。
const consoleSessionPrefix = "console:"

// sessionScopeKey 把客户端会话标识收成按 API Key 隔离的内部键。
// apiKeyID>0 时与脱敏键相同, 是 `apiKeyID:sessionKey`, 不把脱敏键剥回原文。
// apiKeyID<=0 时加 console 前缀。空键与超长键返回空串, 调用方按无会话处理。
// 键只来自调用方已经读到的 X-Session-Id / x-opencode-session, 不收首条消息、
// conversation_id 或其他头。
func sessionScopeKey(apiKeyID int, sessionKey string) string {
	if sessionKey == "" || len(sessionKey) > maxSessionKeyBytes {
		return ""
	}
	if apiKeyID > 0 {
		return strconv.Itoa(apiKeyID) + ":" + sessionKey
	}
	return consoleSessionPrefix + sessionKey
}

// sessionStickyEnabled 返回指定分组在当前请求下是否启用会话粘合:
// 仅故障转移模式、分组配置开启且请求携带会话标识时生效; 引用链解析的每一层独立按自身配置判断。
func sessionStickyEnabled(group model.Group, sessionKey string) bool {
	return group.Mode == model.GroupModeFailover && group.RelayConfig.SessionStickyEnabled && sessionKey != ""
}

// pickSessionSticky 返回会话粘合指向的成员: 未过期、成员仍在分组且不在冷却中才有效,
// 无效时清除该记录并返回零值, 由调用方走正常选路重扫。
func pickSessionSticky(group model.Group, sessionKey string) model.GroupItem {
	routeMu.Lock()
	defer routeMu.Unlock()

	return pickSessionStickyLocked(group, sessionKey)
}

// pickSessionStickyLocked 校验并返回会话粘合成员; 调用方必须持有锁。
func pickSessionStickyLocked(group model.Group, sessionKey string) model.GroupItem {
	sessions := sessionStickies[group.ID]
	entry, ok := sessions[sessionKey]
	if !ok {
		return model.GroupItem{}
	}

	now := time.Now().UnixMilli()
	item := itemOf(group, entry.ItemID)
	valid := entry.ExpireAtUnixMilli > now && item.ID != 0
	if valid {
		// 目标成员正在冷却说明它刚进入 OPEN, 粘合随之失效。
		if route := routes[group.ID]; route != nil {
			if deadline, cooling := route.Cooldowns[item.ID]; cooling && deadline > now {
				valid = false
			}
		}
	}
	if !valid {
		delete(sessions, sessionKey)
		if len(sessions) == 0 {
			delete(sessionStickies, group.ID)
		}
		return model.GroupItem{}
	}
	return item
}

// stickyLastPrune 记录上次全量清理的 Unix 毫秒, 节流 bind 路径的顺带清理:
// 每次成功请求都会绑定/续期粘合, 逐次全量扫描会在会话数大时把 O(会话数) 的扫描
// 压进 routeMu 临界区; 读取路径本就会忽略过期/已删成员的粘合, 全量清理按间隔执行即可。
var stickyLastPrune atomic.Int64

// stickyPruneInterval 两次顺带全量清理之间的最小间隔。
const stickyPruneInterval = int64(60 * time.Second / time.Millisecond)

// bindSessionSticky 建立或滑动续期会话粘合, expireAt 取当前时间加配置的粘合时长。
func bindSessionSticky(group model.Group, sessionKey string, itemID int) {
	if sessionKey == "" || itemID == 0 || group.RelayConfig.SessionStickySeconds < 1 || len(sessionKey) > maxStickyKeyBytes {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	// 顺带清理已删除成员和已过期的粘合残留, 思路与 groupRouteLocked 一致;
	// 按间隔节流, 过期条目在读取路径(pickSessionStickyLocked)本就会被即时剔除。
	now := time.Now().UnixMilli()
	last := stickyLastPrune.Load()
	if now-last >= stickyPruneInterval && stickyLastPrune.CompareAndSwap(last, now) {
		pruneSessionStickyLocked(group)
	}
	sessions := sessionStickies[group.ID]
	if _, exists := sessions[sessionKey]; !exists && stickyFullLocked(sessions) {
		// 单组或跨组条目满: 先清过期/已删成员残留, 清理后仍满则拒绝新增粘合。
		// 已有会话的续期不受影响; 新会话退化为按优先级正常选路, 不额外消耗内存。
		pruneSessionStickyLocked(group)
		pruneAllSessionStickiesLocked(now)
		sessions = sessionStickies[group.ID]
		if _, exists := sessions[sessionKey]; !exists && stickyFullLocked(sessions) {
			return
		}
	}
	if sessions == nil {
		sessions = make(map[string]stickyEntry)
		sessionStickies[group.ID] = sessions
	}
	sessions[sessionKey] = stickyEntry{
		ItemID:            itemID,
		ExpireAtUnixMilli: now + int64(group.RelayConfig.SessionStickySeconds)*1000,
	}
}

// clearSessionStickyByItem 清除指向指定成员的全部会话粘合, 在该成员达到失败阈值进入冷却时调用。
func clearSessionStickyByItem(groupID, itemID int) {
	routeMu.Lock()
	defer routeMu.Unlock()

	sessions := sessionStickies[groupID]
	for key, entry := range sessions {
		if entry.ItemID == itemID {
			delete(sessions, key)
		}
	}
	if len(sessions) == 0 {
		delete(sessionStickies, groupID)
	}
}

// clearSessionStickyByGroup 清除指定分组的全部会话粘合记录, 在分组被删除时调用,
// 防止已删除分组的会话表随组号复用前的空窗期永久滞留内存。
func clearSessionStickyByGroup(groupID int) {
	routeMu.Lock()
	defer routeMu.Unlock()

	delete(sessionStickies, groupID)
}

// PruneExpiredSessionStickies 扫描全部分组的过期粘合条目。
func PruneExpiredSessionStickies() int {
	routeMu.Lock()
	defer routeMu.Unlock()
	now := time.Now().UnixMilli()
	removed := 0
	for groupID, sessions := range sessionStickies {
		for key, entry := range sessions {
			if entry.ExpireAtUnixMilli <= now {
				delete(sessions, key)
				removed++
			}
		}
		if len(sessions) == 0 {
			delete(sessionStickies, groupID)
		}
	}
	return removed
}

// stickyFullLocked 报告新会话是否会撑破单组或跨组上限。调用方必须持有锁。
// sessions 为 nil 时单组计数视为 0。
func stickyFullLocked(sessions map[string]stickyEntry) bool {
	if sessions != nil && len(sessions) >= maxSessionStickiesPerGroup {
		return true
	}
	return sessionStickyTotalLocked() >= maxSessionStickiesTotal
}

// sessionStickyTotalLocked 返回全部分组的粘合条数。调用方必须持有锁。
func sessionStickyTotalLocked() int {
	total := 0
	for _, sessions := range sessionStickies {
		total += len(sessions)
	}
	return total
}

// pruneAllSessionStickiesLocked 删除全部分组里已过期的粘合。调用方必须持有锁。
func pruneAllSessionStickiesLocked(now int64) {
	for groupID, sessions := range sessionStickies {
		for key, entry := range sessions {
			if entry.ExpireAtUnixMilli <= now {
				delete(sessions, key)
			}
		}
		if len(sessions) == 0 {
			delete(sessionStickies, groupID)
		}
	}
}

// pruneSessionStickyLocked 清理分组内已删除成员和已过期的粘合残留; 调用方必须持有锁。
func pruneSessionStickyLocked(group model.Group) {
	sessions := sessionStickies[group.ID]
	if sessions == nil {
		return
	}
	items := make(map[int]bool, len(group.Items))
	for _, item := range group.Items {
		items[item.ID] = true
	}
	now := time.Now().UnixMilli()
	for key, entry := range sessions {
		if entry.ExpireAtUnixMilli <= now || !items[entry.ItemID] {
			delete(sessions, key)
		}
	}
	if len(sessions) == 0 {
		delete(sessionStickies, group.ID)
	}
}
