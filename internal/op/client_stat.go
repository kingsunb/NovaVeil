package op

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm/clause"
)

// clientStatMu 保护 clientStatCache 与 clientStatOrphaned 的并发访问。
var clientStatMu sync.Mutex

// clientStatOrphaned 保存因缓存触顶/过期被逐出的条目, 等待下一次 ClientStatFlush
// 落库后清空。这样逐出不丢未刷增量, 且该 IP 再次出现时可沿用原累计值,
// 避免"数据库已有 RequestCount 100, 重插入后按 1 覆盖"的丢计数问题。
var clientStatOrphaned = make(map[string]*model.ClientStat)

// maxClientStatCacheFallback 内存缓存的 IP 条目上限回退值: 设置未配置或读取失败时使用。
const maxClientStatCacheFallback = 10000

// getClientStatMaxCount 从设置读取客户端统计最大保留条数; 0=不限制。
// 读取失败或值非法时回退到 maxClientStatCacheFallback。
func getClientStatMaxCount() int {
	v, err := SettingGetInt(model.SettingKeyClientStatMaxCount)
	if err != nil || v < 0 {
		return maxClientStatCacheFallback
	}
	return v
}

// clientStatRetentionDays 数据库与内存中客户端统计的保留天数: 最后调用超过该天数即清理。
// 统计仅作面板展示与防滥用审计, 30 天足够; 保留期清理由每次 flush 顺带执行。
const clientStatRetentionDays = 30

// TrackClientStat UPSERT 一条客户端调用记录: 首次插入, 后续仅递增计数并更新时间。
// 写入内存缓存, 由定时任务批量刷入数据库(与其他统计一致), 避免每请求一次 DB 操作。
func TrackClientStat(ip string) {
	if ip == "" {
		return
	}
	now := time.Now()
	clientStatMu.Lock()
	row, ok := clientStatCache[ip]
	if !ok {
		// 被逐出的 IP 再出现时沿用其未刷累计值, 避免重插入覆盖数据库总计数。
		if orphan, exists := clientStatOrphaned[ip]; exists {
			maxCount := getClientStatMaxCount()
			if maxCount > 0 && len(clientStatCache) >= maxCount {
				evictClientStatLocked(now)
			}
			delete(clientStatOrphaned, ip)
			orphan.LastSeen = now
			orphan.RequestCount++
			clientStatCache[ip] = orphan
			clientStatMu.Unlock()
			return
		}
		maxCount := getClientStatMaxCount()
		// maxCount=0 表示不限制; 否则触顶淘汰。
		if maxCount > 0 && len(clientStatCache) >= maxCount {
			evictClientStatLocked(now)
		}
		clientStatCache[ip] = &model.ClientStat{IP: ip, FirstSeen: now, LastSeen: now, RequestCount: 1}
	} else {
		row.LastSeen = now
		row.RequestCount++
	}
	clientStatMu.Unlock()
}

// evictClientStatLocked 触顶时的淘汰路径: 先删除超过保留期的陈旧条目;
// 若仍达上限(单窗口内活跃唯一 IP 极端超标), 再逐出 LastSeen 最旧的一条。
// 仅在插入新 IP 且缓存已满时触发, 均摊开销可忽略。调用方必须持有 clientStatMu。
func evictClientStatLocked(now time.Time) {
	maxCount := getClientStatMaxCount()
	if maxCount <= 0 {
		return // 不限制
	}
	cutoff := now.AddDate(0, 0, -clientStatRetentionDays)
	for ip, row := range clientStatCache {
		if row.LastSeen.Before(cutoff) {
			clientStatOrphaned[ip] = row
			delete(clientStatCache, ip)
		}
	}
	if len(clientStatCache) < maxCount {
		return
	}
	var oldestIP string
	var oldestRow *model.ClientStat
	var oldest time.Time
	for ip, row := range clientStatCache {
		if oldestIP == "" || row.LastSeen.Before(oldest) {
			oldestIP = ip
			oldestRow = row
			oldest = row.LastSeen
		}
	}
	if oldestIP != "" {
		clientStatOrphaned[oldestIP] = oldestRow
		delete(clientStatCache, oldestIP)
	}
}

// ClientStatList 返回按最后调用时间倒序的全部客户端统计。
func ClientStatList() []model.ClientStat {
	clientStatMu.Lock()
	out := make([]model.ClientStat, 0, len(clientStatCache))
	for _, v := range clientStatCache {
		out = append(out, *v)
	}
	clientStatMu.Unlock()
	// 排序在锁外完成, 不阻塞每请求的计数写入; 快照为值拷贝, 与后续变更无竞争。
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ClientStatCount 返回去重后的客户端 IP 数量。
// 与 relay.ClientIPCount 的区别: 本计数基于 clientStatCache, 后者在启动时由
// ClientStatLoad 从数据库加载历史记录, 重启后仍能反映历史调用方; relay.ClientIPCount
// 的进程内集合重启清零, 仅反映本次启动以来的新请求。
func ClientStatCount() int {
	clientStatMu.Lock()
	defer clientStatMu.Unlock()
	return len(clientStatCache)
}

// ClientStatCountSince 返回指定时间点后活跃(最后调用时间 last_seen >= since)的去重
// 客户端 IP 数, 供仪表盘"客户端 IP"KPI 按时间窗口联动统计。since 为零值时返回全表
// 去重 IP 数(不限时间窗口)。
// 走数据库查询而非内存缓存: 内存缓存仅反映本进程可见条目, 且按 maxClientStatCache
// 有界淘汰; 数据库 client_stats 表由 ClientStatFlush 周期落库并按保留期清理, 是跨重启
// 的权威来源。尚未 flush 的近期条目可能略少计入, 对面板 KPI 近似指示可接受。
// 数据库未初始化返回 0 与 nil error, 按优雅降级处理。
func ClientStatCountSince(ctx context.Context, since time.Time) (int64, error) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return 0, nil
	}
	var count int64
	q := gormDB.WithContext(ctx).Model(&model.ClientStat{})
	if !since.IsZero() {
		q = q.Where("last_seen >= ?", since)
	}
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计客户端 IP 失败: %w", err)
	}
	return count, nil
}

// ClientStatFlush 将内存中的客户端统计批量写入数据库(UPSERT), 并清理超过保留期的旧行。
// 在锁内只做快照拷贝, 数据库写入在锁外进行: 落库耗时(慢速磁盘/写锁竞争)不再阻塞
// 每请求的 TrackClientStat。内存行保持自启动以来的累计值, UPSERT 按内存值整体覆盖。
func ClientStatFlush(ctx context.Context) error {
	clientStatMu.Lock()
	values := make([]model.ClientStat, 0, len(clientStatCache))
	for _, v := range clientStatCache {
		values = append(values, *v)
	}
	orphanSnapshot := make(map[string]*model.ClientStat, len(clientStatOrphaned))
	for ip, v := range clientStatOrphaned {
		values = append(values, *v)
		orphanSnapshot[ip] = v
	}
	clientStatMu.Unlock()

	if len(values) > 0 {
		if err := upsertClientStats(ctx, values); err != nil {
			return err
		}
		// 仅在已成功落库的孤儿条目移除占位。写入期间新逐出的条目是新的指针,
		// 按指针判等可避免误删。
		clientStatMu.Lock()
		for ip, v := range orphanSnapshot {
			if clientStatOrphaned[ip] == v {
				delete(clientStatOrphaned, ip)
			}
		}
		clientStatMu.Unlock()
	}

	gormDB := db.GetDB()
	if gormDB == nil {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -clientStatRetentionDays)
	if err := gormDB.WithContext(ctx).Where("last_seen < ?", cutoff).Delete(&model.ClientStat{}).Error; err != nil {
		log.Warnf("client stat retention cleanup: %v", err)
	}
	return nil
}

// upsertClientStats 单事务批量 UPSERT, 以 ip 为主键冲突键整体覆盖(内存持有累计值)。
func upsertClientStats(ctx context.Context, values []model.ClientStat) error {
	gormDB := db.GetDB()
	if gormDB == nil {
		return nil
	}
	err := gormDB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ip"}},
		UpdateAll: true,
	}).CreateInBatches(&values, 500).Error
	if err != nil {
		log.Errorf("client stat flush: %v", err)
		return err
	}
	return nil
}

// ClientStatLoad 从数据库加载既有记录到内存缓存。
// 采用"合并而非覆盖": 内存中已有条目持有自启动以来的累计值(含尚未 flush 的增量),
// 保留内存版本以避免运行时重载(如设置导入触发 InitCache)丢失未刷增量; 仅补入 DB 中
// 内存没有的条目(上一进程遗留, 或本进程已淘汰且累计值已在上轮 flush 落库的)。
// 启动首次调用时缓存为空, 退化为全量加载, 行为与原实现一致。
func ClientStatLoad(ctx context.Context) error {
	var rows []model.ClientStat
	if err := db.GetDB().WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	clientStatMu.Lock()
	defer clientStatMu.Unlock()
	if clientStatCache == nil {
		clientStatCache = make(map[string]*model.ClientStat, len(rows))
	}
	for i := range rows {
		// 仅补入内存没有的条目; 内存已有条目保留其累计值(含未刷增量)。
		// 若该 IP 已作为逐出孤儿存在, 保留孤儿值(其累计计数覆盖数据库旧值),
		// 避免同一 IP 同时出现在 cache 与 orphan 导致单批 UPSERT 内重复键。
		if _, exists := clientStatCache[rows[i].IP]; !exists {
			if _, orphaned := clientStatOrphaned[rows[i].IP]; !orphaned {
				clientStatCache[rows[i].IP] = &rows[i]
			}
		}
	}
	return nil
}

// clientStatCache 内存缓存, 键为 IP。
var clientStatCache = make(map[string]*model.ClientStat)
