package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	_ "github.com/kingsunb/NovaVeil/internal/server/handlers"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"github.com/kingsunb/NovaVeil/static"
)

var (
	// httpSrv 使用指针, 避免 http.Server 内含 sync/atomic.noCopy 被值拷贝
	// (go vet 报 "copies lock value"), 并允许测试中原子替换服务器实例
	// 而不与并发 Serve 协程产生数据竞争。
	httpSrv *http.Server

	// baseCtx/baseCancel 为所有 HTTP 请求提供共享根 context。
	// CancelInFlight 取消该 context 后, 每个活动请求的 context 立即进入 Done,
	// relay handler 在转发循环中检查 ctx.Err() 后会尽快收尾, 不必等待
	// http.Server.Shutdown 的超时才被动放弃。
	baseCtxMu  sync.Mutex
	baseCtx    context.Context
	baseCancel context.CancelFunc
)

func Start() error {
	if conf.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	// 可信代理配置: 直连部署不信任任何代理头(ClientIP 不采信可伪造的
	// X-Forwarded-For, 登录限速等按 IP 的防护不可被伪造头绕过); 反代部署
	// 需显式配置可信 CIDR/IP, 仅来自这些地址的 XFF 才被采信。
	if err := configureTrustedProxies(r); err != nil {
		return fmt.Errorf("failed to set trusted proxies: %w", err)
	}
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) {
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		c.Abort()
	}))

	if conf.IsDebug() {
		r.Use(middleware.RequestLogger())
	}
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.Cors())
	r.Use(middleware.OriginProtection())
	// 管理端 JSON API 请求体上限(仅 /api 前缀, 中转 /v1 由 relay 自身限额管辖)。
	r.Use(middleware.BodyLimit())
	r.Use(middleware.StaticEmbed("/", static.StaticFS))

	if err := router.RegisterAll(r); err != nil {
		return err
	}

	// 头部慢发(Slowloris)需要 ReadHeaderTimeout 显式限制; 闲置长连接需要 IdleTimeout 主动回收。
	// 流式转发(LLM SSE)的写出时长由请求上下文与客户端连接控制, 不在这里设 WriteTimeout, 避免
	// 误截长流。MaxHeaderBytes 收紧到 1MB 防止异常大的 header 撑爆内存。
	httpSrv = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", conf.AppConfig.Server.Host, conf.AppConfig.Server.Port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// 为所有请求注入共享根 context: CancelInFlight 取消后, 每个活动请求的
	// context 立即进入 Done, handler 可据此尽快收尾, 而非被动等待 Shutdown 超时。
	baseCtxMu.Lock()
	baseCtx, baseCancel = context.WithCancel(context.Background())
	ctxForBase := baseCtx
	baseCtxMu.Unlock()
	httpSrv.BaseContext = func(_ net.Listener) context.Context {
		return ctxForBase
	}
	// 同步 bind: 端口占用/地址非法时把错误交回 cmd, 进程以非 0 退出,
	// Docker restart=on-failure 与 systemd 才能按失败拉起; serve 阶段的
	// 错误仍在 goroutine 里记日志, 不阻塞 Start 返回。
	ln, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		router.ResetRoutes()
		return fmt.Errorf("listen %s: %w", httpSrv.Addr, err)
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Errorf("http server serve error: %v", err)
		}
	}()
	return nil
}

// CancelInFlight 取消所有活动 HTTP 请求的根 context, 使在途 handler 的
// ctx.Done() 立即触发。应在 Shutdown 之前调用, 让 handler 主动收尾而非
// 被动等待 Shutdown 超时。多次调用安全。
func CancelInFlight() {
	baseCtxMu.Lock()
	cancel := baseCancel
	baseCtxMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Shutdown 等待在途 handler 在 deadline 内完成, 然后关闭 server。
// 若 deadline 超时, 立即调用 Close 强制中断所有残余连接作为兜底,
// 避免长 SSE/慢任务在后续 flush/DB close 阶段继续写入。
func Shutdown(ctx context.Context) error {
	err := httpSrv.Shutdown(ctx)
	// 重置路由注册标志，允许后续重新 Start（graceful restart / 测试隔离）。
	router.ResetRoutes()
	if err != nil {
		// 超时或取消: 强制关闭所有连接, 不让残余 handler 在 DB 关闭后继续处理。
		_ = httpSrv.Close()
		return err
	}
	return nil
}

// Close 立即中断所有连接. 仅用于 Shutdown 已失败或被取消后的兜底; 优先走 Shutdown。
func Close() error {
	return httpSrv.Close()
}

// configureTrustedProxies 按配置设置可信代理 CIDR/IP 列表。
// 配置为空时不信任任何代理头(直连部署安全默认); 配置非空时仅来自这些
// 地址的 X-Forwarded-For 才被采信, TLS 反代后 ClientIP 仍为真实客户端 IP。
func configureTrustedProxies(r *gin.Engine) error {
	trusted := conf.AppConfig.Server.TrustedProxies
	if len(trusted) == 0 {
		return r.SetTrustedProxies(nil)
	}
	return r.SetTrustedProxies(trusted)
}
