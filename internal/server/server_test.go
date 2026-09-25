package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/conf"
)

// setServerConf 把全局配置指到 127.0.0.1:port, 测试结束还原。
func setServerConf(t *testing.T, port int) {
	t.Helper()
	old := conf.AppConfig.Server
	conf.AppConfig.Server.Host = "127.0.0.1"
	conf.AppConfig.Server.Port = port
	t.Cleanup(func() { conf.AppConfig.Server = old })
}

// freePort 找一个当前空闲的 TCP 端口: 先 listen :0 拿端口号再关闭。
// 与 Start() 之间存在极小的被抢窗口, 测试场景可接受。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestStartListensBeforeReturn Start 返回后端口必须已可连接:
// cmd 层依赖「Start 成功 = 服务真的在听」决定是否进入关停等待。
func TestStartListensBeforeReturn(t *testing.T) {
	port := freePort(t)
	setServerConf(t, port)

	if err := Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = Shutdown(ctx)
	})

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatalf("Start 返回后端口应已可连接: %v", err)
	}
	_ = conn.Close()
}

// TestStartPortOccupiedFails 端口被占用时 Start 必须把 bind 错误交回调用方,
// 而不是吞进 goroutine 让进程以退出码 0 继续跑(Docker/systemd 无法按失败拉起)。
func TestStartPortOccupiedFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	setServerConf(t, port)
	if err := Start(); err == nil {
		t.Fatal("端口被占用时 Start 应返回 error, 实际 nil")
	}
}

// TestCancelInFlightCancelsBaseContext 验证 CancelInFlight 取消后,
// 新请求的 context 立即进入 Done: 这是停机时让在途 handler 主动收尾的前提。
func TestCancelInFlightCancelsBaseContext(t *testing.T) {
	port := freePort(t)
	setServerConf(t, port)

	if err := Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = Shutdown(ctx)
	})

	baseCtxMu.Lock()
	ctx := baseCtx
	baseCtxMu.Unlock()
	if ctx == nil {
		t.Fatal("baseCtx should be set after Start")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("baseCtx should be active before CancelInFlight: %v", err)
	}

	CancelInFlight()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("baseCtx should be cancelled after CancelInFlight")
	}
}

// swapHTTPServer 在锁内置换全局 httpSrv 并返回旧值, 供测试的 t.Cleanup 恢复。
// httpSrv 指针的读写必须持 httpSrvMu: Serve goroutine、Shutdown/Close 与本测试的
// 恢复路径并发访问同一指针, 裸赋值在 -race 下报 DATA RACE。锁外返回值仅供恢复用。
func swapHTTPServer(srv *http.Server) (old *http.Server) {
	httpSrvMu.Lock()
	defer httpSrvMu.Unlock()
	old = httpSrv
	httpSrv = srv
	return old
}

// TestShutdownForceCloseOnTimeout 验证 Shutdown 超时后调用 Close 强制中断所有连接:
// 阻塞 handler 使 Shutdown 无法在 deadline 内完成, Shutdown 应返回 error 并强制关闭。
func TestShutdownForceCloseOnTimeout(t *testing.T) {
	// 保存包级状态, 测试后恢复。
	// httpSrv 是 *http.Server 指针, 保存/恢复指针不会拷贝 struct 内部状态。
	oldSrv := swapHTTPServer(&http.Server{Handler: nil})
	oldBaseCtx := baseCtx
	oldBaseCancel := baseCancel
	t.Cleanup(func() {
		swapHTTPServer(oldSrv)
		baseCtxMu.Lock()
		baseCtx = oldBaseCtx
		baseCancel = oldBaseCancel
		baseCtxMu.Unlock()
	})

	// 构造一个 handler 永远阻塞的 server。
	block := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		<-block
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	baseCtxMu.Lock()
	baseCtx, baseCancel = context.WithCancel(context.Background())
	ctxForBase := baseCtx
	baseCtxMu.Unlock()

	srv := &http.Server{
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return ctxForBase },
	}
	swapHTTPServer(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// 发起一个会阻塞的请求。
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// 等待 handler 开始执行。
	time.Sleep(100 * time.Millisecond)

	// 以极短超时调用 Shutdown: handler 阻塞无法在 deadline 内完成。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = Shutdown(ctx)
	close(block)

	if err == nil {
		t.Fatal("Shutdown 应在超时时返回 error")
	}
}

// TestShutdownOrderingCancelBeforeDrain 验证停机顺序:
// CancelInFlight 先于 Shutdown 调用, handler 因 ctx.Done() 主动收尾,
// Shutdown 在 deadline 内成功完成(无需强制 Close)。
func TestShutdownOrderingCancelBeforeDrain(t *testing.T) {
	oldSrv := swapHTTPServer(&http.Server{Handler: nil})
	oldBaseCtx := baseCtx
	oldBaseCancel := baseCancel
	t.Cleanup(func() {
		swapHTTPServer(oldSrv)
		baseCtxMu.Lock()
		baseCtx = oldBaseCtx
		baseCancel = oldBaseCancel
		baseCtxMu.Unlock()
	})

	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 模拟长 SSE: 等待 context 取消后收尾。
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
		close(handlerDone)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	baseCtxMu.Lock()
	baseCtx, baseCancel = context.WithCancel(context.Background())
	ctxForBase := baseCtx
	baseCtxMu.Unlock()

	srv := &http.Server{
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return ctxForBase },
	}
	swapHTTPServer(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// 发起一个长 SSE 请求(在 goroutine 中, handler 阻塞到 context 取消后才响应)。
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	// 等待请求到达 server。
	time.Sleep(100 * time.Millisecond)

	// 模拟停机: 先 cancel in-flight, 再 drain。
	CancelInFlight()

	// handler 应因 ctx.Done() 主动收尾。
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler 未在 CancelInFlight 后收尾")
	}

	// 等待响应返回。
	select {
	case resp := <-respCh:
		_ = resp.Body.Close()
	case err := <-errCh:
		t.Fatalf("request error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("response not received after handler done")
	}

	// Shutdown 应在 deadline 内成功, 无需强制 Close。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 应在 handler 收尾后成功: %v", err)
	}
}

// TestTrustedProxiesDirectConnectionIgnoresForgedXFF 验证直连部署(无可信代理)时,
// 伪造的 X-Forwarded-For 不影响 ClientIP: 登录限速等按 IP 的防护不可被伪造头绕过。
func TestTrustedProxiesDirectConnectionIgnoresForgedXFF(t *testing.T) {
	old := conf.AppConfig.Server.TrustedProxies
	conf.AppConfig.Server.TrustedProxies = nil
	t.Cleanup(func() { conf.AppConfig.Server.TrustedProxies = old })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := configureTrustedProxies(r); err != nil {
		t.Fatalf("configureTrustedProxies: %v", err)
	}

	var clientIP string
	r.GET("/ip", func(c *gin.Context) {
		clientIP = c.ClientIP()
	})

	req := httptest.NewRequest("GET", "/ip", nil)
	req.RemoteAddr = "203.0.113.5:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if clientIP == "1.2.3.4" {
		t.Fatalf("直连伪造 XFF 不应被采信: ClientIP=%s, 不应等于 1.2.3.4", clientIP)
	}
	if !strings.HasPrefix(clientIP, "203.0.113.5") {
		t.Fatalf("ClientIP 应为真实远端地址, 实际 %s", clientIP)
	}
}

// TestTrustedProxiesConfiguredTrustsXFF 验证配置可信代理后,
// 来自可信地址的 X-Forwarded-For 被正确采信, TLS 反代后 ClientIP 为真实客户端 IP。
func TestTrustedProxiesConfiguredTrustsXFF(t *testing.T) {
	old := conf.AppConfig.Server.TrustedProxies
	conf.AppConfig.Server.TrustedProxies = []string{"127.0.0.1/32"}
	t.Cleanup(func() { conf.AppConfig.Server.TrustedProxies = old })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := configureTrustedProxies(r); err != nil {
		t.Fatalf("configureTrustedProxies: %v", err)
	}

	var clientIP string
	r.GET("/ip", func(c *gin.Context) {
		clientIP = c.ClientIP()
	})

	req := httptest.NewRequest("GET", "/ip", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if clientIP != "1.2.3.4" {
		t.Fatalf("可信代理的 XFF 应被采信: ClientIP=%s, 应为 1.2.3.4", clientIP)
	}
}

// TestTrustedProxiesUntrustedSourceIgnoresXFF 验证配置可信代理后,
// 来自非可信地址的 X-Forwarded-For 仍不被采信。
func TestTrustedProxiesUntrustedSourceIgnoresXFF(t *testing.T) {
	old := conf.AppConfig.Server.TrustedProxies
	conf.AppConfig.Server.TrustedProxies = []string{"10.0.0.0/8"}
	t.Cleanup(func() { conf.AppConfig.Server.TrustedProxies = old })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := configureTrustedProxies(r); err != nil {
		t.Fatalf("configureTrustedProxies: %v", err)
	}

	var clientIP string
	r.GET("/ip", func(c *gin.Context) {
		clientIP = c.ClientIP()
	})

	req := httptest.NewRequest("GET", "/ip", nil)
	req.RemoteAddr = "203.0.113.5:12345" // 非可信地址
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if clientIP == "1.2.3.4" {
		t.Fatalf("非可信地址的 XFF 不应被采信: ClientIP=%s", clientIP)
	}
	if !strings.HasPrefix(clientIP, "203.0.113.5") {
		t.Fatalf("ClientIP 应为真实远端地址, 实际 %s", clientIP)
	}
}
