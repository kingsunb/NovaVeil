package relay

import (
	"testing"
	"time"
)

func clearWarpBlockForTest(id int) {
	warpChannelBlockMu.Lock()
	delete(warpChannelBlockUntil, id)
	warpChannelBlockMu.Unlock()
}

func TestWarpChannelBlockUntilUTC(t *testing.T) {
	const id = 424242
	clearWarpBlockForTest(id)
	defer clearWarpBlockForTest(id)

	if warpChannelBlocked(id) {
		t.Fatal("未登记时不应停用")
	}
	blockWarpChannelUntilUTC(id)
	if !warpChannelBlocked(id) {
		t.Fatal("登记后应停用")
	}
}

func TestWarpChannelBlockExpires(t *testing.T) {
	const id = 424243
	warpChannelBlockMu.Lock()
	warpChannelBlockUntil[id] = time.Now().Add(-time.Second)
	warpChannelBlockMu.Unlock()
	defer clearWarpBlockForTest(id)

	if warpChannelBlocked(id) {
		t.Fatal("过期后不应停用")
	}
	warpChannelBlockMu.Lock()
	_, ok := warpChannelBlockUntil[id]
	warpChannelBlockMu.Unlock()
	if ok {
		t.Fatal("过期项应被清理")
	}
}

func TestWarpChannelBlockIgnoresInvalidID(t *testing.T) {
	blockWarpChannelUntilUTC(0)
	if warpChannelBlocked(0) {
		t.Fatal("无效渠道号不应停用")
	}
}
