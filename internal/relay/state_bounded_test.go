package relay

import (
	"fmt"
	"testing"
	"time"
)

func TestClientIPSetEvictsOldestHalfWhenFull(t *testing.T) {
	clientIPsMu.Lock()
	oldIPs := clientIPSet
	clientIPSet = make(map[string]time.Time)
	clientIPsMu.Unlock()
	t.Cleanup(func() {
		clientIPsMu.Lock()
		clientIPSet = oldIPs
		clientIPsMu.Unlock()
	})

	base := time.Now()
	for i := 0; i < maxClientIPs; i++ {
		ip := fmt.Sprintf("ip-%04d", i)
		clientIPSet[ip] = base.Add(time.Duration(i) * time.Millisecond)
	}
	if len(clientIPSet) != maxClientIPs {
		t.Fatalf("seeded len=%d", len(clientIPSet))
	}
	trackClientRequest("fresh-client")
	if len(clientIPSet) != maxClientIPs/2+1 {
		t.Fatalf("after eviction len=%d, want %d", len(clientIPSet), maxClientIPs/2+1)
	}
	if _, ok := clientIPSet["fresh-client"]; !ok {
		t.Fatal("new client IP must survive eviction")
	}
	if _, ok := clientIPSet["ip-0000"]; ok {
		t.Fatal("oldest IP must be evicted")
	}
	if _, ok := clientIPSet["ip-2047"]; ok {
		t.Fatal("oldest half boundary IP must be evicted")
	}
	if _, ok := clientIPSet["ip-4095"]; !ok {
		t.Fatal("newest IP must survive eviction")
	}
}

func TestTrimFinishedRequestsQueueBounded(t *testing.T) {
	mu.Lock()
	oldRequests := requests
	oldQueue := finishedRequestQueue
	requests = make(map[uint64]*RequestState)
	finishedRequestQueue = finishedRequestQueue[:0]
	for i := 0; i < maxFinished+10; i++ {
		id := uint64(i + 1)
		finishedRequestQueue = append(finishedRequestQueue, id)
		requests[id] = &RequestState{ID: id, Status: StatusCanceled}
	}
	trimFinishedRequestsLocked()
	n := len(finishedRequestQueue)
	reqCount := len(requests)
	oldestAlive := uint64(maxFinished + 11)
	for id := range requests {
		if id < oldestAlive {
			oldestAlive = id
		}
	}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		requests = oldRequests
		finishedRequestQueue = oldQueue
		mu.Unlock()
	})

	if n != maxFinished {
		t.Fatalf("queue len=%d, want %d", n, maxFinished)
	}
	if reqCount != maxFinished {
		t.Fatalf("requests len=%d, want %d", reqCount, maxFinished)
	}
	if oldestAlive != 11 {
		t.Fatalf("oldest kept id=%d, want 11", oldestAlive)
	}
}
