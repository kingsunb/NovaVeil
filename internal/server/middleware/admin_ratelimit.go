package middleware

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"golang.org/x/time/rate"
)

const (
	// adminRateLimitMaxKeys 是 客户端 IP+路由 维度限速条目的容量上限。
	// 超出时淘汰半数最久未命中的条目, 与 relay clientIP 集合同一策略, 保证有界。
	adminRateLimitMaxKeys = 4096
)

var (
	adminRateLimitMu sync.Mutex
	adminRateLimits  = make(map[string]*adminRateEntry)
)

type adminRateEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// AdminRateLimit 返回管理 API 限速中间件, 供路由声明在 Auth 之后显式挂载;
// 目前 /api/v1 各分组统一经由 Auth 在鉴权成功后调用 adminRateLimit, 登录接口
// 不经由 Auth, 因此不在限速范围(审计 OLD-26)。
func AdminRateLimit() gin.HandlerFunc {
	return adminRateLimit
}

func adminRateLimit(c *gin.Context) {
	sec := conf.AppConfig.Security
	if !sec.AdminAPIRateLimitEnabled {
		c.Next()
		return
	}
	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}
	limiter := adminRateLimiterFor(c.ClientIP(), path, sec.AdminAPIRateLimitPerMinute, sec.AdminAPIRateLimitBurst)
	if !limiter.Allow() {
		resp.Error(c, http.StatusTooManyRequests, "admin API rate limit exceeded")
		c.Abort()
		return
	}
	c.Next()
}

func adminRateKey(ip, path string) string {
	return ip + "|" + path
}

func adminRateLimiterFor(ip, path string, perMinute float64, burst int) *rate.Limiter {
	now := time.Now()
	key := adminRateKey(ip, path)

	adminRateLimitMu.Lock()
	defer adminRateLimitMu.Unlock()

	if entry, ok := adminRateLimits[key]; ok {
		entry.lastSeen = now
		return entry.limiter
	}
	if len(adminRateLimits) >= adminRateLimitMaxKeys {
		adminRateEvictOldestLocked(len(adminRateLimits) / 2)
	}
	r := rate.Limit(perMinute / 60.0)
	if r <= 0 {
		r = rate.Inf
	}
	l := rate.NewLimiter(r, burst)
	adminRateLimits[key] = &adminRateEntry{limiter: l, lastSeen: now}
	return l
}

func adminRateEvictOldestLocked(n int) {
	if n <= 0 {
		return
	}
	type kv struct {
		key  string
		seen time.Time
	}
	entries := make([]kv, 0, len(adminRateLimits))
	for key, e := range adminRateLimits {
		entries = append(entries, kv{key: key, seen: e.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seen.Before(entries[j].seen) })
	if n > len(entries) {
		n = len(entries)
	}
	for _, e := range entries[:n] {
		delete(adminRateLimits, e.key)
	}
}
