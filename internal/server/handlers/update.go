package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/stats").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/now-version", http.MethodGet).
				Handle(getNowVersion),
		).
		AddRoute(
			router.NewRoute("/build-info", http.MethodGet).
				Handle(getBuildInfo),
		).
		AddRoute(
			router.NewRoute("/token-trends", http.MethodGet).
				Handle(getTokenTrends),
		).
		AddRoute(
			router.NewRoute("/usage-detail", http.MethodGet).
				Handle(getUsageDetail),
		).
		AddRoute(
			router.NewRoute("/usage-heatmap", http.MethodGet).
				Handle(getUsageHeatmap),
		)
}

func getNowVersion(c *gin.Context) {
	// 统计查询使用独立超时上下文: 请求上下文随客户端断开取消, 不应中断数据库聚合。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// range 联动 KPI 口径: 默认 forever(全量), 与趋势图默认档位一致。
	// 选中某档位时, 四个 KPI 分别统计该时间窗口内的请求数 / 活跃客户端 IP / 错误数 / Token 用量。
	rangeKey := c.Query("range")
	if rangeKey == "" {
		rangeKey = "forever"
	}
	if !op.ValidUsageRange(rangeKey) {
		resp.Error(c, http.StatusBadRequest, "range 仅支持 24h / 7d / 30d / 1y / 3y / forever")
		return
	}
	forever := rangeKey == "forever"
	since := op.UsageWindowSince(rangeKey) // forever 时为零值, 表示全量

	// total_requests + Token 用量共用一次 usage_buckets 聚合:
	//   - forever: total_requests 用进程累计全量(relay.TotalRequestCount), token 用 UsageTotals 全表聚合;
	//   - 非 forever: 二者均取自 UsageKPIsByRange 的窗口聚合, total_requests 用 request_count 合计
	//     (仅计有用量上报的请求, 是窗口内请求数的近似)。
	var totalRequests uint64
	var kpiInput, kpiOutput int64
	var kpiErr error
	if forever {
		totalRequests = relay.TotalRequestCount()
		kpiInput, kpiOutput, kpiErr = op.UsageTotals(ctx)
	} else {
		var reqCnt int64
		kpiInput, kpiOutput, reqCnt, kpiErr = op.UsageKPIsByRange(ctx, rangeKey)
		totalRequests = uint64(reqCnt)
	}
	if kpiErr != nil {
		log.Warnf("usage KPI stats unavailable: %v", kpiErr)
	}

	// client_ip_count: forever 用内存缓存去重数(含本进程未刷增量, 与历史行为一致);
	// 非 forever 按 last_seen >= since 查 client_stats 表(跨重启的权威来源)。
	var clientIPCount int64
	if forever {
		clientIPCount = int64(op.ClientStatCount())
	} else {
		cnt, err := op.ClientStatCountSince(ctx, since)
		if err != nil {
			log.Warnf("failed to count client ips by range: %v", err)
		}
		clientIPCount = cnt
	}

	// error_count: forever 用进程级业务错误计数; 非 forever 用 usage_buckets.error_count
	// (与错误日志保留条数解耦, 按小时分桶, 随档位联动)。
	var errorCount int64
	if forever {
		errorCount = int64(relay.TotalErrorCount())
	} else {
		cnt, err := op.UsageErrorCountByRange(ctx, rangeKey)
		if err != nil {
			log.Warnf("failed to count errors by range: %v", err)
		}
		errorCount = cnt
	}

	tokensByModel, byModelErr := op.UsageTotalsByModelRange(ctx, rangeKey)
	detail, detailErr := op.UsageDetailByRange(ctx, rangeKey)
	// stats_available 标识用量统计是否可读: KPI 聚合与按模型聚合任一失败即置 false,
	// 让前端区分"无流量"(true 且全零)与"统计暂时不可用"(false)。
	statsAvailable := kpiErr == nil && byModelErr == nil && detailErr == nil
	if !statsAvailable {
		log.Warnf("usage stats unavailable: kpi=%v byModel=%v detail=%v", kpiErr, byModelErr, detailErr)
	}
	resp.Success(c, gin.H{
		"version":             conf.Version,
		"commit":              conf.Commit,
		"build_time":          conf.BuildTime,
		"client_ip_count":     clientIPCount,
		"total_requests":      totalRequests,
		"error_count":         errorCount,
		"total_tokens_input":  kpiInput,
		"total_tokens_output": kpiOutput,
		"tokens_by_model":     tokensByModel,
		"stats_available":     statsAvailable,
		// 详细指标: reasoning/cache token、预计消耗、使用时长
		"reasoning_tokens":    detail.ReasoningTokens,
		"cached_tokens":       detail.CachedTokens,
		"total_cost":          detail.Cost,
		"total_duration_ms":   detail.DurationMs,
	})
}

// getBuildInfo 返回当前二进制的构建元信息（version/commit/build_time）。
// 只读 ldflags 注入的 conf 常量，不做任何数据库聚合 —— 前端版本看门狗用它
// 做高频轮询，判定浏览器里跑的旧前端与后端是否已错位（错位则提示强制刷新）；
// 这类轮询不能复用 now-version，否则每次都会触发全表统计聚合。
func getBuildInfo(c *gin.Context) {
	resp.Success(c, gin.H{
		"version":    conf.Version,
		"commit":     conf.Commit,
		"build_time": conf.BuildTime,
	})
}

// getTokenTrends 返回 Token 用量时间序列: /token-trends?range=24h|7d|30d|1y|3y|forever。
// 档位非法回 400; 返回形状 {points:[{t,in,out}], available:bool}, t 为采样区间起点 Unix 毫秒,
// 点数固定 24/28/30/52/36/120, 窗口内无数据的点输出 0 保证 X 轴连续。
// available 为 false 表示统计暂时不可用(DB 查询失败), 此时 points 仍为零值时间轴;
// 为 true 表示统计可读(可能全零=无流量), 前端据此区分两种状态。
func getTokenTrends(c *gin.Context) {
	rangeKey := c.Query("range")
	if rangeKey == "" {
		rangeKey = "forever"
	}
	if !op.ValidUsageRange(rangeKey) {
		resp.Error(c, http.StatusBadRequest, "range 仅支持 24h / 7d / 30d / 1y / 3y / forever")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	points, err := op.UsageTrendPoints(ctx, rangeKey)
	available := err == nil
	if !available {
		log.Warnf("usage trend stats unavailable: %v", err)
	}
	resp.Success(c, gin.H{"points": points, "available": available})
}

// getUsageDetail 返回指定时间窗口的详细指标: /usage-detail?range=24h|7d|30d|1y|3y|forever。
// 包含 input/output/reasoning/cached token、预计消耗(cost)、使用时长(duration_ms)与请求计数。
func getUsageDetail(c *gin.Context) {
	rangeKey := c.Query("range")
	if rangeKey == "" {
		rangeKey = "forever"
	}
	if !op.ValidUsageRange(rangeKey) {
		resp.Error(c, http.StatusBadRequest, "range 仅支持 24h / 7d / 30d / 1y / 3y / forever")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	detail, err := op.UsageDetailByRange(ctx, rangeKey)
	available := err == nil
	if !available {
		log.Warnf("usage detail stats unavailable: %v", err)
	}
	resp.Success(c, gin.H{"detail": detail, "available": available})
}

// getUsageHeatmap 返回每日用量汇总: /usage-heatmap?days=365。
// 供仪表盘 GitHub 风格热力图使用, days 默认 365, 范围 [7, 730]。
func getUsageHeatmap(c *gin.Context) {
	days := 365
	if d := c.Query("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			days = n
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	points, err := op.UsageHeatmapData(ctx, days)
	available := err == nil
	if !available {
		log.Warnf("usage heatmap stats unavailable: %v", err)
	}
	resp.Success(c, gin.H{"points": points, "available": available})
}
