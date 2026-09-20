package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
)

// apiKey 操作的 sentinel 错误: handler 用 errors.Is 区分 4xx/5xx 与 i18n 文案。
var (
	ErrAPIKeyValueExists = errors.New("API key value already exists")
	ErrAPIKeyNotFound    = errors.New("API key not found")
	// ErrAPIKeyValidation 表示自定义 API Key 未通过最小长度校验, handler 据此返回 400。
	ErrAPIKeyValidation = errors.New("API key validation failed")
)

// apiKeyMinLen 是用户自定义 API Key 的最小字符数(审计 SEC-05/S-L1)。
// 服务端生成的 sk-<app>-... 密钥恒大于该值。
const apiKeyMinLen = 16

func validateAPIKeyCustom(value string) error {
	if utf8.RuneCountInString(value) < apiKeyMinLen {
		return fmt.Errorf("%w: API key 长度至少为 %d 个字符", ErrAPIKeyValidation, apiKeyMinLen)
	}
	return nil
}

// apiKeySnapshot 是 API key 缓存的不可变代际快照: 某一时刻全量密钥的成对视图。
//
// 鉴权读取通过 atomic.Pointer 一次加载获得同一代快照, 再从该快照内成对读取
// byKey(key→id) 与 byID(id→对象), 不会出现跨代两步读取撕裂(旧代码先查 id map
// 再查对象缓存, 中间可被 refresh/delete 换代)。写入路径(创建/更新/删除/刷新)
// 在 apiKeyCacheMu 下克隆出下一代快照后原子发布, 已发布的 map 永不被原地修改,
// 因此读取路径无需加锁、无数据竞争。
//
// 这同时修复 STA-05: refresh 的 DB Find 与发布同在 apiKeyCacheMu 内, 与 delete
// 互斥, 不会再用「Find 时拿到的含已撤销 Key 的旧列表」覆盖 delete 刚清掉的缓存,
// 从而消除「refresh 复活已撤销 Key」的竞态窗口。
type apiKeySnapshot struct {
	byID  map[int]model.APIKey // id → 密钥对象
	byKey map[string]int       // api_key 明文 → id(鉴权索引)
}

var (
	// emptyAPIKeySnap 是 InitCache 发布前(或缓存被清空后)的空视图, 保证鉴权读取
	// 永不会看到 nil 快照。
	emptyAPIKeySnap = &apiKeySnapshot{byID: map[int]model.APIKey{}, byKey: map[string]int{}}
	// apiKeyCacheMu 串行化所有写入路径(create/update/delete/refresh)的「DB 写 +
	// 构造下一代快照 + 发布」全过程; 读取路径不取此锁, 走 atomic.Pointer 无锁加载。
	apiKeyCacheMu sync.Mutex
	apiKeySnap    atomic.Pointer[apiKeySnapshot]
	// apiKeyRefreshTestHook 仅供 STA-05 竞态测试注入 barrier, 精确控制 refresh 的
	// 「Find 完成」与「发布」之间的调度。生产中为 nil, refresh 仅多一次 nil 判定,
	// 零可观测开销。钩子在 apiKeyCacheMu 锁内被调用, 故测试可据此验证 delete 在
	// refresh 持锁期间无法插入。
	apiKeyRefreshTestHook func(stage string)
)

// loadAPIKeySnap 返回当前代快照; 未发布过(InitCache 前)时返回空快照, 避免空指针。
func loadAPIKeySnap() *apiKeySnapshot {
	if s := apiKeySnap.Load(); s != nil {
		return s
	}
	return emptyAPIKeySnap
}

// cloneAPIKeySnap 浅拷贝快照的两个 map, 供写入路径在锁内构造下一代快照。
// model.APIKey 是值类型且写入后不再被原地修改, 无需深拷贝。容量 +1 预留单条
// 新增(创建/更新)时避免再哈希。
func cloneAPIKeySnap(s *apiKeySnapshot) *apiKeySnapshot {
	byID := make(map[int]model.APIKey, len(s.byID)+1)
	for k, v := range s.byID {
		byID[k] = v
	}
	byKey := make(map[string]int, len(s.byKey)+1)
	for k, v := range s.byKey {
		byKey[k] = v
	}
	return &apiKeySnapshot{byID: byID, byKey: byKey}
}

// apiKeyValueTakenSnap 检查自定义 key 值是否已被其他密钥占用, excludeID 为自身 ID(创建时传 0)。
func apiKeyValueTakenSnap(s *apiKeySnapshot, value string, excludeID int) bool {
	if id, ok := s.byKey[value]; ok && id != excludeID {
		return true
	}
	return false
}

func APIKeyCreate(key *model.APIKey, ctx context.Context) error {
	if key == nil {
		return fmt.Errorf("%w: 缺少 API key 数据", ErrAPIKeyValidation)
	}
	key.APIKey = strings.TrimSpace(key.APIKey)
	if err := validateAPIKeyCustom(key.APIKey); err != nil {
		return err
	}

	apiKeyCacheMu.Lock()
	defer apiKeyCacheMu.Unlock()

	cur := loadAPIKeySnap()
	if apiKeyValueTakenSnap(cur, key.APIKey, 0) {
		return ErrAPIKeyValueExists
	}
	sealed, sealErr := seal.Seal(key.APIKey)
	if sealErr != nil {
		return fmt.Errorf("failed to encrypt API key: %w", sealErr)
	}
	dbKey := *key
	dbKey.APIKey = sealed
	if err := db.GetDB().WithContext(ctx).Create(&dbKey).Error; err != nil {
		// apikeys.api_key 的 UNIQUE 索引(迁移 013)兜底两个并发请求都过掉本地
		// 去重的情况: 后写者命中索引, 这里把 driver 错误翻译成 sentinel,
		// 让 handler 统一返 409 Conflict。
		if isUniqueConstraintError(err) {
			return ErrAPIKeyValueExists
		}
		return fmt.Errorf("failed to create API key: %w", err)
	}
	key.ID = dbKey.ID
	key.CreatedAt = dbKey.CreatedAt
	next := cloneAPIKeySnap(cur)
	next.byID[key.ID] = *key
	next.byKey[key.APIKey] = key.ID
	apiKeySnap.Store(next)
	return nil
}

func APIKeyUpdate(key *model.APIKey, ctx context.Context) error {
	apiKeyCacheMu.Lock()
	defer apiKeyCacheMu.Unlock()

	key.APIKey = strings.TrimSpace(key.APIKey)
	if key.APIKey != "" {
		if err := validateAPIKeyCustom(key.APIKey); err != nil {
			return err
		}
	}
	cur := loadAPIKeySnap()
	existing, ok := cur.byID[key.ID]
	if !ok {
		return ErrAPIKeyNotFound
	}
	if key.APIKey == "" {
		key.APIKey = existing.APIKey
	}
	if apiKeyValueTakenSnap(cur, key.APIKey, key.ID) {
		return ErrAPIKeyValueExists
	}
	// Preserve server-managed fields that must not be overwritten by client edits.
	// Save() does a full-column UPDATE; without this, CreatedAt (autoCreateTime only
	// fires on INSERT) and LastUsedAt (updated by auth middleware) would be zeroed.
	key.CreatedAt = existing.CreatedAt
	key.LastUsedAt = existing.LastUsedAt
	sealed, sealErr := seal.Seal(key.APIKey)
	if sealErr != nil {
		return fmt.Errorf("failed to encrypt API key: %w", sealErr)
	}
	dbKey := *key
	dbKey.APIKey = sealed
	if err := db.GetDB().WithContext(ctx).Save(&dbKey).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrAPIKeyValueExists
		}
		return fmt.Errorf("failed to update API key: %w", err)
	}
	next := cloneAPIKeySnap(cur)
	if key.APIKey != existing.APIKey {
		delete(next.byKey, existing.APIKey)
	}
	next.byKey[key.APIKey] = key.ID
	next.byID[key.ID] = *key
	apiKeySnap.Store(next)
	return nil
}

func APIKeyList(ctx context.Context) ([]model.APIKey, error) {
	s := loadAPIKeySnap()
	keys := make([]model.APIKey, 0, len(s.byID))
	for _, apiKey := range s.byID {
		keys = append(keys, apiKey)
	}
	// 叠加 DB 中最新的 created_at / last_used_at: 缓存快照由定期刷新填充,
	// last_used_at 由鉴权中间件异步写 DB 但不回写缓存, 列表展示时取 DB 真值。
	if len(keys) > 0 {
		type tsRow struct {
			ID         int
			CreatedAt  int64
			LastUsedAt int64
		}
		var rows []tsRow
		if err := db.GetDB().WithContext(ctx).
			Table("api_keys").
			Select("id, created_at, last_used_at").
			Scan(&rows).Error; err == nil {
			m := make(map[int]tsRow, len(rows))
			for _, r := range rows {
				m[r.ID] = r
			}
			for i := range keys {
				if r, ok := m[keys[i].ID]; ok {
					keys[i].CreatedAt = r.CreatedAt
					keys[i].LastUsedAt = r.LastUsedAt
				}
			}
		}
	}
	return keys, nil
}

func APIKeyGet(id int, ctx context.Context) (model.APIKey, error) {
	s := loadAPIKeySnap()
	apiKey, ok := s.byID[id]
	if !ok {
		return model.APIKey{}, ErrAPIKeyNotFound
	}
	return apiKey, nil
}

func APIKeyGetByAPIKey(apiKey string, ctx context.Context) (model.APIKey, error) {
	// 同一代快照内成对读取索引(key→id)与对象(id→APIKey), 不会跨代撕裂:
	// 一次 atomic.Load 拿到的 byKey 与 byID 必然属于同一代发布, 不会出现旧代码
	// 「先查 id map 命中、再查对象缓存时已被换代缺失」或反向的中间态。
	s := loadAPIKeySnap()
	id, ok := s.byKey[apiKey]
	if !ok {
		return model.APIKey{}, ErrAPIKeyNotFound
	}
	obj, ok := s.byID[id]
	if !ok {
		return model.APIKey{}, ErrAPIKeyNotFound
	}
	return obj, nil
}

func APIKeyDelete(id int, ctx context.Context) error {
	apiKeyCacheMu.Lock()
	defer apiKeyCacheMu.Unlock()

	cur := loadAPIKeySnap()
	existing, ok := cur.byID[id]
	if !ok {
		return ErrAPIKeyNotFound
	}
	result := db.GetDB().WithContext(ctx).Delete(&existing)
	if result.Error != nil {
		return fmt.Errorf("删除 API key 失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrAPIKeyNotFound
	}
	next := cloneAPIKeySnap(cur)
	delete(next.byID, existing.ID)
	delete(next.byKey, existing.APIKey)
	apiKeySnap.Store(next)
	return nil
}

func apiKeyRefreshCache(ctx context.Context) error {
	// STA-05 修复: DB Find 纳入 apiKeyCacheMu 同一同步边界, 与 delete/create/update
	// 互斥。此前 Find 在锁外读取, 可能拿到含已撤销 Key 的旧列表, 随后取锁发布时
	// 覆盖掉 delete 刚清掉的缓存, 复活已撤销 Key; 现在 Find 与发布同在锁内, 不会
	// 观测到比最新写入更旧的 DB 状态再回灌缓存。
	apiKeyCacheMu.Lock()
	defer apiKeyCacheMu.Unlock()

	apiKeys := []model.APIKey{}
	if err := db.GetDB().WithContext(ctx).Find(&apiKeys).Error; err != nil {
		return err
	}
	for i := range apiKeys {
		plain, decErr := seal.Open(apiKeys[i].APIKey)
		if decErr != nil {
			return decErr
		}
		apiKeys[i].APIKey = plain
	}
	if apiKeyRefreshTestHook != nil {
		// 钩子在锁内、Find 与发布之间, 仅供 STA-05 测试注入 barrier 验证持锁不变量。
		apiKeyRefreshTestHook("after-find")
	}
	byID := make(map[int]model.APIKey, len(apiKeys))
	byKey := make(map[string]int, len(apiKeys))
	for _, apiKey := range apiKeys {
		byID[apiKey.ID] = apiKey
		byKey[apiKey.APIKey] = apiKey.ID
	}
	apiKeySnap.Store(&apiKeySnapshot{byID: byID, byKey: byKey})
	if apiKeyRefreshTestHook != nil {
		apiKeyRefreshTestHook("after-publish")
	}
	return nil
}

// isUniqueConstraintError 兼容三种数据库 driver 的「唯一约束冲突」错误信息,
// 用于把 DB 层兜底的 UNIQUE 索引失败翻译成 sentinel, 让 handler 走统一的 4xx 路径。
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"unique constraint failed",
		"duplicate entry",
		"unique constraint",
		"duplicate key",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
