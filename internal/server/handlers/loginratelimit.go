package handlers

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"gorm.io/gorm"
)

const (
	// loginFailWindow 登录失败计数的滑动窗口时长
	loginFailWindow = 15 * time.Minute
	// loginMaxFailures 滑动窗口内允许的最大失败次数, 达到后拒绝该 IP 登录
	loginMaxFailures = 5
	// loginRateLimitDBTimeout 单次限速数据库操作的上界, 避免慢查询拖死请求(审计 OLD-13)。
	loginRateLimitDBTimeout = 5 * time.Second
)

// loginLimiter 登录限速器: 失败计数持久化在 login_attempts 表中, 多副本部署共享同一份窗口计数。
var loginLimiter = newLoginRateLimiter(loginFailWindow, loginMaxFailures)

// loginRateLimiter 基于数据库的按客户端 IP 登录失败计数器:
// 失败时插入一行, 判定时统计窗口内该 IP 的行数, 达上限拒绝登录并给出 Retry-After;
// 过期行由各副本惰性清理(距上次全表清理超过 window/4 时执行)。
type loginRateLimiter struct {
	mu        sync.Mutex // 仅保护 lastSweep; 数据操作本身由 gorm 连接池保证并发安全
	conn      *gorm.DB   // 独立连接仅供测试注入, 为空时回退全局连接
	window    time.Duration
	maxFails  int
	now       func() time.Time
	lastSweep time.Time
}

func newLoginRateLimiter(window time.Duration, maxFails int) *loginRateLimiter {
	return &loginRateLimiter{
		window:   window,
		maxFails: maxFails,
		now:      time.Now,
	}
}

// dbConn 返回限速器使用的数据连接。
func (l *loginRateLimiter) dbConn() *gorm.DB {
	if l.conn != nil {
		return l.conn
	}
	return db.GetDB()
}

// check 返回该 IP 是否允许再次尝试登录; 不允许时同时返回建议等待时长(Retry-After)。
// 数据库查询失败时拒绝放行(fail-close): 限速计数依赖数据库, 故障时放行等于放大
// 限速失效风险; 返回 (false, 0) 由调用方按超限处理, 保证安全侧不因故障旁路。
func (l *loginRateLimiter) check(ctx context.Context, ip string) (bool, time.Duration) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, loginRateLimitDBTimeout)
	defer cancel()

	now := l.now()
	l.sweepIfNeeded(ctx, now)

	var attempts []model.LoginAttempt
	err := l.dbConn().WithContext(ctx).
		Where("ip = ? AND created_at > ?", ip, now.Add(-l.window)).
		Order("created_at ASC").
		Limit(l.maxFails).
		Find(&attempts).Error
	if err != nil {
		log.Errorf("login rate limit check error: %v", err)
		// fail-close: 限速器故障时不放行，交由调用方按超限拒绝。
		return false, 0
	}
	if len(attempts) >= l.maxFails {
		retryAfter := attempts[0].CreatedAt.Add(l.window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter
	}
	return true, 0
}

func (l *loginRateLimiter) recordFailure(ctx context.Context, ip string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, loginRateLimitDBTimeout)
	defer cancel()

	now := l.now()
	l.sweepIfNeeded(ctx, now)
	attempt := model.LoginAttempt{IP: ip, CreatedAt: now}
	if err := l.dbConn().WithContext(ctx).Create(&attempt).Error; err != nil {
		log.Errorf("login rate limit record error: %v", err)
	}
}

// reset 登录成功后清零该 IP 的失败计数。
func (l *loginRateLimiter) reset(ctx context.Context, ip string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, loginRateLimitDBTimeout)
	defer cancel()
	if err := l.dbConn().WithContext(ctx).Where("ip = ?", ip).Delete(&model.LoginAttempt{}).Error; err != nil {
		log.Errorf("login rate limit reset error: %v", err)
	}
}

// sweepIfNeeded 惰性全表清理: 距上次清扫超过 window/4 时删除全部窗口外过期行,
// 防止长期运行下表无限增长。清理失败仅记日志, 不影响本次判定。
func (l *loginRateLimiter) sweepIfNeeded(ctx context.Context, now time.Time) {
	l.mu.Lock()
	due := l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= l.window/4
	if due {
		l.lastSweep = now
	}
	l.mu.Unlock()
	if !due {
		return
	}
	if err := l.dbConn().WithContext(ctx).Where("created_at <= ?", now.Add(-l.window)).Delete(&model.LoginAttempt{}).Error; err != nil {
		log.Errorf("login rate limit sweep error: %v", err)
	}
}

// rejectRateLimited 写入 429 响应与 Retry-After 头
func rejectRateLimited(c *gin.Context, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	resp.Error(c, http.StatusTooManyRequests, resp.ErrTooManyLoginAttempts)
}
