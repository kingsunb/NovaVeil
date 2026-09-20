// This implementation is based on and modified from https://github.com/fanjindong/go-cache
package cache

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/cespare/xxhash/v2"
)

// keyToString 把泛型 key 转为哈希输入字符串。整型 key 是渠道/模型/密钥缓存的主键,
// 走 strconv 避免 fmt 反射分配; 其余类型(如命名 string 类型)回退 fmt。
func keyToString[K comparable](key K) string {
	switch k := any(key).(type) {
	case string:
		return k
	case int:
		return strconv.Itoa(k)
	case int64:
		return strconv.FormatInt(k, 10)
	default:
		return fmt.Sprintf("%v", key)
	}
}

type Cache[K comparable, V any] interface {
	Set(k K, v V)
	Get(k K) (V, bool)
	GetAll() map[K]V
	Del(keys ...K) int
	Len() int
	Clear()
	// RefreshAll 原子替换缓存的全部内容: 先在后台构造完整新一代, 再一次
	// 原子发布; 读者在替换前后始终看到某一代的完整视图, 不会看到全空或
	// 半新的混合代。
	RefreshAll(target map[K]V)
}

func New[K comparable, V any](shards int) Cache[K, V] {
	if shards <= 0 {
		shards = 1024
	}
	// 分片路由按 hash&(shards-1) 位与计算, 非二次幂时高位不参与路由,
	// 部分分片永远不会被命中(如 shards=3 只用 0/2)。向上取整到最近的
	// 二次幂, 保证任意入参分布均匀。
	for shards&(shards-1) != 0 {
		shards = (shards | (shards - 1)) + 1
	}

	c := &cache[K, V]{
		shardMask: uint64(shards - 1),
		refreshMu: sync.RWMutex{},
	}
	c.gen.Store(newGeneration[K, V](shards))

	return c
}

type cache[K comparable, V any] struct {
	gen       atomic.Pointer[generation[K, V]]
	shardMask uint64
	// refreshMu 串行化 RefreshAll/Clear 的代际替换；Set/Del 持读锁、
	// RefreshAll/Clear 持写锁，避免“写入落到刚被替换的退休旧代”而悄然丢失。
	refreshMu sync.RWMutex
}

// generation 是一代完整的 shard 集合, 发布后只读; RefreshAll/Clear 构造新一代
// 再原子替换, 从而消除逐 shard 清空+重建造成的空窗口/混合代(审计 RELI-06/R-M1)。
type generation[K comparable, V any] struct {
	shards []*shard[K, V]
}

func newGeneration[K comparable, V any](shards int) *generation[K, V] {
	g := &generation[K, V]{shards: make([]*shard[K, V], shards)}
	for i := 0; i < shards; i++ {
		g.shards[i] = &shard[K, V]{hashmap: map[K]V{}}
	}
	return g
}

func (c *cache[K, V]) current() *generation[K, V] {
	if g := c.gen.Load(); g != nil {
		return g
	}
	// Init 前理论不可达; 兜底一个空代避免 nil panic。
	return newGeneration[K, V](int(c.shardMask) + 1)
}

func (c *cache[K, V]) Set(k K, v V) {
	hashedKey := xxhash.Sum64String(keyToString(k))
	c.refreshMu.RLock()
	defer c.refreshMu.RUnlock()
	c.getShard(c.current(), hashedKey).set(k, v)
}

func (c *cache[K, V]) Get(k K) (V, bool) {
	hashedKey := xxhash.Sum64String(keyToString(k))
	return c.getShard(c.current(), hashedKey).get(k)
}

func (c *cache[K, V]) GetAll() map[K]V {
	g := c.current()
	result := make(map[K]V)
	for _, shard := range g.shards {
		shardData := shard.snapshot()
		for k, v := range shardData {
			result[k] = v
		}
	}
	return result
}

func (c *cache[K, V]) Del(ks ...K) int {
	c.refreshMu.RLock()
	defer c.refreshMu.RUnlock()
	g := c.current()
	var count int
	for _, k := range ks {
		hashedKey := xxhash.Sum64String(keyToString(k))
		count += c.getShard(g, hashedKey).del(k)
	}
	return count
}

func (c *cache[K, V]) Len() int {
	g := c.current()
	var count int
	for _, shard := range g.shards {
		count += shard.size()
	}
	return count
}

func (c *cache[K, V]) getShard(g *generation[K, V], hashedKey uint64) *shard[K, V] {
	return g.shards[hashedKey&c.shardMask]
}

// Clear 用新一代空缓存原子替换旧代, 不在旧 shard 上原地清空, 读者不会观察
// 到"刚清空但新代尚未发布"的全空窗口。
func (c *cache[K, V]) Clear() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.gen.Store(newGeneration[K, V](int(c.shardMask) + 1))
}

// RefreshAll 先构造完整新一代再原子发布。构造在后台进行, 不阻塞读者;
// 发布后新 Set/Get 全部落到新一代, 旧代由 GC 回收。
func (c *cache[K, V]) RefreshAll(target map[K]V) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	g := newGeneration[K, V](int(c.shardMask) + 1)
	for k, v := range target {
		hashedKey := xxhash.Sum64String(keyToString(k))
		g.shards[hashedKey&c.shardMask].set(k, v)
	}
	c.gen.Store(g)
}
