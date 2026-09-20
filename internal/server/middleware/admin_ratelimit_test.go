package middleware

import (
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"net/http"
	"net/http/httptest"
)

func TestAdminRateLimiterPerKeyBucket(t *testing.T) {
	l := adminRateLimiterFor("203.0.113.7", "/api/v1/channels", 60, 1)
	if !l.Allow() {
		t.Fatal("first request must be allowed by burst bucket")
	}
	if l.Allow() {
		t.Fatal("second request must be denied after burst exhausted")
	}
	other := adminRateLimiterFor("203.0.113.8", "/api/v1/channels", 60, 1)
	if !other.Allow() {
		t.Fatal("different client IP must get an independent bucket")
	}
}

func TestAdminRateLimitMiddlewareDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sec := conf.AppConfig.Security
	t.Cleanup(func() { conf.AppConfig.Security = sec })

	conf.AppConfig.Security.AdminAPIRateLimitEnabled = false
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil)

	adminRateLimit(c)
	if w.Code != http.StatusOK {
		t.Fatalf("disabled middleware must pass through, code = %d", w.Code)
	}
}

func TestAdminRateLimitEntryEvictionBounded(t *testing.T) {
	for i := 0; i < adminRateLimitMaxKeys; i++ {
		adminRateLimits[adminRateKey("10.0.0.1", string(rune(i)))] = &adminRateEntry{limiter: nil, lastSeen: time.Now()}
	}
	adminRateEvictOldestLocked(adminRateLimitMaxKeys / 2)
	if len(adminRateLimits) >= adminRateLimitMaxKeys {
		t.Fatalf("eviction must shrink map below cap, len=%d", len(adminRateLimits))
	}
}

func TestAdminRateLimiterConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = adminRateLimiterFor("198.51.100.9", "/api/v1/groups", 120, 30)
		}(i)
	}
	wg.Wait()
	if adminRateLimiterFor("198.51.100.9", "/api/v1/groups", 120, 30) == nil {
		t.Fatal("concurrent limiter construction must stay available")
	}
}
