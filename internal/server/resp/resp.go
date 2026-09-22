package resp

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
)

type ResponseStruct struct {
	Code    int         `json:"code" example:"200"`
	Message string      `json:"message" example:"success"`
	Data    interface{} `json:"data,omitempty"`
}

func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, ResponseStruct{
		Code:    http.StatusOK,
		Message: "success",
		Data:    data,
	})
}

func Error(c *gin.Context, code int, err string) {
	if code >= http.StatusInternalServerError {
		log.Errorf("http %d: %s", code, err)
		err = ErrInternalServer
	}
	c.AbortWithStatusJSON(code, ResponseStruct{
		Code:    code,
		Message: err,
	})
}

// ErrorExposed 与 Error 一样写 5xx 日志并回 JSON, 但保留真实 err 文案原样回给
// 客户端。仅供管理员会话内的主动探测端点使用(拉取模型 / 测试连通 / 逐 Key 测试):
// 这些端点把上游 HTTP 状态码与错误片段回显给管理员排障, 本身不出现在普通 API
// 客户端路径; 通用路径仍走 Error 兜底, 避免向非管理员泄露内部与上游细节。
func ErrorExposed(c *gin.Context, code int, err string) {
	if code >= http.StatusInternalServerError {
		log.Errorf("http %d: %s", code, err)
	}
	c.AbortWithStatusJSON(code, ResponseStruct{
		Code:    code,
		Message: err,
	})
}

// NoStore 禁止中间缓存保存敏感响应(密钥导出、明文查看、整库备份)。
func NoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
}

// ErrorMustChangePassword 以 403 返回强制改密错误, 并携带机器可读标记头
// (X-NovaVeil-Error: password_change_required): 前端的改密引导按头判定,
// message 文案可自由调整。今后任何新的发射点都必须走本助手而不是直接 Error。
func ErrorMustChangePassword(c *gin.Context) {
	c.Header(ErrMarkerHeader, ErrMarkerPasswordChangeRequired)
	Error(c, http.StatusForbidden, ErrMustChangePassword)
}

// ErrorRateLimited 以 429 返回限流错误并携带 Retry-After 头(秒)。
// retryAfterSeconds 非正数时按 1 秒下发, 避免客户端 0 退避打转。
func ErrorRateLimited(c *gin.Context, message string, retryAfterSeconds int) {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(retryAfterSeconds))
	Error(c, http.StatusTooManyRequests, message)
}

// ---------------------------------------------------------------------------
// OpenAI 兼容错误响应（/v1/ 中转路由专用）
// ---------------------------------------------------------------------------

// openAIErrorBody 是 OpenAI / new-api 风格的错误响应体。
type openAIErrorBody struct {
	Error openAIErrorDetail `json:"error"`
}

type openAIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// GenRequestID 生成与 new-api 风格一致的请求 ID: 时间戳(14 位) + 随机后缀。
func GenRequestID() string {
	const suffixLen = 20
	b := make([]byte, suffixLen)
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if _, err := rand.Read(b); err != nil {
		// rand.Read 极少失败; 退化为时间戳纳秒保证仍有唯一性。
		return fmt.Sprintf("%s%d", time.Now().Format("20060102150405"), time.Now().UnixNano())
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return time.Now().Format("20060102150405") + string(b)
}

// RelayError 以 OpenAI 兼容格式返回错误, 供 /v1/ 中转路由使用。
// message 中自动追加 request id 便于排障。
func RelayError(c *gin.Context, statusCode int, message string) {
	rid := GenRequestID()
	c.Header("X-Request-Id", rid)
	c.AbortWithStatusJSON(statusCode, openAIErrorBody{
		Error: openAIErrorDetail{
			Code:    "",
			Message: fmt.Sprintf("%s (request id: %s)", message, rid),
			Type:    "novaveil_error",
		},
	})
}
