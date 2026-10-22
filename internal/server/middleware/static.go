package middleware

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func StaticEmbed(urlPrefix string, embedFS fs.FS) gin.HandlerFunc {
	fs := http.FS(embedFS)
	return static(urlPrefix, fs)
}

// compressibleExt 判断路径扩展名是否适合 gzip 压缩(文本类资源)。
func compressibleExt(path string) bool {
	ext := path[strings.LastIndex(path, ".")+1:]
	switch strings.ToLower(ext) {
	case "js", "css", "svg", "html", "json", "map", "txt", "webmanifest":
		return true
	default:
		return false
	}
}

// gzipResponseWriter 包装响应写入器: 首次写入时移除上游设置的 Content-Length 并以 gzip 流输出。
type gzipResponseWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
}

func (g *gzipResponseWriter) Write(data []byte) (int, error) {
	g.Header().Del("Content-Length")
	return g.writer.Write(data)
}

func (g *gzipResponseWriter) WriteString(s string) (int, error) {
	g.Header().Del("Content-Length")
	return g.writer.Write([]byte(s))
}

func (g *gzipResponseWriter) Flush() {
	g.writer.Flush()
	g.ResponseWriter.Flush()
}

func static(urlPrefix string, fileSystem http.FileSystem) gin.HandlerFunc {
	fileserver := http.FileServer(fileSystem)
	if urlPrefix != "" {
		fileserver = http.StripPrefix(urlPrefix, fileserver)
	}
	return func(c *gin.Context) {
		// API 路径（管理端 /api 与中转 /v1）交由后端路由处理，不做静态/SPA fallback。
		if c.Request.URL.Path == "/healthz" ||
			strings.HasPrefix(c.Request.URL.Path, "/api") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1") {
			c.Next()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Header("Allow", "GET, HEAD")
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
		file, err := fileSystem.Open(c.Request.URL.Path)
		if err == nil {
			// 只为判断资源是否存在而打开的句柄必须立即关闭；否则高频静态请求会耗尽句柄。
			_ = file.Close()
			if strings.HasPrefix(c.Request.URL.Path, "/assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				c.Header("Cache-Control", "no-cache")
			}
			// 文本类资源启用 gzip: 首屏 JS 体积可压缩至约 1/3, 显著加快首屏加载。
			accepts := strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") &&
				compressibleExt(c.Request.URL.Path)
			if accepts {
				c.Header("Vary", "Accept-Encoding")
				// 构建期预压缩的 .gz 优先: 直发文件, 省去每次请求的运行时压缩 CPU。
				if servePrecompressed(c, fileSystem, c.Request.URL.Path) {
					c.Abort()
					return
				}
				// 未预压缩(如本地目录模式)回退动态压缩。
				c.Header("Content-Encoding", "gzip")
				gz := gzip.NewWriter(c.Writer)
				defer gz.Close()
				c.Writer = &gzipResponseWriter{ResponseWriter: c.Writer, writer: gz}
			}
			fileserver.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		// SPA fallback: 文件不存在且非 API 路径，回退到 index.html，
		// 让前端路由器接管（如 /dashboard /channels 等前端路由）。
		// 排除有文件扩展名的路径（如 .js .css .png），那些是真正的资源 404。
		// 用 http.ServeContent 直接输出，不用 fileserver.ServeHTTP —
		// 后者会把 /index.html 301 重定向到 ./，导致浏览器 URL 从 /channels 跳到 /，
		// React Router 丢失原始路由。
		if path.Ext(c.Request.URL.Path) == "" {
			indexFile, indexErr := fileSystem.Open("/index.html")
			if indexErr == nil {
				defer indexFile.Close()
				stat, statErr := indexFile.Stat()
				if statErr == nil {
					c.Header("Cache-Control", "no-cache")
					http.ServeContent(c.Writer, c.Request, "index.html", stat.ModTime(), indexFile)
					c.Abort()
					return
				}
			}
		}
	}
}

// servePrecompressed 尝试直接回源构建期产出的 name+".gz" 预压缩文件:
// 命中时按原始扩展名给 Content-Type, 以 .gz 文件的字节数作 Content-Length 原样传输。
func servePrecompressed(c *gin.Context, fileSystem http.FileSystem, name string) bool {
	file, err := fileSystem.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		return false
	}
	if contentType := precompressedContentType(path.Ext(name)); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	// 浏览器只有看到该头才会解压 .gz 字节; 缺了它即使 200 也只会收到一堆二进制乱码。
	c.Header("Content-Encoding", "gzip")
	c.Header("Content-Length", strconv.FormatInt(stat.Size(), 10))
	// gin 对未命中路由的请求会先把 writermem.status 预置成 404 再跑全局中间件链;
	// 既有 fileserver 路径经 http.ServeContent 显式写状态码覆盖它, 这里的手动直发
	// 必须同样显式声明 200, 否则首个 Write 就带着预置的 404 出站。
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, file)
	// 预压缩文件一旦命中并写出响应，就不能再回退动态压缩，否则会二次写响应。
	return true
}

// precompressedContentType 预压缩资源按原始扩展名的固定 Content-Type 映射。
// 不用 mime.TypeByExtension: 它在 Windows 上会读注册表, 可能把 .js 判成 text/plain
// 导致浏览器拒绝执行模块脚本。
func precompressedContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".webmanifest":
		return "application/manifest+json"
	case ".json", ".map":
		return "application/json"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return ""
	}
}
