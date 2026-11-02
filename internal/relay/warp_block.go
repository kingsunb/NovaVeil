package relay

import (
	"sync"
	"time"
)

var (
	warpChannelBlockMu    sync.Mutex
	warpChannelBlockUntil = map[int]time.Time{}
)

// 停用 WARP 渠道至下一个 UTC 零点。
func blockWarpChannelUntilUTC(channelID int) {
	if channelID <= 0 {
		return
	}
	now := time.Now().UTC()
	until := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	warpChannelBlockMu.Lock()
	if current, ok := warpChannelBlockUntil[channelID]; !ok || until.After(current) {
		warpChannelBlockUntil[channelID] = until
	}
	warpChannelBlockMu.Unlock()
}

// 报告 WARP 渠道是否仍在停用期。
func warpChannelBlocked(channelID int) bool {
	if channelID <= 0 {
		return false
	}
	now := time.Now()
	warpChannelBlockMu.Lock()
	defer warpChannelBlockMu.Unlock()
	until, ok := warpChannelBlockUntil[channelID]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(warpChannelBlockUntil, channelID)
		return false
	}
	return true
}
