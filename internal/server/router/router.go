// router.go implements NovaVeil's declarative route registry: route groups are
// declared at init time and registered onto a Gin engine via RegisterAll.
package router

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GroupRouter represents a group of routes with shared path prefix and middlewares
type GroupRouter struct {
	Path        string
	Routes      []*Route
	Middlewares []gin.HandlerFunc
}

// Global registry for route groups
var registeredRouters []*GroupRouter

// registeredOnce 防止同一进程内对同一个/多个 engine 重复执行 RegisterAll:
// 二次注册会把全部路由再挂一遍(gin 允许重复挂载, 造成不可预期的优先级与计数),
// 且历史实现会把注册表清空导致第二次调用静默注册零路由。
var registeredOnce bool

// ResetRoutes 重置路由注册标志，允许 Shutdown 后重新 Start。
// 生产环境中 graceful restart 场景需要此能力；测试中多个用例各自 Start/Shutdown 也依赖它。
func ResetRoutes() {
	registeredOnce = false
}

// NewGroupRouter creates a new GroupRouter with the given path and automatically registers it.
func NewGroupRouter(path string) *GroupRouter {
	router := &GroupRouter{
		Path:   path,
		Routes: make([]*Route, 0),
	}
	registeredRouters = append(registeredRouters, router)
	return router
}

// Use adds middlewares to the group.
func (g *GroupRouter) Use(middlewares ...gin.HandlerFunc) *GroupRouter {
	g.Middlewares = append(g.Middlewares, middlewares...)
	return g
}

// AddRoute adds a route to the group.
func (g *GroupRouter) AddRoute(route *Route) *GroupRouter {
	g.Routes = append(g.Routes, route)
	return g
}

// Route defines a single endpoint with its handlers and middlewares.
type Route struct {
	Path        string
	Method      string
	Handlers    []gin.HandlerFunc
	Middlewares []gin.HandlerFunc
}

// NewRoute creates a new Route instance with the given path and method.
func NewRoute(path string, method string) *Route {
	return &Route{
		Path:     path,
		Method:   method,
		Handlers: make([]gin.HandlerFunc, 0),
	}
}

// Handle adds handler functions to the route.
func (r *Route) Handle(handlers ...gin.HandlerFunc) *Route {
	r.Handlers = append(r.Handlers, handlers...)
	return r
}

// Use adds middlewares to the route.
func (r *Route) Use(middlewares ...gin.HandlerFunc) *Route {
	r.Middlewares = append(r.Middlewares, middlewares...)
	return r
}

// Validate checks if the route is valid
func (r *Route) Validate() error {
	if len(r.Handlers) == 0 {
		return fmt.Errorf("route must have at least one handler")
	}
	return nil
}

// GetRouterCount returns the total count of registered routes across all groups.
func GetRouterCount() int {
	count := 0
	for _, router := range registeredRouters {
		count += len(router.Routes)
	}
	return count
}

// RegisterAll registers all globally registered route groups to the Gin engine
func RegisterAll(engine *gin.Engine) error {
	if registeredOnce {
		return fmt.Errorf("routes already registered")
	}
	registeredOnce = true
	for _, router := range registeredRouters {
		// Validate all routes in the group first
		for _, route := range router.Routes {
			if err := route.Validate(); err != nil {
				return fmt.Errorf("invalid route in group %s: %w", router.Path, err)
			}
		}

		// Create the route group
		group := engine.Group(router.Path, router.Middlewares...)

		// Register all routes in the group
		for _, route := range router.Routes {
			handlers := make([]gin.HandlerFunc, 0, len(route.Middlewares)+len(route.Handlers))
			handlers = append(handlers, route.Middlewares...)
			handlers = append(handlers, route.Handlers...)

			if err := registerRoute(group, route.Method, route.Path, handlers); err != nil {
				return fmt.Errorf("invalid route %s %s in group %s: %w", route.Method, route.Path, router.Path, err)
			}
		}
	}
	return nil
}

// registerRoute registers a single route to a Gin route group.
// 未知 method 返回错误而不是静默注册成 GET: 路由声明若打错会以 404 形式在生产暴露,
// 而错误把所有请求都落到 GET 处理器(审计 SEC-07)。
func registerRoute(group *gin.RouterGroup, method string, path string, handlers []gin.HandlerFunc) error {
	if len(handlers) == 0 {
		return fmt.Errorf("route must have at least one handler")
	}

	if path != "" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}

	switch method {
	case http.MethodGet:
		group.GET(path, handlers...)
	case http.MethodPost:
		group.POST(path, handlers...)
	case http.MethodPut:
		group.PUT(path, handlers...)
	case http.MethodDelete:
		group.DELETE(path, handlers...)
	case http.MethodHead:
		group.HEAD(path, handlers...)
	case http.MethodOptions:
		group.OPTIONS(path, handlers...)
	case http.MethodPatch:
		group.PATCH(path, handlers...)
	default:
		return fmt.Errorf("unsupported HTTP method %q", method)
	}
	return nil
}
