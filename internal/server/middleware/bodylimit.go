package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxAPIBodyBytes 管理端 JSON API 的请求体上限:
// 防止已登录会话向内存灌入超大 JSON(ShouldBindJSON 会完整读入), 8MB 足以覆盖
// 全部管理端配置写操作; DB 导入接口自带 128MB 专用上限, 在此放行由其自行限制。
// 中转路径(/v1)不受此限制, 由 relay 的分压缩/解压双层限额(bodylimit.go)管辖。
const maxAPIBodyBytes int64 = 8 * 1024 * 1024

// maxLoginBodyBytes 登录接口请求体上限 64KiB。
const maxLoginBodyBytes int64 = 64 * 1024

// LoginBodyLimit 为登录路由单独收紧请求体上限。
func LoginBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginBodyBytes)
		}
		c.Next()
	}
}

// BodyLimit 限制管理端 API 请求体大小。仅在 /api 前缀路由上包裹 MaxBytesReader,
// 超限时读取方得到错误并由 handler 以 400 响应, 不主动短路 SSE/流式 GET。
func BodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1/setting/import") {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAPIBodyBytes)
		c.Next()
	}
}
