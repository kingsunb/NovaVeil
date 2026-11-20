package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/server/auth"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
)

func newLoginTestRouter() *gin.Engine {
	r := gin.New()
	r.POST("/api/v1/user/login", middleware.LoginBodyLimit(), login)
	return r
}

func TestLoginMalformedJSONCountsTowardLimit(t *testing.T) {
	limiter := useCredentialTestLimiter(t)
	r := newLoginTestRouter()

	const ip = "203.0.113.60"
	body := `{"username":`
	for i := 0; i < loginMaxFailures; i++ {
		w := postCredentialJSON(t, r, "/api/v1/user/login", ip, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400", i+1, w.Code)
		}
	}
	if got := countAttempts(t, limiter.conn); got != int64(loginMaxFailures) {
		t.Fatalf("malformed JSON rows = %d, want %d", got, loginMaxFailures)
	}
	w := postCredentialJSON(t, r, "/api/v1/user/login", ip, body)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status after %d malformed JSON = %d, want 429", loginMaxFailures, w.Code)
	}
}

func TestLoginOversizedBodyCountsTowardLimit(t *testing.T) {
	limiter := useCredentialTestLimiter(t)
	r := newLoginTestRouter()

	const ip = "203.0.113.61"
	body := `{"username":"admin","password":"` + strings.Repeat("a", 64*1024) + `"}`
	w := postCredentialJSON(t, r, "/api/v1/user/login", ip, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 400", w.Code)
	}
	if got := countAttempts(t, limiter.conn); got != 1 {
		t.Fatalf("oversized body rows = %d, want 1", got)
	}
}

func TestLoginSuccessResetsFailures(t *testing.T) {
	seedAdminUser(t, "old-password-123")
	limiter := useCredentialTestLimiter(t)

	const ip = "203.0.113.62"
	limiter.recordFailure(context.Background(), ip)
	limiter.recordFailure(context.Background(), ip)

	r := newLoginTestRouter()
	w := postCredentialJSON(t, r, "/api/v1/user/login", ip, `{"username":"admin","password":"old-password-123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("valid login status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := countAttempts(t, limiter.conn); got != 0 {
		t.Fatalf("failure rows after login success = %d, want 0", got)
	}
	found := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == middleware.AuthCookieName {
			found = true
			if !auth.VerifyJWTToken(cookie.Value) {
				t.Fatal("successful login must issue a valid token")
			}
		}
	}
	if !found {
		t.Fatal("successful login must set the auth cookie")
	}
}

func TestLoginWrongCredentialsCountTowardLimit(t *testing.T) {
	seedAdminUser(t, "correct-password-123")
	limiter := useCredentialTestLimiter(t)
	r := newLoginTestRouter()
	const ip = "203.0.113.63"
	for _, body := range []string{
		`{"username":"admin","password":"wrong-password"}`,
		`{"username":"wrong-user","password":"correct-password-123"}`,
	} {
		w := postCredentialJSON(t, r, "/api/v1/user/login", ip, body)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid credentials status = %d, want 401", w.Code)
		}
		if w.Header().Get("Set-Cookie") != "" {
			t.Fatal("invalid credentials must not set an auth cookie")
		}
	}
	if got := countAttempts(t, limiter.conn); got != 2 {
		t.Fatalf("credential failure rows = %d, want 2", got)
	}
}
