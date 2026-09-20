package cache

import (
	"sync"
	"testing"
)

func TestRefreshAllAtomicVisibleGeneration(t *testing.T) {
	c := New[int, string](3)
	c.Set(1, "old")
	c.RefreshAll(map[int]string{1: "new", 2: "two"})

	if v, ok := c.Get(1); !ok || v != "new" {
		t.Fatalf("Get(1) = %q,%v want new,true", v, ok)
	}
	if v, ok := c.Get(2); !ok || v != "two" {
		t.Fatalf("Get(2) = %q,%v want two,true", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	all := c.GetAll()
	if len(all) != 2 || all[1] != "new" || all[2] != "two" {
		t.Fatalf("GetAll = %#v", all)
	}
}

func TestClearPublishesEmptyGeneration(t *testing.T) {
	c := New[int, string](4)
	c.RefreshAll(map[int]string{1: "a", 2: "b"})
	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", c.Len())
	}
	if _, ok := c.Get(1); ok {
		t.Fatal("Get(1) after Clear must be missing")
	}
}

// TestRefreshAllConcurrentReaders 是并发读取的烟雾测试: 刷新前后都只能读到
// 完整代数, 不能 panic 或读到空窗口(RELI-06/R-M1)。
func TestRefreshAllConcurrentReaders(t *testing.T) {
	c := New[int, int](8)
	const before, after = 64, 128
	for i := 0; i < before; i++ {
		c.Set(i, i)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if got, ok := c.Get(0); ok && got != 0 {
						t.Error("unexpected value for key 0 before refresh")
						return
					}
					_ = c.Len()
				}
			}
		}()
	}
	target := make(map[int]int, after)
	for i := 0; i < after; i++ {
		target[i] = i
	}
	c.RefreshAll(target)
	close(stop)
	wg.Wait()

	if c.Len() != after {
		t.Fatalf("Len after RefreshAll = %d, want %d", c.Len(), after)
	}
	if v, ok := c.Get(127); !ok || v != 127 {
		t.Fatalf("new generation Get(127) = %v,%v", v, ok)
	}
}
