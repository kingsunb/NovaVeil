package op

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 用量分桶: 请求终态把 token 用量 UPSERT 累加到 (UTC 小时 × 上游模型) 一行,
// 一张表同时支撑主页趋势图、模型用量 Top 与总量 KPI。桶粒度取小时:
// 24h 档 1 小时/点, 7d 档 6 小时/点(28 点), 30d 档 24 小时/点(30 点)。

// UsageBucketHour 桶粒度; 导出供测试与未来按日归档扩展使用。
const UsageBucketHour = time.Hour

// usageBucketNow 取桶时钟; 可注入以便测试冻结时间(参照 conversationDiskHasRoom 模式)。
var usageBucketNow = time.Now

// RecordUsageBucket 把一次请求终态的用量累加到对应 (小时, 模型) 桶。
// 同步单行 UPSERT: 请求已定稿, 该调用不阻塞其他请求的转发循环。
// 跳过条件: 数据库未初始化(无 DB 的单测环境)、模型名为空(未路由到的请求)、
// 双零用量(上游未上报, 不写零行污染聚合)。
// 负 token 是上游异常上报, 逐项钳为 0, 绝不把负增量 UPSERT 进累计值。
// reasoning/cached 为推理与缓存命中 token, cost 为预计消耗(USD), durationMs 为请求耗时(毫秒)。
// 失败仅 warn 降级: 用量统计允许少量丢失, 绝不因落库错误影响请求定稿路径。
func RecordUsageBucket(targetModel string, input, output, reasoning, cached int64, cost float64, durationMs int64) {
	gormDB := db.GetDB()
	if gormDB == nil || targetModel == "" || (input <= 0 && output <= 0) {
		return
	}
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	if reasoning < 0 {
		reasoning = 0
	}
	if cached < 0 {
		cached = 0
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if input == 0 && output == 0 {
		return
	}
	bucket := model.UsageBucket{
		BucketAt:        usageBucketNow().UTC().Truncate(UsageBucketHour),
		ModelName:       targetModel,
		InputTokens:     input,
		OutputTokens:    output,
		ReasoningTokens: reasoning,
		CachedTokens:    cached,
		Cost:            cost,
		DurationMs:      durationMs,
		RequestCount:    1,
	}
	// ON CONFLICT (bucket_at, model_name) DO UPDATE 累加, 三方言由 GORM 自动适配;
	// 只累加不覆盖, 并发定稿的多个请求各自 UPSERT 不会互相吞量。
	err := gormDB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_at"},
			{Name: "model_name"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"input_tokens":     gorm.Expr("input_tokens + ?", input),
			"output_tokens":    gorm.Expr("output_tokens + ?", output),
			"reasoning_tokens": gorm.Expr("reasoning_tokens + ?", reasoning),
			"cached_tokens":    gorm.Expr("cached_tokens + ?", cached),
			"cost":             gorm.Expr("cost + ?", cost),
			"duration_ms":      gorm.Expr("duration_ms + ?", durationMs),
			"request_count":    gorm.Expr("request_count + ?", 1),
		}),
	}).Create(&bucket).Error
	if err != nil {
		log.Warnf("record usage bucket failed: %v", err)
	}
}

// UsageTrendPoint 趋势图单点: t 为采样区间起点(Unix 毫秒), in/out 为该区间
// 全部模型的 token 合计。
type UsageTrendPoint struct {
	T   int64 `json:"t"`
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

// usageWindow 各 range 档位的聚合参数: 窗口总时长与单点步长。
type usageWindow struct {
	span  time.Duration // 窗口总时长
	step  time.Duration // 单点覆盖的桶时长
	count int           // 点数
}

var usageWindows = map[string]usageWindow{
	"24h":     {span: 24 * time.Hour, step: time.Hour, count: 24},
	"7d":      {span: 7 * 24 * time.Hour, step: 6 * time.Hour, count: 28},
	"30d":     {span: 30 * 24 * time.Hour, step: 24 * time.Hour, count: 30},
	"1y":      {span: 365 * 24 * time.Hour, step: 7 * 24 * time.Hour, count: 53},        // 1 年: 周粒度 53 点 (含当前周, 避免 idx 越界丢弃最新数据)
	"3y":      {span: 3 * 365 * 24 * time.Hour, step: 30 * 24 * time.Hour, count: 37},   // 3 年: 月粒度 37 点 (含当前月)
	"forever": {span: 10 * 365 * 24 * time.Hour, step: 30 * 24 * time.Hour, count: 121}, // 永久: 以 10 年月粒度近似, 覆盖全量历史 (含当前月)
}

// ValidUsageRange 校验趋势查询档位, 供 handler 层 400 判定使用。
func ValidUsageRange(r string) bool {
	_, ok := usageWindows[r]
	return ok
}

// UsageTrendPoints 返回 range 档位的时间序列: 拉取窗口内的全部桶行,
// 在 Go 内存按步长聚合为 count 个点。点从窗口起点开始、到当前小时收尾,
// 末点包含仍在进行中的当前小时桶; 窗口内无数据的点输出 0, 保证 X 轴连续。
// 返回的 error 在 DB 查询失败时非 nil: 此时 points 仍为按档位填好的零值时间轴
// (保留 X 轴连续), 调用方可据此区分"窗口内无流量"(err==nil 且全零)与"统计不可用"(err!=nil)。
// 数据库未初始化(无 DB 单测环境)返回零值时间轴与 nil error, 按优雅降级处理。
func UsageTrendPoints(ctx context.Context, rangeKey string) ([]UsageTrendPoint, error) {
	window, ok := usageWindows[rangeKey]
	if !ok {
		rangeKey = "7d"
		window = usageWindows[rangeKey]
	}
	points := make([]UsageTrendPoint, 0, window.count)

	gormDB := db.GetDB()
	if gormDB == nil {
		now := usageBucketNow().UTC().Truncate(time.Hour)
		start := now.Add(-window.span + time.Hour)
		for i := 0; i < window.count; i++ {
			points = append(points, UsageTrendPoint{T: start.Add(time.Duration(i) * window.step).UnixMilli()})
		}
		return points, nil
	}

	// 对齐到小时: 窗口从「当前小时 - span + 1 小时」起, 末点覆盖进行中的当前小时。
	now := usageBucketNow().UTC().Truncate(time.Hour)
	start := now.Add(-window.span + time.Hour)

	buckets := make([]model.UsageBucket, 0)
	// 读取下界多取一个步长: 30d 等大步长档位下, 窗口起点若落在桶中间,
	// 该桶的用量按占比折算进首点没有意义, 直接整桶计入首点更直观。
	err := gormDB.WithContext(ctx).
		Where("bucket_at >= ? AND bucket_at < ?", start, now.Add(time.Hour)).
		Find(&buckets).Error
	if err != nil {
		log.Warnf("query usage buckets failed: %v", err)
	}
	// 桶 → 点的映射: (桶时刻 - 窗口起点) / 步长 = 点下标; 边界桶归入相邻点。
	sums := make(map[int]*UsageTrendPoint, window.count)
	for i := 0; i < window.count; i++ {
		sums[i] = &UsageTrendPoint{T: start.Add(time.Duration(i) * window.step).UnixMilli()}
	}
	for _, b := range buckets {
		idx := int(b.BucketAt.Sub(start) / window.step)
		if idx < 0 || idx >= window.count {
			continue
		}
		sums[idx].In += b.InputTokens
		sums[idx].Out += b.OutputTokens
	}
	for i := 0; i < window.count; i++ {
		points = append(points, *sums[i])
	}
	return points, err
}

// UsageTotals 返回全表 input/output token 总和(跨全部历史, 非 24h 窗口),
// 供 now-version 的 Token KPI 使用; 空表返回零值。
// 受用量保留天数设置约束: 非 0 保留时仅聚合保留窗口内的分桶, 0(永久)聚合全量。
// DB 查询失败时返回 0/0 与非 nil error, 让调用方区分"无流量"与"统计不可用";
// 数据库未初始化返回 0/0 与 nil error, 按优雅降级处理。
func UsageTotals(ctx context.Context) (totalInput, totalOutput int64, err error) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return 0, 0, nil
	}
	var row struct {
		Input  int64
		Output int64
	}
	sql := "SELECT COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output FROM usage_buckets"
	var args []any
	if cutoff := usageRetentionCutoff(); !cutoff.IsZero() {
		sql += " WHERE bucket_at >= ?"
		args = append(args, cutoff)
	}
	err = gormDB.WithContext(ctx).Raw(sql, args...).Scan(&row).Error
	if err != nil {
		log.Warnf("aggregate usage totals failed: %v", err)
		return 0, 0, err
	}
	return row.Input, row.Output, nil
}

// UsageWindowSince 返回 rangeKey 档位的 KPI 统计下界时刻:
//   - "forever" 或非法档位返回零值时间, 调用方据此走全量口径;
//   - 其余档位返回「当前小时 − span」, 与桶的小时粒度对齐。
//
// 供 handler 层为 client_stats / error_logs 等非 usage_buckets 表计算窗口下界,
// 让四个 KPI 卡片共享同一时间口径。
func UsageWindowSince(rangeKey string) time.Time {
	window, ok := usageWindows[rangeKey]
	if !ok || rangeKey == "forever" {
		return time.Time{}
	}
	return usageBucketNow().UTC().Truncate(time.Hour).Add(-window.span)
}

// UsageKPIsByRange 返回指定时间窗口内的 Token 用量与请求计数汇总, 供仪表盘 KPI
// 按趋势档位联动。rangeKey 为 "forever" 时不加窗口条件(全表聚合, token 口径等同
// UsageTotals, 额外返回 request_count 全表合计); 其余档位按 usageWindows 的 span
// 计算 since, 仅聚合 bucket_at >= since 的行。forever 仍受用量保留天数约束, 与
// UsageTotals 保持一致; 非 forever 不再叠加保留下界(表内分桶已被 UsageBucketCleanExpired
// 清理至保留窗口内, 窗口条件已隐含)。
// DB 查询失败返回零值与非 nil error, 让调用方区分"无流量"与"统计不可用";
// 数据库未初始化返回零值与 nil error, 按优雅降级处理。
func UsageKPIsByRange(ctx context.Context, rangeKey string) (input, output, requestCount int64, err error) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return 0, 0, 0, nil
	}
	window, ok := usageWindows[rangeKey]
	if !ok {
		rangeKey = "forever"
		window = usageWindows[rangeKey]
	}
	var row struct {
		Input  int64
		Output int64
		ReqCnt int64
	}
	sql := "SELECT COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output, " +
		"COALESCE(SUM(request_count), 0) AS req_cnt FROM usage_buckets"
	var args []any
	if rangeKey != "forever" {
		// 非 forever: 按窗口下界过滤(表内分桶已被 UsageBucketCleanExpired 清理至保留窗口,
		// 窗口条件已隐含保留约束, 无需再叠加 cutoff)。
		since := usageBucketNow().UTC().Truncate(time.Hour).Add(-window.span)
		sql += " WHERE bucket_at >= ?"
		args = append(args, since)
	} else if cutoff := usageRetentionCutoff(); !cutoff.IsZero() {
		// forever: 全量口径, 仅受用量保留天数约束, 与 UsageTotals 保持一致。
		sql += " WHERE bucket_at >= ?"
		args = append(args, cutoff)
	}
	err = gormDB.WithContext(ctx).Raw(sql, args...).Scan(&row).Error
	if err != nil {
		log.Warnf("aggregate usage KPIs by range failed: %v", err)
		return 0, 0, 0, err
	}
	return row.Input, row.Output, row.ReqCnt, nil
}

// UsageTotalsByModel 返回按模型聚合的 input/output 总和, 按合计降序取前 10,
// 供 now-version 的模型用量 Top 使用。
// 受用量保留天数设置约束: 非 0 保留时仅聚合保留窗口内的分桶, 0(永久)聚合全量。
// DB 查询失败时返回空切片与非 nil error, 让调用方区分"无流量"与"统计不可用";
// 数据库未初始化返回空切片与 nil error, 按优雅降级处理。
func UsageTotalsByModel(ctx context.Context) ([]ModelTokenUsage, error) {
	byModel := make([]ModelTokenUsage, 0)
	gormDB := db.GetDB()
	if gormDB == nil {
		return byModel, nil
	}
	sql := "SELECT model_name AS name, COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output " +
		"FROM usage_buckets GROUP BY model_name " +
		"ORDER BY (COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(output_tokens), 0)) DESC LIMIT 10"
	var args []any
	if cutoff := usageRetentionCutoff(); !cutoff.IsZero() {
		sql = "SELECT model_name AS name, COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output " +
			"FROM usage_buckets WHERE bucket_at >= ? GROUP BY model_name " +
			"ORDER BY (COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(output_tokens), 0)) DESC LIMIT 10"
		args = append(args, cutoff)
	}
	err := gormDB.WithContext(ctx).Raw(sql, args...).Scan(&byModel).Error
	if err != nil {
		log.Warnf("aggregate usage by model failed: %v", err)
		return byModel, err
	}
	return byModel, nil
}

// RecordErrorBucket 把一次业务失败累加到 (小时, 模型) 桶的 error_count。
// 与错误日志表的条数上限解耦, 供仪表盘错误 KPI 按时间窗口统计。
func RecordErrorBucket(targetModel string) {
	gormDB := db.GetDB()
	if gormDB == nil || targetModel == "" {
		return
	}
	bucket := model.UsageBucket{
		BucketAt:  usageBucketNow().UTC().Truncate(UsageBucketHour),
		ModelName: targetModel,
		ErrorCount: 1,
	}
	err := gormDB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_at"},
			{Name: "model_name"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"error_count": gorm.Expr("error_count + ?", 1),
		}),
	}).Create(&bucket).Error
	if err != nil {
		log.Warnf("record error bucket failed: %v", err)
	}
}

// UsageErrorCountByRange 返回时间窗口内 error_count 合计。
func UsageErrorCountByRange(ctx context.Context, rangeKey string) (int64, error) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return 0, nil
	}
	window, ok := usageWindows[rangeKey]
	if !ok {
		rangeKey = "forever"
		window = usageWindows[rangeKey]
	}
	var total int64
	sql := "SELECT COALESCE(SUM(error_count), 0) FROM usage_buckets"
	var args []any
	if rangeKey != "forever" {
		since := usageBucketNow().UTC().Truncate(time.Hour).Add(-window.span)
		sql += " WHERE bucket_at >= ?"
		args = append(args, since)
	} else if cutoff := usageRetentionCutoff(); !cutoff.IsZero() {
		sql += " WHERE bucket_at >= ?"
		args = append(args, cutoff)
	}
	if err := gormDB.WithContext(ctx).Raw(sql, args...).Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// UsageTotalsByModelRange 按时间窗口聚合模型用量 Top, 与 KPI 档位一致。
func UsageTotalsByModelRange(ctx context.Context, rangeKey string) ([]ModelTokenUsage, error) {
	if rangeKey == "" || rangeKey == "forever" {
		return UsageTotalsByModel(ctx)
	}
	window, ok := usageWindows[rangeKey]
	if !ok {
		return UsageTotalsByModel(ctx)
	}
	byModel := make([]ModelTokenUsage, 0)
	gormDB := db.GetDB()
	if gormDB == nil {
		return byModel, nil
	}
	since := usageBucketNow().UTC().Truncate(time.Hour).Add(-window.span)
	sql := "SELECT model_name AS name, COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output " +
		"FROM usage_buckets WHERE bucket_at >= ? GROUP BY model_name " +
		"ORDER BY (COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(output_tokens), 0)) DESC LIMIT 10"
	if err := gormDB.WithContext(ctx).Raw(sql, since).Scan(&byModel).Error; err != nil {
		log.Warnf("aggregate usage by model range failed: %v", err)
		return byModel, err
	}
	return byModel, nil
}

// usageRetentionDays 返回当前生效的用量分桶保留天数:
// 设置缺失或非法时回退默认值(0=永久保留); 负值按 0 处理。0 表示永久保留,
// 非 0 值受 model.UsageRetentionDaysMin 约束(校验在设置写入时拦截, 此处仅兜底)。
func usageRetentionDays() int {
	days, err := SettingGetInt(model.SettingKeyUsageRetentionDays)
	if err != nil {
		return model.DefaultUsageRetentionDays
	}
	if days < 0 {
		return 0
	}
	return days
}

// usageRetentionCutoff 返回用量保留下界时刻; 永久保留(0)返回零值时间,
// 调用方据此跳过 WHERE 子句, 保持全量聚合与历史行为一致。
func usageRetentionCutoff() time.Time {
	days := usageRetentionDays()
	if days <= 0 {
		return time.Time{}
	}
	return usageBucketNow().UTC().AddDate(0, 0, -days)
}

// UsageBucketCleanExpired 按用量保留天数设置清理过期分桶, 返回删除行数。
// 保留天数为 0(永久保留)或非法负值时不做任何清理。模式参照 ErrorLogCleanExpired。
func UsageBucketCleanExpired(ctx context.Context) (int64, error) {
	days := usageRetentionDays()
	if days <= 0 {
		return 0, nil
	}
	cutoff := usageBucketNow().UTC().AddDate(0, 0, -days)
	result := db.GetDB().WithContext(ctx).Where("bucket_at < ?", cutoff).Delete(&model.UsageBucket{})
	if result.Error != nil {
		return 0, fmt.Errorf("清理过期用量分桶失败: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// 详细指标 & 热力图查询 (参考 TokenArena 的 Usage 总览 / 详细指标 / 预计消耗 / 使用时长 / 热力图)
// ──────────────────────────────────────────────────────────────────────────────

// UsageDetailByRange 返回指定时间窗口内的详细指标汇总: input/output/reasoning/cached
// token、预计消耗(cost)、使用时长(duration_ms)与请求计数, 供仪表盘详细指标卡片使用。
// rangeKey 为 "forever" 时不加窗口条件(全表聚合); 其余档位按 usageWindows 的 span 过滤。
// DB 查询失败返回零值与非 nil error; 数据库未初始化返回零值与 nil error(优雅降级)。
func UsageDetailByRange(ctx context.Context, rangeKey string) (detail UsageDetail, err error) {
	gormDB := db.GetDB()
	if gormDB == nil {
		return UsageDetail{}, nil
	}
	window, ok := usageWindows[rangeKey]
	if !ok {
		rangeKey = "forever"
		window = usageWindows[rangeKey]
	}
	var row struct {
		Input     int64
		Output    int64
		Reasoning int64
		Cached    int64
		Cost      float64
		Duration  int64
		ReqCnt    int64
	}
	sql := "SELECT COALESCE(SUM(input_tokens), 0) AS input, COALESCE(SUM(output_tokens), 0) AS output, " +
		"COALESCE(SUM(reasoning_tokens), 0) AS reasoning, COALESCE(SUM(cached_tokens), 0) AS cached, " +
		"COALESCE(SUM(cost), 0) AS cost, COALESCE(SUM(duration_ms), 0) AS duration, " +
		"COALESCE(SUM(request_count), 0) AS req_cnt FROM usage_buckets"
	var args []any
	if rangeKey != "forever" {
		since := usageBucketNow().UTC().Truncate(time.Hour).Add(-window.span)
		sql += " WHERE bucket_at >= ?"
		args = append(args, since)
	} else if cutoff := usageRetentionCutoff(); !cutoff.IsZero() {
		sql += " WHERE bucket_at >= ?"
		args = append(args, cutoff)
	}
	err = gormDB.WithContext(ctx).Raw(sql, args...).Scan(&row).Error
	if err != nil {
		log.Warnf("aggregate usage detail by range failed: %v", err)
		return UsageDetail{}, err
	}
	return UsageDetail{
		InputTokens:     row.Input,
		OutputTokens:    row.Output,
		ReasoningTokens: row.Reasoning,
		CachedTokens:    row.Cached,
		Cost:            row.Cost,
		DurationMs:      row.Duration,
		RequestCount:    row.ReqCnt,
	}, nil
}

// UsageDetail 详细指标汇总行, 供仪表盘详细指标卡片与 API 响应使用。
type UsageDetail struct {
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	Cost            float64 `json:"cost"`
	DurationMs      int64   `json:"duration_ms"`
	RequestCount    int64   `json:"request_count"`
}

// UsageHeatmapPoint 热力图单点: date 为 UTC 日期(YYYY-MM-DD), tokens 为该日全部模型
// 的 input+output 合计, cost 为该日预计消耗, count 为该日请求数。
type UsageHeatmapPoint struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
	Count  int64   `json:"count"`
}

// UsageHeatmapData 返回最近 days 天的每日用量汇总, 供仪表盘 GitHub 风格热力图使用。
// 按 UTC 日期分桶聚合, 无数据的日期输出零值点, 保证热力图日期连续。
// days 默认 365, 上限 730(两年), 下限 7。DB 查询失败返回空切片与非 nil error;
// 数据库未初始化返回空切片与 nil error(优雅降级)。
func UsageHeatmapData(ctx context.Context, days int) ([]UsageHeatmapPoint, error) {
	if days < 7 {
		days = 7
	}
	if days > 730 {
		days = 730
	}
	gormDB := db.GetDB()
	if gormDB == nil {
		return make([]UsageHeatmapPoint, 0), nil
	}

	now := usageBucketNow().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(days - 1))

	// 查询窗口内的分桶, 按日期聚合
	var rows []struct {
		Date   string
		Tokens int64
		Cost   float64
		Count  int64
	}
	err := gormDB.WithContext(ctx).Raw(
		`SELECT DATE(bucket_at) AS date,
		        COALESCE(SUM(input_tokens + output_tokens), 0) AS tokens,
		        COALESCE(SUM(cost), 0) AS cost,
		        COALESCE(SUM(request_count), 0) AS count
		 FROM usage_buckets
		 WHERE bucket_at >= ?
		 GROUP BY DATE(bucket_at)
		 ORDER BY date ASC`,
		start,
	).Scan(&rows).Error
	if err != nil {
		log.Warnf("query usage heatmap failed: %v", err)
		return make([]UsageHeatmapPoint, 0), err
	}

	// 构建日期 → 数据映射, 填充无数据日期为零值
	byDate := make(map[string]UsageHeatmapPoint, len(rows))
	for _, r := range rows {
		byDate[r.Date] = UsageHeatmapPoint{
			Date:   r.Date,
			Tokens: r.Tokens,
			Cost:   r.Cost,
			Count:  r.Count,
		}
	}

	points := make([]UsageHeatmapPoint, 0, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		if p, ok := byDate[d]; ok {
			points = append(points, p)
		} else {
			points = append(points, UsageHeatmapPoint{Date: d})
		}
	}
	return points, nil
}
