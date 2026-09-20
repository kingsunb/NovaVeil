package op

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestAPIKeyCreateRejectsShortCustomKey(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{Name: "short-create-key", APIKey: "short-key-123", Enabled: true}
	if err := APIKeyCreate(&key, ctx); !errors.Is(err, ErrAPIKeyValidation) {
		t.Fatalf("APIKeyCreate short key error = %v, want ErrAPIKeyValidation", err)
	}
}

func TestAPIKeyUpdateRejectsShortCustomKey(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{Name: "short-update-key", APIKey: "sk-test-short-update-key-0001", Enabled: true}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	update := model.APIKey{ID: key.ID, Name: key.Name, APIKey: "short-key-123", Enabled: true}
	if err := APIKeyUpdate(&update, ctx); !errors.Is(err, ErrAPIKeyValidation) {
		t.Fatalf("APIKeyUpdate short key error = %v, want ErrAPIKeyValidation", err)
	}
}
func TestAPIKeyUpdateKeepsSecretWhenOmitted(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{
		Name:    t.Name(),
		APIKey:  "sk-test-apikey-preserve-secret",
		Enabled: true,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	update := model.APIKey{ID: key.ID, Name: "renamed", Enabled: true}
	if err := APIKeyUpdate(&update, ctx); err != nil {
		t.Fatalf("APIKeyUpdate: %v", err)
	}
	if update.APIKey != key.APIKey {
		t.Fatalf("APIKeyUpdate secret = %q, want preserved secret", update.APIKey)
	}
	if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err != nil {
		t.Fatalf("preserved API key no longer resolves: %v", err)
	}
}

func TestAPIKeyDeleteClearsCaches(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{
		Name:    t.Name(),
		APIKey:  "sk-test-apikey-delete-cache-map",
		Enabled: true,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	if err := APIKeyDelete(key.ID, ctx); err != nil {
		t.Fatalf("APIKeyDelete: %v", err)
	}
	if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err == nil {
		t.Fatal("deleted API key should not be found by its old value")
	}
	s := loadAPIKeySnap()
	if _, ok := s.byID[key.ID]; ok {
		t.Fatal("deleted API key ID should be removed from snapshot byID")
	}
	if _, ok := s.byKey[key.APIKey]; ok {
		t.Fatal("deleted API key value should be removed from snapshot byKey")
	}
}

// TestAPIKeyRefreshHoldsLockAcrossFindAndPublish 是 STA-05 的确定性回归测试。
//
// 旧代码 refresh 的 DB Find 在锁外执行, 与 delete 之间存在「Find 读到含已撤销 Key
// 的旧列表 → delete 清缓存 → refresh 用旧列表覆盖缓存」的 interleaving, 复活已撤销 Key。
// 修复后 Find 与发布同在 apiKeyCacheMu 内。本测试通过 apiKeyRefreshTestHook 在
// refresh 的「Find 完成」与「发布」之间注入 barrier, 并在该窗口内启动 delete:
// 修复代码下 refresh 仍持锁, delete 必然阻塞到 refresh 发布之后才执行, 故最终
// Key 不会复活; 旧代码下 delete 会在窗口内完成并随后被 refresh 的旧列表覆盖, 复活。
func TestAPIKeyRefreshHoldsLockAcrossFindAndPublish(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{
		Name:    t.Name(),
		APIKey:  "sk-sta05-barrier-revive",
		Enabled: true,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	afterFind := make(chan struct{})
	allowPublish := make(chan struct{})
	apiKeyRefreshTestHook = func(stage string) {
		if stage != "after-find" {
			return
		}
		close(afterFind)
		<-allowPublish // 阻塞 refresh 发布, 模拟 Find 与发布之间的窗口
	}
	t.Cleanup(func() { apiKeyRefreshTestHook = nil })

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- apiKeyRefreshCache(ctx) }()

	<-afterFind // refresh 已 Find 并持锁, 尚未发布

	// 在 refresh 持锁窗口内启动 delete。修复代码下 delete 拿不到锁, 不应完成;
	// 旧代码下 Find 在锁外、refresh 此刻未持锁, delete 会立即完成并随后被覆盖。
	delResult := make(chan error, 1)
	go func() { delResult <- APIKeyDelete(key.ID, ctx) }()

	select {
	case e := <-delResult:
		// delete 在 refresh 发布前完成 ⇒ refresh 未持锁 ⇒ STA-05 修复被破坏。
		close(allowPublish)
		if err := <-refreshDone; err != nil {
			t.Fatalf("refresh: %v", err)
		}
		t.Fatalf("STA-05 regression: delete completed while refresh held lock (revival window): %v", e)
	case <-time.After(100 * time.Millisecond):
		// 预期: delete 阻塞在锁上, 窗口内未完成。
	}

	close(allowPublish) // 放行 refresh 发布
	if err := <-refreshDone; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := <-delResult; err != nil {
		t.Fatalf("APIKeyDelete: %v", err)
	}

	// 不变量: 删除返回后, 旧 Key 不得在后续鉴权中复活。
	if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err == nil {
		t.Fatal("STA-05 regression: deleted key revived after refresh+delete interleaving")
	}
	if _, err := APIKeyGet(key.ID, ctx); err == nil {
		t.Fatal("STA-05 regression: deleted key ID revived after interleaving")
	}
	s := loadAPIKeySnap()
	if _, ok := s.byKey[key.APIKey]; ok {
		t.Fatal("STA-05 regression: deleted key present in snapshot byKey")
	}
}

// TestAPIKeyRefreshDoesNotReviveDeletedKey 在持续并发刷新的压力下反复删除并断言
// 已撤销 Key 永不复活, 覆盖单实例 InitCache 触发 refresh 与 delete 竞态的场景。
func TestAPIKeyRefreshDoesNotReviveDeletedKey(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{
		Name:    t.Name(),
		APIKey:  "sk-sta05-concurrent-revive",
		Enabled: true,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	var refreshMu sync.Mutex
	var refreshErr error
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := apiKeyRefreshCache(ctx); err != nil {
				refreshMu.Lock()
				refreshErr = err
				refreshMu.Unlock()
				return
			}
		}
	}()

	if err := APIKeyDelete(key.ID, ctx); err != nil {
		close(stop)
		wg.Wait()
		t.Fatalf("APIKeyDelete: %v", err)
	}
	close(stop)
	wg.Wait()

	refreshMu.Lock()
	e := refreshErr
	refreshMu.Unlock()
	if e != nil {
		t.Fatalf("refresh error: %v", e)
	}

	// 删除返回后, 任何后续鉴权/查询都不得再看到该 Key。
	for i := 0; i < 200; i++ {
		if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err == nil {
			t.Fatalf("STA-05 regression: deleted key revived after %d queries", i)
		}
		if _, err := APIKeyGet(key.ID, ctx); err == nil {
			t.Fatalf("STA-05 regression: deleted key ID revived after %d queries", i)
		}
	}
	// 再刷一次, 确认 DB 视图与缓存一致, 仍未复活。
	if err := apiKeyRefreshCache(ctx); err != nil {
		t.Fatalf("final refresh: %v", err)
	}
	if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err == nil {
		t.Fatal("STA-05 regression: deleted key revived after final refresh")
	}
}

// TestAPIKeyDeleteThenRecreateSameValue 覆盖「删除再建同值」路径: 旧 ID 永不复活,
// 同值重建后鉴权命中新对象, 刷新后仍一致。
func TestAPIKeyDeleteThenRecreateSameValue(t *testing.T) {
	ctx := context.Background()
	const token = "sk-sta05-recreate-same-value"

	k1 := model.APIKey{Name: t.Name() + "-v1", APIKey: token, Enabled: true}
	if err := APIKeyCreate(&k1, ctx); err != nil {
		t.Fatalf("APIKeyCreate v1: %v", err)
	}
	id1 := k1.ID

	if err := APIKeyDelete(id1, ctx); err != nil {
		t.Fatalf("APIKeyDelete v1: %v", err)
	}
	if _, err := APIKeyGet(id1, ctx); err == nil {
		t.Fatal("old id should not resolve after delete")
	}
	if _, err := APIKeyGetByAPIKey(token, ctx); err == nil {
		t.Fatal("old token should not resolve after delete and before recreate")
	}

	k2 := model.APIKey{Name: t.Name() + "-v2", APIKey: token, Enabled: true}
	if err := APIKeyCreate(&k2, ctx); err != nil {
		t.Fatalf("APIKeyCreate v2: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(k2.ID, ctx) })
	if k2.ID == id1 {
		t.Fatal("recreate should yield a new id")
	}

	got, err := APIKeyGetByAPIKey(token, ctx)
	if err != nil {
		t.Fatalf("recreated token not found: %v", err)
	}
	if got.ID != k2.ID {
		t.Fatalf("resolved id = %d, want %d", got.ID, k2.ID)
	}
	if _, err := APIKeyGet(id1, ctx); err == nil {
		t.Fatal("old id revived after recreate")
	}

	if err := apiKeyRefreshCache(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got2, err := APIKeyGetByAPIKey(token, ctx)
	if err != nil {
		t.Fatalf("token not found after refresh: %v", err)
	}
	if got2.ID != k2.ID {
		t.Fatalf("after refresh resolved id = %d, want %d", got2.ID, k2.ID)
	}
	if _, err := APIKeyGet(id1, ctx); err == nil {
		t.Fatal("old id revived after refresh")
	}
}

// TestAPIKeyConcurrentUpdateRefreshAuth 在 -race 下并发执行「更新 token / 刷新 /
// 鉴权读取」, 验证 COW 快照无数据竞争, 且收尾后缓存与 DB 一致、鉴权命中本 ID。
func TestAPIKeyConcurrentUpdateRefreshAuth(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{
		Name:    t.Name(),
		APIKey:  "sk-sta05-concurrent-base",
		Enabled: true,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	tokens := []string{
		"sk-sta05-concurrent-a",
		"sk-sta05-concurrent-b",
		"sk-sta05-concurrent-c",
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 4)

	// 持续刷新(模拟 InitCache / 周期性 refresh 与写入并发)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := apiKeyRefreshCache(ctx); err != nil {
				errs <- err
				return
			}
		}
	}()

	// 持续在三个 token 值间轮换更新(写入路径)。
	var tk atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			i := int(tk.Add(1)) % len(tokens)
			upd := model.APIKey{ID: key.ID, Name: t.Name(), APIKey: tokens[i], Enabled: true}
			if err := APIKeyUpdate(&upd, ctx); err != nil && !errors.Is(err, ErrAPIKeyValueExists) {
				errs <- err
				return
			}
		}
	}()

	// 持续鉴权读取(读取路径, -race 下验证不触碰写入侧 map)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, tkn := range tokens {
				_, _ = APIKeyGetByAPIKey(tkn, ctx)
			}
			_, _ = APIKeyGet(key.ID, ctx)
			_, _ = APIKeyList(ctx)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent op error: %v", e)
	}

	// 收尾: 刷新使缓存与 DB 一致, 当前 token 必须鉴权命中本 ID。
	if err := apiKeyRefreshCache(ctx); err != nil {
		t.Fatalf("final refresh: %v", err)
	}
	s := loadAPIKeySnap()
	obj, ok := s.byID[key.ID]
	if !ok {
		t.Fatal("key missing from snapshot after concurrent ops")
	}
	got, err := APIKeyGetByAPIKey(obj.APIKey, ctx)
	if err != nil {
		t.Fatalf("current token %q not found: %v", obj.APIKey, err)
	}
	if got.ID != key.ID {
		t.Fatalf("resolved id = %d, want %d", got.ID, key.ID)
	}
}
