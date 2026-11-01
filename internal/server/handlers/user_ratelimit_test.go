package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// seedAdminUser 预置已知密码的管理员并载入 op 用户缓存。
func seedAdminUser(t *testing.T, password string) {
	t.Helper()
	hashed := model.User{Username: "admin", Password: password}
	if err := hashed.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		if err := db.GetDB().Create(&hashed).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	} else {
		stored.Password = hashed.Password
		stored.MustChangePassword = false
		if err := db.GetDB().Save(&stored).Error; err != nil {
			t.Fatalf("save user: %v", err)
		}
	}
	if err := op.UserInit(t.TempDir()); err != nil {
		t.Fatalf("init user cache: %v", err)
	}
}

// useCredentialTestLimiter 替换全局限速器, 用例结束恢复。
func useCredentialTestLimiter(t *testing.T) *loginRateLimiter {
	t.Helper()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	limiter := newTestLimiter(newTestLimiterDB(t), loginFailWindow, loginMaxFailures, &now)
	old := loginLimiter
	loginLimiter = limiter
	t.Cleanup(func() { loginLimiter = old })
	return limiter
}

func postCredentialJSON(t *testing.T, r http.Handler, path, ip, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestChangePasswordWrongOldPasswordCountsTowardLoginLimit(t *testing.T) {
	seedAdminUser(t, "old-password-123")
	useCredentialTestLimiter(t)

	r := gin.New()
	r.POST("/api/v1/user/change-password", changePassword)

	const ip = "203.0.113.50"
	body := `{"old_password":"wrong-password","new_password":"new-password-123"}`
	for i := 0; i < loginMaxFailures; i++ {
		w := postCredentialJSON(t, r, "/api/v1/user/change-password", ip, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400", i+1, w.Code)
		}
	}

	w := postCredentialJSON(t, r, "/api/v1/user/change-password", ip, body)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status after %d failures = %d, want 429", loginMaxFailures, w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}

	allowed, _ := loginLimiter.check(context.Background(), ip)
	if !allowed {
		t.Fatal("credential verify failures must not block the login namespace")
	}
}

func TestChangeUsernameWrongPasswordCountsTowardLoginLimit(t *testing.T) {
	seedAdminUser(t, "old-password-123")
	useCredentialTestLimiter(t)

	r := gin.New()
	r.POST("/api/v1/user/change-username", changeUsername)

	const ip = "203.0.113.51"
	body := `{"new_username":"renamed-admin","password":"wrong-password"}`
	for i := 0; i < loginMaxFailures; i++ {
		w := postCredentialJSON(t, r, "/api/v1/user/change-username", ip, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400", i+1, w.Code)
		}
	}

	w := postCredentialJSON(t, r, "/api/v1/user/change-username", ip, body)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status after %d failures = %d, want 429", loginMaxFailures, w.Code)
	}
}

func TestCredentialVerifySuccessResetsFailures(t *testing.T) {
	seedAdminUser(t, "old-password-123")
	limiter := useCredentialTestLimiter(t)

	const ip = "203.0.113.52"
	key := "credential-verify:" + ip
	limiter.recordFailure(context.Background(), key)
	limiter.recordFailure(context.Background(), key)
	if got := countAttempts(t, limiter.conn); got != 2 {
		t.Fatalf("failure rows = %d, want 2", got)
	}

	r := gin.New()
	r.POST("/api/v1/user/change-password", changePassword)
	body := `{"old_password":"old-password-123","new_password":"new-password-456"}`
	w := postCredentialJSON(t, r, "/api/v1/user/change-password", ip, body)
	if w.Code != http.StatusOK {
		t.Fatalf("valid change status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := countAttempts(t, limiter.conn); got != 0 {
		t.Fatalf("failure rows after success = %d, want 0", got)
	}
}
