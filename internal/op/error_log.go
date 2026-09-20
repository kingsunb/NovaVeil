package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

// errorLogQueue 批量写入的内存队列与写入协程生命周期控制。
// errorLogLifeMu 保护生命周期字段的创建/停止/重启竞态; 写入协程本身不持有该锁。
var (
	errorLogLifeMu sync.Mutex

	errorLogQueue chan model.ErrorLog
	errorLogStop  chan struct{}
	errorLogDone  chan struct{}

	errorLogDropped atomic.Uint64

	// errorLogShuttingDown 标记停机排空流程已开始；此后 ErrorLogEnqueue 走
	// 短超时同步写入，避免在队列/写协程已停止时产生无人消费的滞留事件。
	errorLogShuttingDown bool
)

// errorLogShutdownFlushTimeout 停机排空 writer 的等待上限。writer 单批写入
// 预算是 30s，这里留出 5s 余量，避免 db.Close 与仍在写库的 writer 重叠。
const errorLogShutdownFlushTimeout = 35 * time.Second

// errorLogWriterAlive 返回写入协程是否处于存活状态; 调用方必须持有 errorLogLifeMu。
func errorLogWriterAlive() bool {
	if errorLogStop == nil || errorLogDone == nil {
		return false
	}
	select {
	case <-errorLogDone:
		return false
	default:
		return true
	}
}

// errBriefMaxBytes 错误摘要的最大保留字节数, 超长按 UTF-8 边界截断。
const errBriefMaxBytes = 256

// errorLogQueueCapacity 错误日志批量写入的内存队列容量; 队满时丢弃新条目并计数, 绝不阻塞转发链路。
const errorLogQueueCapacity = 512

// errorLogFlushInterval 批量写入的最大等待间隔: 攒批降低事务次数, 慢速磁盘下显著减少 IO 次数。
const errorLogFlushInterval = 5 * time.Second

// errorLogFlushThreshold 队列内积攒到该条数时立即触发一次批量写入, 不等间隔到期。
const errorLogFlushThreshold = 64

// ErrorLogListLimitDefault 错误日志查询接口未指定 limit 时的默认返回条数。
const ErrorLogListLimitDefault = 50

// ErrorLogListLimitMax 错误日志查询单次返回的条数上限, 防止误传超大 limit 拖垮内存。
const ErrorLogListLimitMax = 500

// ErrDatabaseNotInitialized 数据库尚未初始化时返回的哨兵错误:
// 无数据库环境(如未初始化 DB 的单元测试)下调用方据此静默跳过持久化。
var ErrDatabaseNotInitialized = errors.New("database not initialized")

// ErrorLogCreate 同步插入一条终态失败记录, 摘要与详情超长按字节截断。
// 转发热路径请改用 ErrorLogEnqueue 批量写入; 本函数保留给测试与低频管理操作。
func ErrorLogCreate(ctx context.Context, entry model.ErrorLog) error {
	gormDB := db.GetDB()
	if gormDB == nil {
		return ErrDatabaseNotInitialized
	}
	entry = sanitizeErrorLog(entry)
	if err := gormDB.WithContext(ctx).Create(&entry).Error; err != nil {
		return fmt.Errorf("写入错误日志失败: %w", err)
	}
	return nil
}

// ErrorLogEnqueue 把失败记录非阻塞投入内存队列, 由后台写入协程攒批落库。
// 协程未启动或已停止时惰性(重新)启动; 队列满载时丢弃该条并递增计数, 返回 false。
// 无数据库环境直接返回 false; 绝不阻塞转发链路。
// 入队路径对请求体做 JSON 感知截断(保留合法 JSON 结构), 正则脱敏由写入协程完成;
// 仅在失败路径(recordErrorLog)调用, JSON 解码开销可接受。入队前先截断也约束了队列内存与脱敏输入规模。
func ErrorLogEnqueue(entry model.ErrorLog) bool {
	errorLogLifeMu.Lock()
	if db.GetDB() == nil {
		errorLogLifeMu.Unlock()
		return false
	}
	if errorLogShuttingDown {
		errorLogLifeMu.Unlock()
		entry = sanitizeErrorLog(entry)
		ctx, cancel := context.WithTimeout(context.Background(), errorLogShutdownFlushTimeout)
		defer cancel()
		if err := ErrorLogCreate(ctx, entry); err != nil {
			if !errors.Is(err, ErrDatabaseNotInitialized) {
				log.Warnf("failed to write error log during shutdown: %v", err)
			}
			return false
		}
		return true
	}
	if !errorLogWriterAlive() {
		errorLogQueue = make(chan model.ErrorLog, errorLogQueueCapacity)
		errorLogStop = make(chan struct{})
		errorLogDone = make(chan struct{})
		go errorLogWriterLoop()
	}
	queue := errorLogQueue
	errorLogLifeMu.Unlock()

	entry = truncateErrorLogForEnqueue(entry)
	select {
	case queue <- entry:
		return true
	default:
		dropped := errorLogDropped.Add(1)
		if dropped == 1 || dropped%100 == 0 {
			log.Warnf("error log queue full, dropped total %d entries", dropped)
		}
		return false
	}
}

// truncateErrorLogForEnqueue 入队前的裁剪, 不含正则脱敏(脱敏由写入协程完成):
// API Key 只留尾缀, 摘要/详情按字节上限截断; 请求体走 JSON 感知截断以保留合法 JSON 结构,
// 使前端可格式化展示。仅在失败路径调用(recordErrorLog), JSON 解码开销可接受。
func truncateErrorLogForEnqueue(entry model.ErrorLog) model.ErrorLog {
	entry.APIKeySuffix = apiKeyTail(entry.APIKeySuffix)
	entry.ErrBrief = truncateUTF8Bytes(entry.ErrBrief, errBriefMaxBytes)
	entry.ErrDetail = truncateUTF8Bytes(entry.ErrDetail, model.MaxErrDetailBytes)
	entry.RequestBody = truncateRequestBodyJSON(entry.RequestBody, model.MaxRequestBodyLogBytes)
	entry.MaskMatches = normalizeMaskMatches(entry.MaskMatches)
	return entry
}

// FlushErrorLogQueue 停止后台写入协程并同步排空队列中尚未落库的错误日志,
// 供优雅关闭钩子调用; 必须注册在 db.Close 之前(shutdown 以 LIFO 执行)。
// 幂等且可重入: 协程已停止时仅排空残留条目; 后续 Enqueue 会自动重启协程。
//
// 阶段 1 先发出停止信号并清空句柄(在持锁状态下), 阶段 2 在 writer 退出后再加锁完成排空,
// 避免与捕获路径上的 CaptureConversation / 后续 ErrorLogEnqueue 形成竞态或被锁阻塞。
//
// 等待 writer 退出的预算独立于调用方 ctx(调用方通常是 8s 的 shutdown hook)：
// writer 单批插入预算为 30s，该函数会等到其结束或 35s 超时，确保 db.Close
// 不会与仍在写库的 writer 重叠。
func FlushErrorLogQueue(_ context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), errorLogShutdownFlushTimeout)
	defer cancel()

	errorLogLifeMu.Lock()
	if errorLogQueue == nil {
		errorLogLifeMu.Unlock()
		return nil
	}
	var done chan struct{}
	if errorLogWriterAlive() {
		close(errorLogStop)
		done = errorLogDone
		errorLogShuttingDown = true
	}
	queue := errorLogQueue
	errorLogLifeMu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-shutdownCtx.Done():
			log.Warnf("error log writer did not stop within %s", errorLogShutdownFlushTimeout)
			return fmt.Errorf("error log writer did not stop within %s", errorLogShutdownFlushTimeout)
		}
	}

	// 写入协程已退出, 此处独占排空剩余条目并一次性批量写入。
	batch := make([]model.ErrorLog, 0, errorLogQueueCapacity)
	if queue != nil {
		for {
			select {
			case entry := <-queue:
				batch = append(batch, sanitizeErrorLog(entry))
				continue
			default:
			}
			break
		}
	}
	if len(batch) == 0 {
		return nil
	}
	if err := insertErrorLogBatch(shutdownCtx, batch); err != nil && !errors.Is(err, ErrDatabaseNotInitialized) {
		log.Warnf("failed to flush %d error logs: %v", len(batch), err)
	} else if trimErr := ErrorLogTrimToMaxCount(shutdownCtx); trimErr != nil && !errors.Is(trimErr, ErrDatabaseNotInitialized) {
		log.Warnf("failed to trim error logs: %v", trimErr)
	}
	return nil
}

// ErrorLogList 按时间新到旧分页读取错误日志; limit 非正取默认值, 超上限按上限截断,
// class 非空时按错误分类过滤。
func ErrorLogList(ctx context.Context, limit int, class string) ([]model.ErrorLog, error) {
	if limit <= 0 {
		limit = ErrorLogListLimitDefault
	}
	if limit > ErrorLogListLimitMax {
		limit = ErrorLogListLimitMax
	}
	query := db.GetDB().WithContext(ctx).Model(&model.ErrorLog{}).Order("created_at DESC, id DESC").Limit(limit)
	if class != "" {
		query = query.Where("err_class = ?", class)
	}
	logs := make([]model.ErrorLog, 0, min(limit, 64))
	if err := query.Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("读取错误日志失败: %w", err)
	}
	// 兼容升级前已落库的旧记录: 返回管理 API 前再过滤一次, 防止历史明文凭据继续暴露。
	for i := range logs {
		logs[i] = sanitizeErrorLog(logs[i])
	}
	return logs, nil
}

// ErrorLogDeleteAll 清空全部错误日志记录。
func ErrorLogDeleteAll(ctx context.Context) error {
	if err := db.GetDB().WithContext(ctx).Where("1 = 1").Delete(&model.ErrorLog{}).Error; err != nil {
		return fmt.Errorf("删除错误日志失败: %w", err)
	}
	return nil
}

// ErrorLogCountSince 统计指定时刻以来的错误日志条数。
func ErrorLogCountSince(ctx context.Context, since time.Time) (int64, error) {
	var count int64
	if err := db.GetDB().WithContext(ctx).Model(&model.ErrorLog{}).Where("created_at >= ?", since).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计错误日志失败: %w", err)
	}
	return count, nil
}

// ErrorLogCleanExpired 按保留天数设置清理过期错误日志, 并按最大条数设置裁剪超量行,
// 返回删除的总行数。保留天数为 0(或非法负值)表示永久保留; 最大条数为 0 表示不按条数限制。
func ErrorLogCleanExpired(ctx context.Context) (int64, error) {
	days := ErrorLogRetentionDays(ctx)
	maxCount := ErrorLogRetentionMaxCount(ctx)
	if days <= 0 && maxCount <= 0 {
		return 0, nil
	}
	var removed int64
	if days > 0 {
		cutoff := time.Now().AddDate(0, 0, -days)
		result := db.GetDB().WithContext(ctx).Where("created_at < ?", cutoff).Delete(&model.ErrorLog{})
		if result.Error != nil {
			return 0, fmt.Errorf("清理过期错误日志失败: %w", result.Error)
		}
		removed += result.RowsAffected
	}
	if maxCount > 0 {
		// 派生表包裹 LIMIT 子查询: MySQL 禁止 IN 子查询直接带 LIMIT(ER 1235), 包一层后三方言通用。
		result := db.GetDB().WithContext(ctx).Exec(
			"DELETE FROM error_logs WHERE id NOT IN (SELECT id FROM (SELECT id FROM error_logs ORDER BY id DESC LIMIT ?) AS keep_rows)",
			maxCount,
		)
		if result.Error != nil {
			return removed, fmt.Errorf("按上限裁剪错误日志失败: %w", result.Error)
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

// ErrorLogTrimToMaxCount 批量落库后即时按条数上限裁剪(仅当设置了非零上限才产生查询开销)。
func ErrorLogTrimToMaxCount(ctx context.Context) error {
	maxCount := ErrorLogRetentionMaxCount(ctx)
	if maxCount <= 0 {
		return nil
	}
	gormDB := db.GetDB()
	if gormDB == nil {
		return ErrDatabaseNotInitialized
	}
	// 派生表包裹 LIMIT 子查询, 兼容 MySQL(ER 1235), 与 ErrorLogCleanExpired 保持同一写法。
	return gormDB.WithContext(ctx).Exec(
		"DELETE FROM error_logs WHERE id NOT IN (SELECT id FROM (SELECT id FROM error_logs ORDER BY id DESC LIMIT ?) AS keep_rows)",
		maxCount,
	).Error
}

// ErrorLogRetentionMaxCount 返回当前生效的错误日志最大保留条数:
// 设置缺失时惰性补种默认行; 值非法(负数)时按 0 处理; 历史值超过硬上限
// (如旧版默认 10000)时钳制到 ErrorRetentionMaxCountMax, 保证裁剪语义一致。
func ErrorLogRetentionMaxCount(ctx context.Context) int {
	if err := ensureErrorLogSetting(ctx, model.SettingKeyErrorRetentionMaxCount, model.DefaultErrorRetentionMaxCount); err != nil {
		log.Warnf("failed to ensure error max count setting: %v", err)
	}
	n, err := SettingGetInt(model.SettingKeyErrorRetentionMaxCount)
	if err != nil || n < 0 {
		return 0
	}
	if n > model.ErrorRetentionMaxCountMax {
		return model.ErrorRetentionMaxCountMax
	}
	return n
}

// ErrorLogRetentionDays 返回当前生效的错误日志保留天数:
// 设置缺失时惰性补种默认行; 值非法(负数)时按 0 处理, 避免误删数据。
func ErrorLogRetentionDays(ctx context.Context) int {
	if err := ErrorLogEnsureRetentionSetting(ctx); err != nil {
		log.Warnf("failed to ensure error retention setting: %v", err)
	}
	days, err := SettingGetInt(model.SettingKeyErrorRetentionDays)
	if err != nil {
		log.Warnf("failed to get error retention setting, fallback to default: %v", err)
		return model.DefaultErrorRetentionDays
	}
	if days < 0 {
		return 0
	}
	return days
}

// ErrorLogEnsureRetentionSetting 保证错误日志保留天数设置行存在:
// 该键未纳入 DefaultSettings, 由本函数在启动缓存初始化与首次使用时惰性补种默认值。
func ErrorLogEnsureRetentionSetting(ctx context.Context) error {
	return ensureErrorLogSetting(ctx, model.SettingKeyErrorRetentionDays, model.DefaultErrorRetentionDays)
}

// ensureErrorLogSetting 保证设置行存在并载入缓存: 键不在 DefaultSettings 的错误日志
// 策略项由本函数在启动缓存初始化与首次使用时惰性补种默认值。
func ensureErrorLogSetting(ctx context.Context, key model.SettingKey, defaultValue int) error {
	if _, ok := settingCache.Get(key); ok {
		return nil
	}
	gormDB := db.GetDB()
	if gormDB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	gormDB = gormDB.WithContext(ctx)

	var setting model.Setting
	err := gormDB.Where("key = ?", key).First(&setting).Error
	switch {
	case err == nil:
		settingCache.Set(setting.Key, setting.Value)
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("读取设置 %s 失败: %w", key, err)
	}
	setting = model.Setting{Key: key, Value: fmt.Sprintf("%d", defaultValue)}
	if err := gormDB.Create(&setting).Error; err != nil {
		// 并发补种时另一协程可能已插入成功, 重查一次兜底。
		var existing model.Setting
		if retryErr := gormDB.Where("key = ?", key).First(&existing).Error; retryErr == nil {
			settingCache.Set(existing.Key, existing.Value)
			return nil
		}
		return fmt.Errorf("创建设置 %s 失败: %w", key, err)
	}
	settingCache.Set(setting.Key, setting.Value)
	return nil
}

var (
	bearerSecretPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	// fieldSecretPattern 匹配「敏感字段名 + 分隔符 + 值」并替换为占位符。
	// 分隔符组 (\s*"?[:=]\s*) 在冒号/等号前接受可选的 JSON key 结束双引号,
	// 因此 "api_key":"..."、"password":"..." 这类 JSON 文本也能命中(而不仅限于
	// key: value / key=value 形式)。值组覆盖双引号/单引号(含转义字符)与无引号 token,
	// 无引号分支同时兜底截断边界处缺少闭合引号的不完整值。该正则仅作为 JSON 解码失败
	// 后的文本兜底脱敏, 永不作为放行原文的依据。
	fieldSecretPattern = regexp.MustCompile(`(?i)\b(authorization|proxy[-_ ]?authorization|x[-_ ]?api[-_ ]?key|api[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|auth[-_ ]?token|token|password|passwd|client[-_ ]?secret|secret)\b(\s*"?[:=]\s*)(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\s,;&}\]]+)`)
)

// sanitizeErrorLog 是所有错误日志写入与读取的统一安全边界。
func sanitizeErrorLog(entry model.ErrorLog) model.ErrorLog {
	entry.APIKeySuffix = apiKeyTail(entry.APIKeySuffix)
	entry.ErrBrief = truncateErrorBrief(redactSensitiveText(entry.ErrBrief))
	entry.ErrDetail = truncateErrorDetail(redactSensitiveText(entry.ErrDetail))
	entry.RequestBody = truncateRequestBodyJSON(redactRequestBody(entry.RequestBody), model.MaxRequestBodyLogBytes)
	entry.MaskMatches = normalizeMaskMatches(entry.MaskMatches)
	return entry
}

func apiKeyTail(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "..."))
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 4 {
		runes = runes[len(runes)-4:]
	}
	return "..." + string(runes)
}

// redactRequestBody 对请求体做有预算的结构化脱敏, 是 RequestBody 落库前的唯一安全边界。
// 入队前已按 MaxRequestBodyLogBytes 做 JSON 感知截断, 故本函数输入有界, 解码/正则代价可控。
//
// 策略(明确的安全失败):
//  1. 空串原样返回;
//  2. 先尝试 JSON 解码并递归脱敏敏感字段, 成功则重编码返回合法 JSON;
//  3. JSON 解码失败(非 JSON 文本)时绝不退回原文, 走文本正则兜底脱敏。
//     fieldSecretPattern 已覆盖 JSON key 结束双引号、引号转义、嵌套字段与截断处不完整值,
//     保证 "api_key":"..."、"password":"..." 等形态在结构化路径失效后仍被脱敏。
//     正则未命中的残余文本不含已知敏感字段名, 可安全保留。
func redactRequestBody(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	if redacted, ok := redactRequestBodyJSON(body); ok {
		return redacted
	}
	// JSON 不完整或非 JSON: 安全失败, 仅返回脱敏后的文本, 永不退回原文。
	return redactSensitiveText(body)
}

// redactRequestBodyJSON 尝试把请求体当作完整 JSON 解码、递归脱敏敏感字段后重编码。
// 成功(解码与重编码均无错)返回脱敏后的 JSON 与 true; 任一步失败返回 false,
// 由调用方走文本正则兜底, 避免把部分解析结果误当合法 JSON 落库。
func redactRequestBodyJSON(body string) (string, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	redactJSONValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func redactJSONValue(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if isSensitiveField(key) {
				current[key] = "[REDACTED]"
				continue
			}
			redactJSONValue(child)
		}
	case []any:
		for _, child := range current {
			redactJSONValue(child)
		}
	}
}

// truncateRequestBodyJSON 把请求体截断到 maxBytes 以内, 尽量保持 JSON 合法性:
// 能解码为完整 JSON 时, 递归缩短过长的字符串值使重编码结果落在预算内, 返回合法 JSON;
// 不能解码(非 JSON 或已破坏)时回退到 UTF-8 边界字节截断。
//
// 用于错误日志请求体截断: 字节截断会切断 JSON 字符串导致结构破坏, 前端无法格式化展示;
// JSON 感知截断保留合法 JSON 结构, 仅缩短过长的字符串值(如 messages[].content),
// 使前端 FormattedBody 始终能解析并缩进展示。
func truncateRequestBodyJSON(body string, maxBytes int) string {
	if len(body) <= maxBytes {
		return body
	}
	// 逐步收紧字符串值上限, 直到重编码落在预算内。
	for cap := 512; cap >= 16; cap /= 2 {
		encoded, ok := shrinkAndEncodeJSON(body, cap)
		if !ok {
			return truncateUTF8Bytes(body, maxBytes) // 非 JSON, 回退字节截断
		}
		if len(encoded) <= maxBytes {
			return encoded
		}
	}
	// 所有上限下仍超限(结构本身过大), 兜底字节截断紧凑编码。
	encoded, ok := shrinkAndEncodeJSON(body, 0)
	if !ok {
		return truncateUTF8Bytes(body, maxBytes)
	}
	return truncateUTF8Bytes(encoded, maxBytes)
}

// shrinkAndEncodeJSON 把 body 解码为 JSON, 按 maxLen 截断过长的字符串值后重编码为紧凑 JSON。
// maxLen <= 0 时不截断, 仅重编码。解码或重编码失败返回 ("", false)。
func shrinkAndEncodeJSON(body string, maxLen int) (string, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if maxLen > 0 {
		shrinkJSONStrings(value, maxLen)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// shrinkJSONStrings 递归截断 JSON 树中超过 maxLen 字节的字符串值,
// 截断处追加 "...[truncated]" 标记, 使重编码结果可控且保留合法 JSON 结构。
func shrinkJSONStrings(value any, maxLen int) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if str, ok := child.(string); ok {
				if len(str) > maxLen {
					current[key] = truncateUTF8Bytes(str, maxLen) + "...[truncated]"
				}
			} else {
				shrinkJSONStrings(child, maxLen)
			}
		}
	case []any:
		for i, child := range current {
			if str, ok := child.(string); ok {
				if len(str) > maxLen {
					current[i] = truncateUTF8Bytes(str, maxLen) + "...[truncated]"
				}
			} else {
				shrinkJSONStrings(child, maxLen)
			}
		}
	}
}

func isSensitiveField(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, key)
	return normalized == "authorization" || normalized == "proxyauthorization" ||
		strings.HasSuffix(normalized, "apikey") || strings.HasSuffix(normalized, "token") ||
		strings.HasSuffix(normalized, "password") || strings.HasSuffix(normalized, "passwd") ||
		strings.HasSuffix(normalized, "secret")
}

func redactSensitiveText(text string) string {
	if text == "" {
		return ""
	}
	text = bearerSecretPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	return fieldSecretPattern.ReplaceAllString(text, "$1$2[REDACTED]")
}

// truncateErrorBrief 把错误摘要按字节截断到上限内, 并回退到完整的 UTF-8 边界避免出现残缺字符。
func truncateErrorBrief(text string) string {
	return truncateUTF8Bytes(text, errBriefMaxBytes)
}

// truncateErrorDetail 把错误详情按字节截断到上限内(UTF-8 边界安全)。
func truncateErrorDetail(text string) string {
	return truncateUTF8Bytes(text, model.MaxErrDetailBytes)
}

// truncateUTF8Bytes 按字节上限截断文本并保证不切断多字节字符。
// 真正发生截断时用 strings.Clone 复制小结果, 释放对原大字符串底层存储的引用,
// 避免预览/日志字段钉住完整报文内存; 未超限的短串路径直接返回原串, 不做无意义复制。
func truncateUTF8Bytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return strings.Clone(cut)
}

// errorLogWriterLoop 攒批落库: 每 errorLogFlushInterval 或积攒 errorLogFlushThreshold 条
// 触发一次单事务批量插入; 收到停止信号后直接退出, 排空由 FlushErrorLogQueue 独占完成。
func errorLogWriterLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("error log writer panicked: %v", r)
		}
		close(errorLogDone)
	}()

	ticker := time.NewTicker(errorLogFlushInterval)
	defer ticker.Stop()

	batch := make([]model.ErrorLog, 0, errorLogFlushThreshold)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := insertErrorLogBatch(ctx, batch)
		cancel()
		if err != nil && !errors.Is(err, ErrDatabaseNotInitialized) {
			log.Warnf("failed to flush %d error logs: %v", len(batch), err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case entry := <-errorLogQueue:
			// 脱敏在此完成(消费侧), 入队路径只做截断; 条目已裁剪到上限内, 正则与
			// JSON 重编码的输入有界, 不会阻塞转发链路。
			batch = append(batch, sanitizeErrorLog(entry))
			if len(batch) >= errorLogFlushThreshold {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-errorLogStop:
			// 排空已进入本协程视野的剩余条目后再退出;
			// 此后到达队列的条目由 FlushErrorLogQueue 兜底收集。
			for {
				select {
				case entry := <-errorLogQueue:
					batch = append(batch, sanitizeErrorLog(entry))
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// insertErrorLogBatch 单事务批量写入一批失败记录, 最小化慢速磁盘上的事务与 fsync 次数。
func insertErrorLogBatch(ctx context.Context, entries []model.ErrorLog) error {
	gormDB := db.GetDB()
	if gormDB == nil {
		return ErrDatabaseNotInitialized
	}
	err := gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(&entries, len(entries)).Error
	})
	if err != nil {
		return fmt.Errorf("批量写入错误日志失败: %w", err)
	}
	return nil
}

// errDedupLastRun 记录每个 err_class 上次执行按类去重清扫的 Unix 秒, 节流用。
var errDedupLastRun sync.Map // class string -> *atomic.Int64

// errDedupMinInterval 同一 err_class 两次去重清扫之间的最小间隔。
// 故障风暴下同一 class 每秒可能失败数百次, 每次都触发 COUNT/DELETE 会以全量查询
// 冲击数据库恰好最脆弱的时刻; 节流到间隔内一次即可保证"同类只保留最近 keep 条"的最终一致。
const errDedupMinInterval = 60 * time.Second

// ErrorLogDedupPerClass 确保同一 err_class 只保留最近 keep 条记录, 超出的删除最旧的。
// 每类按 errDedupMinInterval 节流执行; 配合 (err_class, created_at) 复合索引, 三条语句
// 都走索引范围扫描。执行在独立协程, 绝不阻塞调用方。
func ErrorLogDedupPerClass(class string, keep int) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return
	}
	now := time.Now().Unix()
	counter, _ := errDedupLastRun.LoadOrStore(class, &atomic.Int64{})
	last := counter.(*atomic.Int64)
	prev := last.Load()
	if now-prev < int64(errDedupMinInterval/time.Second) {
		return
	}
	// CAS 抢占: 同一时刻同类只放行一个清扫协程, 其余调用直接跳过。
	if !last.CompareAndSwap(prev, now) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("error log dedup goroutine panicked: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var count int64
		if err := gormDB.WithContext(ctx).Model(&model.ErrorLog{}).Where("err_class = ?", class).Count(&count).Error; err != nil {
			return
		}
		if count <= int64(keep) {
			return
		}
		// 找到第 keep 条的 created_at, 删除更早的
		var cutoff time.Time
		if err := gormDB.WithContext(ctx).Model(&model.ErrorLog{}).
			Where("err_class = ?", class).
			Order("created_at DESC").
			Offset(keep-1).Limit(1).
			Pluck("created_at", &cutoff).Error; err != nil {
			return
		}
		if !cutoff.IsZero() {
			gormDB.WithContext(ctx).
				Where("err_class = ? AND created_at < ?", class, cutoff).
				Delete(&model.ErrorLog{})
		}
	}()
}
