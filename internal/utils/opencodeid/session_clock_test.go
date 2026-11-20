package opencodeid

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSessionClockCapacityAndExpiry(t *testing.T) {
	store := newSessionClockStore()
	const started = uint64(10000)
	oldest := sha256.Sum256([]byte("0"))
	for i := 0; i <= sessionClockLimit; i++ {
		key := sha256.Sum256([]byte(strconv.Itoa(i)))
		store.millisLocked(key, started)
	}
	if len(store.entries) != sessionClockLimit || store.order.Len() != sessionClockLimit {
		t.Fatalf("缓存超出上限: entries=%d order=%d", len(store.entries), store.order.Len())
	}
	if _, exists := store.entries[oldest]; exists {
		t.Fatal("超出容量时应淘汰最早条目")
	}
	fresh := sha256.Sum256([]byte("fresh"))
	store.millisLocked(fresh, started+sessionClockMaxAgeMillis)
	if len(store.entries) != 1 || store.order.Len() != 1 {
		t.Fatalf("过期条目应被移除: entries=%d order=%d", len(store.entries), store.order.Len())
	}
}

func TestSessionClockRollbackRefreshesInvalidEntries(t *testing.T) {
	first := sha256.Sum256([]byte("first"))
	second := sha256.Sum256([]byte("second"))
	cases := []struct {
		name      string
		firstTime uint64
		created   uint64
		now       uint64
	}{
		{name: "队首有效但命中时间在未来", firstTime: 1000000, created: 2000000, now: 1500000},
		{name: "回拨前条目不能挡住过期清理", firstTime: 10000000, created: 1000000, now: 1000000 + sessionClockMaxAgeMillis},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := newSessionClockStore()
			store.millisLocked(first, test.firstTime)
			store.millisLocked(second, test.created)
			if got := store.millisLocked(second, test.now); got != test.now {
				t.Fatalf("失效时间未刷新: got=%d want=%d", got, test.now)
			}
			if got := store.millisLocked(second, test.now+1); got != test.now {
				t.Fatalf("刷新后的时间不稳定: got=%d want=%d", got, test.now)
			}
			if len(store.entries) != store.order.Len() {
				t.Fatalf("缓存索引不一致: entries=%d order=%d", len(store.entries), store.order.Len())
			}
		})
	}
}

func TestSessionClockConcurrentInitializationAndRefresh(t *testing.T) {
	key := sha256.Sum256([]byte("shared-session"))
	const workers = 64
	for _, expired := range []bool{false, true} {
		store := newSessionClockStore()
		started := uint64(time.Now().UnixMilli())
		if expired {
			store.millisLocked(key, started-sessionClockMaxAgeMillis)
		}
		gate := make(chan struct{})
		results := make(chan uint64, workers)
		var group sync.WaitGroup
		for i := 0; i < workers; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-gate
				results <- store.millis(key)
			}()
		}
		close(gate)
		group.Wait()
		close(results)
		var first uint64
		for result := range results {
			if first == 0 {
				first = result
			}
			if result != first {
				t.Fatalf("同一会话返回不同时间: %d != %d", result, first)
			}
			if result < started {
				t.Fatalf("缓存未刷新: %d", result)
			}
		}
	}
}
