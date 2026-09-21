package handlers

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/auth"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/login", http.MethodPost).
				Handle(login),
		)
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/logout", http.MethodPost).
				Handle(logout),
		).
		AddRoute(
			router.NewRoute("/change-password", http.MethodPost).
				Handle(changePassword),
		).
		AddRoute(
			router.NewRoute("/change-username", http.MethodPost).
				Handle(changeUsername),
		).
		AddRoute(
			router.NewRoute("/status", http.MethodGet).
				Handle(status),
		)
}

func login(c *gin.Context) {
	ip := c.ClientIP()
	unlockIP := loginLimiter.lockIP(ip)
	defer unlockIP()
	if allowed, retryAfter := loginLimiter.check(c.Request.Context(), ip); !allowed {
		rejectRateLimited(c, retryAfter)
		return
	}
	var user model.UserLogin
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.UserVerify(user.Username, user.Password); err != nil {
		loginLimiter.recordFailure(c.Request.Context(), ip)
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	// SEC-08/S-L6: 首次登录成功即删除一次性初始密码文件; 删除失败(极少数权限/IO
	// 错误)只记日志, 不阻断本轮登录——文件仍是 0600 且仅初始 admin 可读目录。
	if err := op.UserConsumeInitialPasswordFile(); err != nil {
		log.Warnf("login succeeded but failed to remove initial password file: %v", err)
	}
	loginLimiter.reset(c.Request.Context(), ip)
	token, maxAge, err := auth.GenerateJWTToken()
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	middleware.SetAuthCookie(c, token, maxAge)
	resp.Success(c, model.UserStatus{
		Username:           user.Username,
		MustChangePassword: op.UserGet().MustChangePassword,
	})
}

func logout(c *gin.Context) {
	middleware.ClearAuthCookie(c)
	// 单管理员: 登出即轮换 JWT 密钥, 已签发 token 全部失效, 而不仅清 cookie。
	if err := op.AuthJWTRotateSecret(); err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	resp.Success(c, nil)
}

func changePassword(c *gin.Context) {
	var user model.UserChangePassword
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.UserChangePassword(user.OldPassword, user.NewPassword); err != nil {
		if errors.Is(err, op.ErrIncorrectOldPassword) {
			resp.Error(c, http.StatusBadRequest, "incorrect old password")
			return
		}
		if errors.Is(err, op.ErrPasswordValidation) {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, op.ErrInitialPasswordFileCleanupFailed) {
			// 改密已生效, 仅初始密码文件清理失败: 返回成功并附带告警,
			// 不让客户端误以为改密未发生。
			resp.Success(c, "password changed successfully, but failed to remove the initial password file")
			return
		}
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	resp.Success(c, "password changed successfully")
}

func changeUsername(c *gin.Context) {
	var user model.UserChangeUsername
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	// 用户名是唯一登录凭据标识: 空串或超长会把账号改到无法正常登录, 写库前校验。
	username := strings.TrimSpace(user.NewUsername)
	if n := utf8.RuneCountInString(username); n < 2 || n > 64 {
		resp.Error(c, http.StatusBadRequest, "用户名需为 2-64 个字符")
		return
	}
	if err := op.UserChangeUsername(username, user.Password); err != nil {
		if errors.Is(err, op.ErrUsernameUnchanged) {
			resp.Error(c, http.StatusBadRequest, "新用户名与原用户名相同")
			return
		}
		if errors.Is(err, op.ErrIncorrectOldPassword) {
			resp.Error(c, http.StatusBadRequest, "incorrect password")
			return
		}
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	resp.Success(c, "username changed successfully")
}

func status(c *gin.Context) {
	u := op.UserGet()
	resp.Success(c, model.UserStatus{
		Username:           u.Username,
		MustChangePassword: u.MustChangePassword,
	})
}
