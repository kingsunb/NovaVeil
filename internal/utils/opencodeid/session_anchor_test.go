package opencodeid

import (
	"encoding/hex"
	"regexp"
	"sync"
	"testing"
	"time"
)

// TestSessionIDFromSignalStaysInWindow 锁定派生会话号的 36 位时间段经纪元重建
// (ts + k×2^36 取不晚于当前的最近值)后落在「当前 UTC 日 − 至多 30 天」窗口内,
// 且同一信号恒定派生同一号。48 位字只保留低 36 位毫秒, 解码必须按纪元展开。
func TestSessionIDFromSignalStaysInWindow(t *testing.T) {
	pattern := regexp.MustCompile(`^ses_([0-9a-f]{12})[0-9A-Za-z]{14}$`)
	const era = uint64(1) << 36
	now := uint64(time.Now().UnixMilli())
	currentEra := now / era
	oldest := now - 31*sessionDayMillis - 24*3600*1000
	for _, signal := range []string{"conv-a", "conv-b", "x"} {
		first := SessionIDFromSignal(signal, false)
		if again := SessionIDFromSignal(signal, false); again != first {
			t.Fatalf("同一信号应恒定派生: %s vs %s", first, again)
		}
		m := pattern.FindStringSubmatch(first)
		if m == nil {
			t.Fatalf("派生号形态异常: %q", first)
		}
		raw, err := hex.DecodeString(m[1])
		if err != nil {
			t.Fatalf("解析时间段: %v", err)
		}
		var inverted uint64
		for _, b := range raw {
			inverted = inverted<<8 | uint64(b)
		}
		val36 := (^inverted & 0xFFFFFFFFFFFF) >> 12
		ts := val36 + currentEra*era
		if ts > now {
			ts -= era
		}
		if ts > now+24*3600*1000 || ts < oldest {
			t.Fatalf("纪元重建后时间段越窗: signal=%q ts=%d 窗口=[%d,%d]", signal, ts, oldest, now)
		}
	}
	official := GenerateSessionID()
	if SessionIDFromSignal(official, false) != official {
		t.Fatal("官方号应原样保留")
	}
}

// TestSessionIDFromSignalSaltedPerProcess 锁定进程盐契约: 同进程恒定派生,
// 换盐(模拟重启)后同一信号派生新号, 且形态仍匹配官方 pattern。
func TestSessionIDFromSignalSaltedPerProcess(t *testing.T) {
	pattern := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	first := SessionIDFromSignal("conv-salt", false)
	if again := SessionIDFromSignal("conv-salt", false); again != first {
		t.Fatalf("同进程内应恒定派生: %s vs %s", first, again)
	}
	t.Cleanup(func() {
		// 还原盐状态, 后续用例重铸随机盐。
		processSaltOnce = sync.Once{}
		processSaltBuf = [16]byte{}
	})
	// 模拟重启: 直接改写盐字节, Once 已消耗不再重铸。
	for i := range processSaltBuf {
		processSaltBuf[i] = byte(i + 1)
	}
	rebooted := SessionIDFromSignal("conv-salt", false)
	if rebooted == first {
		t.Fatal("换盐后同一信号应派生新号")
	}
	if !pattern.MatchString(rebooted) {
		t.Fatalf("换盐后派生号形态异常: %q", rebooted)
	}
	if again := SessionIDFromSignal("conv-salt", false); again != rebooted {
		t.Fatalf("换盐后进程内应恒定派生: %s vs %s", rebooted, again)
	}
}
