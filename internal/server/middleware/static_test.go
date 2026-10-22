package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeFileSystem 返回 os.DirFS 适配 fs.FS, 让中间件走真实文件 IO 路径, 适配 gzip
// 预压缩的回源测试。传入的 fs 仅用于 gin 中间件; 另返回 dir 路径供测试代码自身
// 写入构建期产物 (例如 .gz 预压缩文件)。
func fakeFileSystem(t *testing.T) (fs.FS, string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("assets/index.js", "console.log('hello')\n")
	mustWrite("assets/styles.css", "body { margin: 0 }\n")
	mustWrite("index.html", "<!doctype html>\n")
	mustWrite("__flags/default.json", `{"new-web":true}`+"\n")
	return os.DirFS(dir), dir
}

func TestStaticServesPrecompressedWhenAcceptGzip(t *testing.T) {
	fs, dir := fakeFileSystem(t)

	// 模拟构建期预压缩阶段: 对每个文本类资源产出 .gz 等价物。
	const srcPath = "assets/index.js"
	gzPath := filepath.Join(dir, srcPath+".gz")
	if err := os.WriteFile(gzPath, gzipBytes(t, mustRead(t, filepath.Join(dir, srcPath))), 0o644); err != nil {
		t.Fatalf("写入预压缩 .gz: %v", err)
	}

	r := gin.New()
	r.Use(StaticEmbed("", fs))
	r.GET("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+srcPath, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("命中 .gz 直发必须声明 Content-Encoding: gzip, 实际 %q（缺失时浏览器把 .gz 字节当原文渲染）", got)
	}
	if got := w.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("missing Vary: Accept-Encoding, got %q", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("Content-Type 应识别 .js, got %q", got)
	}
	if got := w.Header().Get("Content-Length"); got == "" {
		t.Fatalf("Content-Length 应直传 .gz 字节数")
	}
	// 验证响应体确实是 .gz 字节流且能解出原文。
	body, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v (body 应该是 .gz 流)", err)
	}
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if !bytes.Contains(decoded, []byte("hello")) {
		t.Fatalf("解出的原文不含 hello: %q", decoded)
	}
}

func TestStaticFallsBackToRuntimeGzipWhenNoPrecompressed(t *testing.T) {
	fs, dir := fakeFileSystem(t)
	// 不预写任何 .gz → 应回退运行时压缩
	_ = dir

	r := gin.New()
	r.Use(StaticEmbed("", fs))
	r.GET("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("兜底路径应运行时 gzip, got Content-Encoding=%q", got)
	}
	body, _ := io.ReadAll(w.Body)
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("兜底响应体必须是 gzip: %v", err)
	}
	defer gz.Close()
}

func TestStaticDoesNotGzipWhenAcceptEmpty(t *testing.T) {
	fs, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs))
	r.GET("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	// 无 Accept-Encoding 时不压缩
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("未声明 gzip 时不应压缩, got %q", got)
	}
}

func TestPrecompressedContentTypeMatchesExtension(t *testing.T) {
	cases := []struct {
		ext  string
		want string
	}{
		{".js", "text/javascript"},
		{".css", "text/css"},
		{".html", "text/html"},
		{".svg", "image/svg+xml"},
		{".json", "application/json"},
		{".map", "application/json"},
		{".webmanifest", "application/manifest+json"},
		{".txt", "text/plain"},
		{".bin", ""},               // 未知扩展名
		{".JS", "text/javascript"}, // 大小写不敏感
	}
	for _, tc := range cases {
		got := precompressedContentType(tc.ext)
		if tc.want == "" && got != "" {
			t.Errorf("%s 期望空, got %q", tc.ext, got)
			continue
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s 期望包含 %q, got %q", tc.ext, tc.want, got)
		}
	}
}

// helpers
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func gzipBytes(t *testing.T, src []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(src); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// ---- 以下回归移植自 NovaVeil 0cc3478（无路由链下的预压缩直发契约）----

// TestStaticNoRoutePrecompressedServes200WithEncoding 回归: 生产拓扑里静态路径
// 全部落入 gin 的 NoRoute 链, gin 的 serveError 会先把 writermem.status 预置成
// 404 再运行中间件; 预压缩直发必须显式回写 200（覆盖预置的 404）并带
// Content-Encoding: gzip, 否则全部静态资源以"404 + gzip 字节 + 无编码头"下发,
// 浏览器当失败处理且无法解码, CDN 还会把 404 按不可变 TTL 缓存下来。
func TestStaticNoRoutePrecompressedServes200WithEncoding(t *testing.T) {
	fs2, dir := fakeFileSystem(t)
	srcPath := "assets/index.js"
	if err := os.WriteFile(filepath.Join(dir, srcPath+".gz"), gzipBytes(t, mustRead(t, filepath.Join(dir, srcPath))), 0o644); err != nil {
		t.Fatalf("写入预压缩 .gz: %v", err)
	}
	// 只挂静态中间件、不注册任何业务路由 → 全部请求走 NoRoute 链。
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+srcPath, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("预压缩直发应返回 200, 实际 %d", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding 应为 gzip, 实际 %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type 应为 text/javascript; charset=utf-8, 实际 %q", got)
	}
	gz, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应体应可解压: %v", err)
	}
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if !bytes.Contains(decoded, []byte("hello")) {
		t.Fatalf("解出的原文不含 hello: %q", decoded)
	}
}

// TestStaticNoRouteWithoutAcceptEncoding 回退路径: 客户端不接受 gzip 时由
// fileserver 回源原始文件; http.ServeContent 显式写状态码, 无路由链下同样保持 200。
func TestStaticNoRouteWithoutAcceptEncoding(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/index.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("无编码请求应返回 200, 实际 %d", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("无编码请求不应带 Content-Encoding, 实际 %q", got)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("hello")) {
		t.Fatalf("回退路径应返回原始文件内容, 实际 %q", w.Body.String())
	}
}

// TestStaticNoRouteMissAndShell 收尾覆盖: 未命中文件 404, 首页壳 200 且不预压缩。
func TestStaticNoRouteMissAndShell(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("未命中文件应返回 404, 实际 %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("首页应返回 200, 实际 %d", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("无扩展名首页不应被压缩, 实际 %q", got)
	}
}

// TestStaticSkipsAPIPaths 确保 /api 和 /v1 前缀的请求不被静态中间件拦截，
// 而是交由后端路由处理。回归 /v1/models 被 SPA fallback 吞掉返回 404 的问题。
func TestStaticSkipsAPIPaths(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	for _, prefix := range []string{"/api/v1/user/status", "/v1/models", "/v1/chat/completions"} {
		called := false
		// 为每种路径注册一个后端路由，验证中间件确实放行。
		switch prefix {
		case "/api/v1/user/status":
			r.GET(prefix, func(c *gin.Context) { called = true; c.JSON(http.StatusOK, gin.H{"ok": true}) })
		case "/v1/models":
			r.GET(prefix, func(c *gin.Context) { called = true; c.JSON(http.StatusOK, gin.H{"data": []any{}}) })
		case "/v1/chat/completions":
			r.POST(prefix, func(c *gin.Context) { called = true; c.JSON(http.StatusOK, gin.H{"ok": true}) })
		}

		w := httptest.NewRecorder()
		method := http.MethodGet
		if strings.HasSuffix(prefix, "/completions") {
			method = http.MethodPost
		}
		r.ServeHTTP(w, httptest.NewRequest(method, prefix, nil))

		if !called {
			t.Errorf("%s: 后端路由未被调用，静态中间件可能拦截了请求", prefix)
		}
		if w.Code != http.StatusOK {
			t.Errorf("%s: 期望 200，实际 %d（body: %s）", prefix, w.Code, w.Body.String())
		}
		// 确保返回的是 JSON 而非 index.html
		if !strings.Contains(w.Body.String(), "{") {
			t.Errorf("%s: 响应体不是 JSON，可能被 SPA fallback 吞掉", prefix)
		}
	}
}

func TestStaticSkipsHealthz(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz was intercepted by static fallback: %q", w.Body.String())
	}
}

func TestStaticMethodContract(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	cases := []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodHead, http.StatusOK},
		{http.MethodPost, http.StatusMethodNotAllowed},
		{http.MethodPut, http.StatusMethodNotAllowed},
		{http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPatch, http.StatusMethodNotAllowed},
		{http.MethodTrace, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, "/", nil))
		if w.Code != tc.want {
			t.Errorf("%s / = %d, want %d", tc.method, w.Code, tc.want)
		}
		if tc.want == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s / Allow = %q, want %q", tc.method, w.Header().Get("Allow"), "GET, HEAD")
		}
	}
}

func TestStaticDoesNotRejectNonGetAPIMethods(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))
	r.PUT("/api/v1/channel/update", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/channel/update", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT API status = %d, want 204", w.Code)
	}
}

// TestStaticSPAFallbackForFrontendRoutes 确保前端路由（/channels /dashboard 等）
// 直接访问时返回 index.html（SPA fallback），而非 404。
func TestStaticSPAFallbackForFrontendRoutes(t *testing.T) {
	fs2, _ := fakeFileSystem(t)
	r := gin.New()
	r.Use(StaticEmbed("", fs2))

	for _, route := range []string{"/channels", "/dashboard", "/settings", "/groups", "/logs"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: 前端路由应返回 200 (index.html), 实际 %d", route, w.Code)
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("doctype")) {
			t.Errorf("%s: 响应体应包含 index.html 内容, 实际 %q", route, w.Body.String())
		}
	}
}
