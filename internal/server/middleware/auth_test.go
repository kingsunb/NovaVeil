package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/keylimit"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/auth"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/testutil"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	if err := op.InitCache(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// seedUser 预置已知密码的管理员行并加载进 op 缓存
func seedUser(t *testing.T, password string, mustChange bool) {
	t.Helper()
	u := model.User{Username: "admin", Password: password, MustChangePassword: mustChange}
	if err := u.HashPassword(); err != nil {
		t.Fatalf("hash: %v", err)
	}
	var count int64
	db.GetDB().Model(&model.User{}).Count(&count)
	if count == 0 {
		if err := db.GetDB().Create(&u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	} else {
		var stored model.User
		if err := db.GetDB().First(&stored).Error; err != nil {
			t.Fatalf("load user: %v", err)
		}
		stored.Password = u.Password
		stored.MustChangePassword = mustChange
		if err := db.GetDB().Save(&stored).Error; err != nil {
			t.Fatalf("save user: %v", err)
		}
	}
	if err := op.UserInit(); err != nil {
		t.Fatalf("UserInit: %v", err)
	}
}

func newTestEngine() *gin.Engine {
	r := gin.New()
	r.Use(Auth())
	r.GET("/api/v1/user/status", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/api/v1/user/change-password", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/api/v1/channel/list", func(c *gin.Context) { c.String(http.StatusOK, "channels") })
	return r
}

func doRequest(r *gin.Engine, method, path, cookieValue string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookieValue != "" {
		req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: cookieValue})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func cookieAttrs(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	header := w.Header().Get("Set-Cookie")
	if header == "" {
		t.Fatal("expected Set-Cookie header")
	}
	attrs := make(map[string]string)
	for i, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if i == 0 {
			kv := strings.SplitN(part, "=", 2)
			attrs["__name"] = kv[0]
			attrs["__value"] = kv[1]
			continue
		}
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			attrs[kv[0]] = kv[1]
		} else {
			attrs[part] = ""
		}
	}
	if attrs["__name"] != AuthCookieName {
		t.Fatalf("cookie name = %q, want %q", attrs["__name"], AuthCookieName)
	}
	if _, ok := attrs["Path"]; !ok {
		t.Fatal("cookie missing Path attribute")
	}
	if _, ok := attrs["HttpOnly"]; !ok {
		t.Fatal("cookie must be HttpOnly")
	}
	if sameSite, ok := attrs["SameSite"]; !ok || sameSite != "Lax" {
		t.Fatalf("cookie SameSite = %q, want Lax", sameSite)
	}
	return attrs
}

func TestSetAuthCookieAttributes(t *testing.T) {
	conf.AppConfig.Security.CookieSecure = false
	defer func() { conf.AppConfig.Security.CookieSecure = false }()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	SetAuthCookie(c, "token-value", 60)
	attrs := cookieAttrs(t, w)
	if attrs["__value"] != "token-value" {
		t.Fatalf("cookie value = %q", attrs["__value"])
	}
	if attrs["Max-Age"] != "60" {
		t.Fatalf("Max-Age = %q, want 60", attrs["Max-Age"])
	}
	if _, hasSecure := attrs["Secure"]; hasSecure {
		t.Fatal("Secure must be absent when security.cookie_secure = false")
	}

	conf.AppConfig.Security.CookieSecure = true
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	SetAuthCookie(c2, "token-value", 60)
	attrs = cookieAttrs(t, w2)
	if _, ok := attrs["Secure"]; !ok {
		t.Fatal("Secure must be present when security.cookie_secure = true")
	}

	// SEC-02: 直连 HTTP 但可信反代终止 TLS 并显式传入 X-Forwarded-Proto: https,
	// 认证 cookie 必须携带 Secure, 避免凭据经明文链路回传。
	conf.AppConfig.Security.CookieSecure = false
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	c3.Request.Header.Set("X-Forwarded-Proto", "https")
	SetAuthCookie(c3, "token-value", 60)
	attrs = cookieAttrs(t, w3)
	if _, ok := attrs["Secure"]; !ok {
		t.Fatal("Secure must be present when X-Forwarded-Proto is https")
	}

	// 普通 HTTP 无 X-Forwarded-Proto 时直接 HTTP 仍不带 Secure(保持部署兼容)。
	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	c4.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	SetAuthCookie(c4, "token-value", 60)
	attrs = cookieAttrs(t, w4)
	if _, hasSecure := attrs["Secure"]; hasSecure {
		t.Fatal("Secure must remain absent for direct HTTP without X-Forwarded-Proto")
	}
}

func TestSetAuthCookieSessionScope(t *testing.T) {
	conf.AppConfig.Security.CookieSecure = false
	defer func() { conf.AppConfig.Security.CookieSecure = false }()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	// remember=false 时 maxAge=0: net/http 省略 Max-Age, 渲染为会话 cookie, 关闭浏览器即失效。
	SetAuthCookie(c, "token-value", 0)
	attrs := cookieAttrs(t, w)
	if _, hasMaxAge := attrs["Max-Age"]; hasMaxAge {
		t.Fatalf("session cookie must omit Max-Age, got %q", attrs["Max-Age"])
	}
}

func TestClearAuthCookieExpiresSession(t *testing.T) {
	conf.AppConfig.Security.CookieSecure = false
	defer func() { conf.AppConfig.Security.CookieSecure = false }()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/status", nil)
	ClearAuthCookie(c)
	expectExpiredCookie(t, w)
}

// expectExpiredCookie 断言清除型 cookie(空值 + 过期 Max-Age, net/http 将负值渲染为 0)
func expectExpiredCookie(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	attrs := cookieAttrs(t, w)
	if attrs["Max-Age"] != "0" && attrs["Max-Age"] != "-1" {
		t.Fatalf("expired cookie Max-Age = %q, want 0", attrs["Max-Age"])
	}
	if attrs["__value"] != "" {
		t.Fatalf("cleared cookie should carry empty value, got %q", attrs["__value"])
	}
	return attrs
}

func TestAuthRejectsMissingAndInvalidToken(t *testing.T) {
	seedUser(t, "password-1", false)
	r := newTestEngine()

	w := doRequest(r, http.MethodGet, "/api/v1/channel/list", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie -> %d, want 401", w.Code)
	}

	// 无效 token: 401 且清除 cookie 的同时携带加固属性
	w = doRequest(r, http.MethodGet, "/api/v1/channel/list", "invalid-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token -> %d, want 401", w.Code)
	}
	expectExpiredCookie(t, w)
}

func TestMustChangePasswordWhitelist(t *testing.T) {
	seedUser(t, "password-2", true)
	token, _, err := auth.GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken: %v", err)
	}
	r := newTestEngine()

	// 白名单路径放行
	if w := doRequest(r, http.MethodGet, "/api/v1/user/status", token); w.Code != http.StatusOK {
		t.Fatalf("status under flag -> %d, want 200", w.Code)
	}
	if w := doRequest(r, http.MethodPost, "/api/v1/user/change-password", token); w.Code != http.StatusOK {
		t.Fatalf("change-password under flag -> %d, want 200", w.Code)
	}

	// 其余路径 403, 且携带机器可读标记头: 前端改密引导按头判定而非 message 文案
	w := doRequest(r, http.MethodGet, "/api/v1/channel/list", token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("other path under flag -> %d, want 403", w.Code)
	}
	if got := w.Header().Get(resp.ErrMarkerHeader); got != resp.ErrMarkerPasswordChangeRequired {
		t.Fatalf("403 should carry machine-readable marker header, got %q", got)
	}
	if !strings.Contains(w.Body.String(), respMessageMustChangePassword) {
		t.Fatalf("403 body should explain password change requirement, got %s", w.Body.String())
	}
}

const respMessageMustChangePassword = "Password change required"

func TestAfterPasswordChangeAllPathsAllowedOldTokensInvalid(t *testing.T) {
	seedUser(t, "password-3", true)
	oldToken, _, err := auth.GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken: %v", err)
	}
	if err := op.UserChangePassword("password-3", "new-password-3"); err != nil {
		t.Fatalf("UserChangePassword: %v", err)
	}

	newToken, _, err := auth.GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken: %v", err)
	}
	r := newTestEngine()

	// 轮换后旧 token 失效
	if w := doRequest(r, http.MethodGet, "/api/v1/user/status", oldToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation -> %d, want 401", w.Code)
	}
	// 新 token 全站放行
	if w := doRequest(r, http.MethodGet, "/api/v1/channel/list", newToken); w.Code != http.StatusOK {
		t.Fatalf("protected path after change -> %d, want 200", w.Code)
	}
}

// seedAPIKey 预置一把启用的下游密钥并加载进 op 缓存, 返回明文 key 值。
func seedAPIKey(t *testing.T, value string, maxConcurrent, rateLimitRPM int) string {
	t.Helper()
	key := model.APIKey{
		Name:          "limit-test-" + value,
		APIKey:        value,
		Enabled:       true,
		MaxConcurrent: maxConcurrent,
		RateLimitRPM:  rateLimitRPM,
	}
	if err := op.APIKeyCreate(&key, t.Context()); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	return value
}

func newAPIKeyTestEngine(handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(APIKeyAuth())
	r.POST("/v1/chat/completions", handler)
	return r
}

func doKeyRequest(r *gin.Engine, keyValue string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+keyValue)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAPIKeyAuthRPMLimitFailFast(t *testing.T) {
	keylimit.Reset()
	t.Cleanup(keylimit.Reset)
	keyValue := seedAPIKey(t, "sk-rpm-limit-test", 0, 1)
	r := newAPIKeyTestEngine(func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	if w := doKeyRequest(r, keyValue); w.Code != http.StatusOK {
		t.Fatalf("first request -> %d, want 200", w.Code)
	}
	// 窗口内第二次: fail-fast 429 + Retry-After, 客户端按头退避而不是排队。
	w := doKeyRequest(r, keyValue)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request within window -> %d, want 429", w.Code)
	}
	retryAfter := w.Header().Get("Retry-After")
	seconds, parseErr := strconv.Atoi(retryAfter)
	if parseErr != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want >=1 integer", retryAfter)
	}
	if !strings.Contains(w.Body.String(), resp.ErrAPIKeyRateLimited) {
		t.Fatalf("429 body should explain rate limit, got %s", w.Body.String())
	}
}

func TestAPIKeyAuthConcurrencyLimitFailFast(t *testing.T) {
	keylimit.Reset()
	t.Cleanup(keylimit.Reset)
	keyValue := seedAPIKey(t, "sk-concurrency-limit-test", 1, 0)

	entered := make(chan struct{})
	block := make(chan struct{})
	var once sync.Once
	r := newAPIKeyTestEngine(func(c *gin.Context) {
		once.Do(func() { close(entered) })
		<-block
		c.String(http.StatusOK, "ok")
	})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- doKeyRequest(r, keyValue)
	}()
	<-entered // 第一个请求已在 handler 内持有唯一并发槽位

	// 并发已满: fail-fast 429, 不等待槽位。
	w := doKeyRequest(r, keyValue)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("request while slot held -> %d, want 429", w.Code)
	}
	if retryAfter := w.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("429 must carry Retry-After header")
	}
	if !strings.Contains(w.Body.String(), resp.ErrAPIKeyConcurrencyFull) {
		t.Fatalf("429 body should explain concurrency limit, got %s", w.Body.String())
	}

	// 请求完成后槽位归还, 后续请求恢复放行。
	close(block)
	if w := <-firstDone; w.Code != http.StatusOK {
		t.Fatalf("first request -> %d, want 200", w.Code)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if w := doKeyRequest(r, keyValue); w.Code == http.StatusOK {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("request after slot release -> %d, want 200", w.Code)
		}
	}
}

func TestAPIKeyAuthUnlimitedPassesRepeatedly(t *testing.T) {
	keylimit.Reset()
	t.Cleanup(keylimit.Reset)
	keyValue := seedAPIKey(t, "sk-unlimited-test", 0, 0)
	r := newAPIKeyTestEngine(func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// 0 = 不限: 反复请求全部放行。
	for range 5 {
		if w := doKeyRequest(r, keyValue); w.Code != http.StatusOK {
			t.Fatalf("unlimited request -> %d, want 200", w.Code)
		}
	}
}

// TestAPIKeyAuthOpenAIErrorFormat 验证 /v1/ 路由鉴权失败时返回 OpenAI 兼容错误格式,
// 而非管理端 {code,message} 格式。回归 /v1/models 无 key 时返回非标准错误的问题。
func TestAPIKeyAuthOpenAIErrorFormat(t *testing.T) {
	keylimit.Reset()
	t.Cleanup(keylimit.Reset)
	r := newAPIKeyTestEngine(func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// 1) 无令牌
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no key -> %d, want 401", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{`"error"`, `"type":"novaveil_error"`, `"message"`, "request id"} {
		if !strings.Contains(body, want) {
			t.Fatalf("no-key error body missing %q, got: %s", want, body)
		}
	}
	// 不应包含管理端格式字段 "code":401
	if strings.Contains(body, `"code":401`) {
		t.Fatalf("no-key error should not use management format {code,message}, got: %s", body)
	}

	// 2) 无效令牌
	w = doKeyRequest(r, "sk-invalid-key-that-does-not-exist")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key -> %d, want 401", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, `"type":"novaveil_error"`) {
		t.Fatalf("invalid key error should be OpenAI format, got: %s", body)
	}
	if !strings.Contains(body, "无效的令牌") {
		t.Fatalf("invalid key error should say 无效的令牌, got: %s", body)
	}

	// 3) X-Request-Id 头应存在
	if rid := w.Header().Get("X-Request-Id"); rid == "" {
		t.Fatal("X-Request-Id header should be set for relay errors")
	}
}
