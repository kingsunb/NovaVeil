package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/keylimit"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/auth"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
)

const (
	// AuthCookieName 认证 cookie 名称
	AuthCookieName = "auth"
	// AuthCookiePath 认证 cookie 作用路径
	AuthCookiePath = "/"
)

// mustChangePasswordWhitelist 标记未清除时(必须修改密码)仍可访问的路径
var mustChangePasswordWhitelist = map[string]bool{
	"/api/v1/user/change-password": true,
	"/api/v1/user/logout":          true,
	"/api/v1/user/status":          true,
}

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(AuthCookieName)
		if err != nil || token == "" {
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}
		if !auth.VerifyJWTToken(token) {
			ClearAuthCookie(c)
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}
		if op.UserGet().MustChangePassword && !mustChangePasswordWhitelist[c.Request.URL.Path] {
			// 带机器可读标记头: 前端改密引导按头判定, 不再耦合 message 文案。
			resp.ErrorMustChangePassword(c)
			c.Abort()
			return
		}
		// 管理 API 限速在鉴权成功后生效; 登录接口不经 Auth, 不受此限(审计 OLD-26)。
		adminRateLimit(c)
	}
}

// SetAuthCookie 设置认证 cookie, 统一 SameSite/HttpOnly/Secure 属性
func SetAuthCookie(c *gin.Context, value string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	// 请求本身走 TLS, 或可信反代已终止 TLS 并显式传入 X-Forwarded-Proto: https 时,
	// Secure=true: 客户端与反代之间已是 HTTPS 通道, 漏开 Secure 会导致凭据经明文链路回传。
	// 直连部署不配置反代时请求头可被客户端任意伪造, 但此处只可能把 cookie 设得更严格,
	// 不会弱于现状; 真正信任 XFF/代理头仍以 conf.Server.TrustedProxies 为准。
	secure := conf.AppConfig.Security.CookieSecure || c.Request.TLS != nil ||
		strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
	c.SetCookie(AuthCookieName, value, maxAge, AuthCookiePath, "", secure, true)
}

// ClearAuthCookie 清除认证 cookie
func ClearAuthCookie(c *gin.Context) {
	SetAuthCookie(c, "", -1)
}

func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		var apiKey string

		if key := c.Request.Header.Get("x-api-key"); key != "" {
			apiKey = key
		} else if authorization := c.Request.Header.Get("Authorization"); authorization != "" {
			// 只接受 Bearer scheme: 非 Bearer(如 Basic/自定义 scheme)按 401 拒绝,
			// 而不是把 scheme 一起塞进 API Key 查询(审计 SEC-06)。
			parts := strings.Fields(authorization)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
				resp.RelayError(c, http.StatusUnauthorized, "不支持的 Authorization scheme，仅接受 Bearer <token>")
				return
			}
			apiKey = parts[1]
		}

		if apiKey == "" {
			resp.RelayError(c, http.StatusUnauthorized, "无可用令牌，请确认是否已登录或令牌是否正确")
			return
		}
		apiKeyObj, err := op.APIKeyGetByAPIKey(apiKey, c.Request.Context())
		if err != nil {
			resp.RelayError(c, http.StatusUnauthorized, "无效的令牌")
			return
		}
		if !apiKeyObj.Enabled {
			resp.RelayError(c, http.StatusUnauthorized, "令牌已禁用")
			return
		}
		if apiKeyObj.ExpireAt > 0 && apiKeyObj.ExpireAt < time.Now().Unix() {
			resp.RelayError(c, http.StatusUnauthorized, "令牌已过期")
			return
		}
		// 密钥级限速(fail-fast): 进入业务 handler 前判定, 超限立即 429 + Retry-After,
		// 由客户端按头退避, 不做内部排队(阻塞不可信客户端只会占住资源放大压力)。
		// 并发槽位覆盖整个请求生命周期(含流式泵送), defer 在本中间件返回时归还,
		// panic 展开同样会归还; RPM 名额是窗口内的一次放行记录, 无需归还。
		// 与渠道级限速(relay/channellimit)独立叠加生效, 实际吞吐取两者较小值。
		releaseConcurrency, err := keylimit.AcquireConcurrency(apiKeyObj.ID, apiKeyObj.MaxConcurrent)
		if err != nil {
			c.Header("Retry-After", "1")
			resp.RelayError(c, http.StatusTooManyRequests, resp.ErrAPIKeyConcurrencyFull)
			return
		}
		defer releaseConcurrency()
		retryAfter, err := keylimit.AcquireRPMPermit(apiKeyObj.ID, apiKeyObj.RateLimitRPM)
		if err != nil {
			if retryAfter < 1 {
				retryAfter = 1
			}
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			resp.RelayError(c, http.StatusTooManyRequests, resp.ErrAPIKeyRateLimited)
			return
		}
		c.Set("supported_models", apiKeyObj.SupportedModels)
		c.Set("api_key_raw", apiKeyObj.APIKey)
		c.Set("api_key_name", apiKeyObj.Name)
		c.Set("api_key_id", apiKeyObj.ID)
		// 异步记录最后使用时间, 不阻塞请求、不影响鉴权决策。
		op.APIKeyTouchLastUsed(apiKeyObj.ID)
		c.Next()
	}
}
