package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestLimiterDB 为单个用例创建独立的临时 sqlite 连接并迁移限速表。
func newTestLimiterDB(t *testing.T) *gorm.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "loginrl-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	conn, err := gorm.Open(sqlite.Open(filepath.Join(dir, "test.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := conn.AutoMigrate(&model.LoginAttempt{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

func newTestLimiter(conn *gorm.DB, window time.Duration, maxFails int, now *time.Time) *loginRateLimiter {
	l := newLoginRateLimiter(window, maxFails)
	l.conn = conn
	l.now = func() time.Time { return *now }
	return l
}

func advance(now *time.Time, d time.Duration) {
	*now = now.Add(d)
}

func countAttempts(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := conn.Model(&model.LoginAttempt{}).Count(&n).Error; err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return n
}

func TestRateLimitBlocksAtThreshold(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	now := base
	l := newTestLimiter(newTestLimiterDB(t), 15*time.Minute, 5, &now)

	const ip = "203.0.113.10"

	// 4 次失败仍在阈值内
	for i := 0; i < 4; i++ {
		if allowed, _ := l.check(context.Background(), ip); !allowed {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.recordFailure(context.Background(), ip)
	}
	if allowed, _ := l.check(context.Background(), ip); !allowed {
		t.Fatal("5th attempt should still be allowed before 5th failure")
	}
	l.recordFailure(context.Background(), ip)

	// 5 次失败后拒绝并给出 Retry-After(全部失败发生在注入时钟的同一时刻, 等待时长应等于整个窗口)
	allowed, retryAfter := l.check(context.Background(), ip)
	if allowed {
		t.Fatal("must be blocked after 5 failures")
	}
	if retryAfter != 15*time.Minute {
		t.Fatalf("retryAfter = %v, want %v", retryAfter, 15*time.Minute)
	}
}

func TestRateLimitSlidingWindowExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	l := newTestLimiter(newTestLimiterDB(t), 50*time.Millisecond, 2, &now)

	const ip = "203.0.113.11"
	l.recordFailure(context.Background(), ip)
	advance(&now, 30*time.Millisecond)
	l.recordFailure(context.Background(), ip)

	// 第 1 次失败尚未滑出窗口 -> 阻断
	if allowed, _ := l.check(context.Background(), ip); allowed {
		t.Fatal("should be blocked while both failures are inside window")
	}

	// 越过第一次失败的时间点后, 仅剩一次失败 -> 放行
	advance(&now, 21*time.Millisecond)
	if allowed, _ := l.check(context.Background(), ip); !allowed {
		t.Fatal("should be allowed after oldest failure slides out of window")
	}
}

func TestRateLimitResetOnSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	conn := newTestLimiterDB(t)
	l := newTestLimiter(conn, time.Minute, 3, &now)

	const ip = "203.0.113.12"
	for i := 0; i < 3; i++ {
		l.recordFailure(context.Background(), ip)
	}
	if got := countAttempts(t, conn); got != 3 {
		t.Fatalf("failure rows = %d, want 3", got)
	}
	if allowed, _ := l.check(context.Background(), ip); allowed {
		t.Fatal("should be blocked at threshold")
	}
	l.reset(context.Background(), ip)
	if got := countAttempts(t, conn); got != 0 {
		t.Fatalf("rows after reset = %d, want 0", got)
	}
	if allowed, _ := l.check(context.Background(), ip); !allowed {
		t.Fatal("successful login must clear failure counter")
	}
}

func TestRateLimitIsolatesIPsAndSweepsExpiredRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	conn := newTestLimiterDB(t)
	l := newTestLimiter(conn, time.Minute, 2, &now)

	blockedIP := "203.0.113.13"
	otherIP := "203.0.113.14"
	l.recordFailure(context.Background(), blockedIP)
	l.recordFailure(context.Background(), blockedIP)
	if allowed, _ := l.check(context.Background(), otherIP); !allowed {
		t.Fatal("failure counting must be isolated per IP")
	}
	if allowed, _ := l.check(context.Background(), blockedIP); allowed {
		t.Fatal("blocked IP must stay blocked")
	}

	// 窗口整体过期后触发惰性清理(时钟已推进超过 window/4): 过期行被删除, 该 IP 恢复可登录。
	advance(&now, 2*time.Minute)
	l.recordFailure(context.Background(), otherIP) // 触发全表惰性清理并写入一条新失败记录
	if got := countAttempts(t, conn); got != 1 {
		t.Fatalf("expired rows must be swept, remaining = %d, want only the fresh one", got)
	}
	if allowed, _ := l.check(context.Background(), blockedIP); !allowed {
		t.Fatal("expired entries must unblock the IP")
	}
	var remaining model.LoginAttempt
	if err := conn.First(&remaining).Error; err != nil || remaining.IP != otherIP {
		t.Fatalf("sweeper must keep only fresh row, got %+v err=%v", remaining, err)
	}
}

func TestRejectRateLimitedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)

	rejectRateLimited(c, 90*time.Second)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	retryAfter := w.Header().Get("Retry-After")
	if got, err := strconv.Atoi(retryAfter); err != nil || got != 90 {
		t.Fatalf("Retry-After = %q, want 90", retryAfter)
	}
	if w.Body.String() == "" {
		t.Fatal("429 response body must not be empty")
	}
}
