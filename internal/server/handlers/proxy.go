package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/client"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/setting").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/proxy/test", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(testProxy),
		)
}

// proxyTestTimeout 代理测试超时: 涵盖代理握手 + TLS + IP 查询服务响应。
const proxyTestTimeout = 15 * time.Second

// ipEchoURLs 依次尝试的出口 IP 查询服务, 任一成功即返回。
var ipEchoURLs = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
}

// proxyTestDefaultAccount 代理池测试时填充 {account} 占位符的默认账号。
// 代理池条目可含 {account}(与渠道专属代理同口径), 但测试不绑定具体渠道/密钥,
// 无从派生别名, 故固定用 NovaVeil 作为默认账号, 使形如
// socks5h://Default.{account}:1@resin:2260 的条目也能测出口 IP。
const proxyTestDefaultAccount = "NovaVeil"

// errProxyTestTemplateInvalid 代理解析失败的固定哨兵, 不透出模板原文(可能含凭据)。
var errProxyTestTemplateInvalid = errors.New("proxy url is invalid")

type proxyTestRequest struct {
	URL string `json:"url"`
}

type proxyTestResult struct {
	IP      string `json:"ip"`       // 出口 IP 地址; 失败时为空。
	Elapsed int64  `json:"elapsed"`  // 耗时(毫秒)。
}

// testProxy 通过指定代理访问公网 IP 查询服务, 返回出口 IP 地址。
func testProxy(c *gin.Context) {
	var req proxyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		resp.Error(c, http.StatusBadRequest, "请填写代理地址")
		return
	}

	// 代理池条目可含 {account} 占位符, { 与 } 不在 net/url 允许的 userinfo 字符集内,
	// 直接 url.Parse 会失败。先用默认账号解析占位符, 再建客户端测出口 IP。
	resolved, err := resolveProxyTestURL(url)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	httpClient, err := client.GetHTTPClientCustomProxy(resolved)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), proxyTestTimeout)
	defer cancel()

	var lastErr error
	for _, echoURL := range ipEchoURLs {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, echoURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp2, err := httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		// 读取 body 后立即关闭, 避免连接泄漏。
		body, err := io.ReadAll(io.LimitReader(resp2.Body, 64))
		resp2.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp2.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s returned status %d", echoURL, resp2.StatusCode)
			continue
		}
		ip := strings.TrimSpace(string(body))
		if ip == "" {
			lastErr = fmt.Errorf("%s returned empty body", echoURL)
			continue
		}
		resp.Success(c, proxyTestResult{
			IP:      ip,
			Elapsed: time.Since(start).Milliseconds(),
		})
		return
	}
	resp.Error(c, http.StatusBadGateway, fmt.Sprintf("代理测试失败: %v", lastErr))
}

// resolveProxyTestURL 解析代理测试地址: 含 {account} 占位符时用默认账号 NovaVeil
// 填充后再返回, 使代理池里带占位符的条目也能通过 url.Parse 建客户端测出口 IP;
// 不含占位符时原样返回, 不做任何归一化, 保持与既有行为一致。
func resolveProxyTestURL(rawURL string) (string, error) {
	if !strings.Contains(rawURL, model.AccountPlaceholder) {
		return rawURL, nil
	}
	resolved, err := helper.ResolveProxyTemplate(rawURL, proxyTestDefaultAccount)
	if err != nil {
		return "", errProxyTestTemplateInvalid
	}
	return resolved.String(), nil
}
