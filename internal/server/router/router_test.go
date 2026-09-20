package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterRouteUnknownMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/test")

	handlers := []gin.HandlerFunc{func(c *gin.Context) { c.Status(http.StatusOK) }}
	if err := registerRoute(group, http.MethodGet, "/ok", handlers); err != nil {
		t.Fatalf("GET should register, got %v", err)
	}

	if err := registerRoute(group, "TRACE", "/bad", handlers); err == nil {
		t.Fatal("unknown method should return error, not silently fall back to GET")
	}
}

func TestRegisterAllUnknownMethodFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registeredRouters = []*GroupRouter{{
		Path:   "/test",
		Routes: []*Route{{Path: "/bad", Method: "TRACE", Handlers: []gin.HandlerFunc{func(c *gin.Context) {}}}},
	}}
	// 注册失败后 registeredOnce 仍为 true(与历史行为一致); 这里只在边界测试前手动复位,
	// 保证该测试不会向其他测试泄漏注册状态。
	registeredOnce = false
	defer func() {
		registeredOnce = false
		registeredRouters = nil
	}()

	engine := gin.New()
	if err := RegisterAll(engine); err == nil {
		t.Fatal("RegisterAll should fail when a route has unknown method")
	}
}
