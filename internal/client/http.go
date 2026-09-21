package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"golang.org/x/net/proxy"
)

var (
	systemDirectClient *http.Client
	systemProxyClient  *http.Client
	systemProxyURL     string
	clientLock         sync.RWMutex
)

var customProxyClients sync.Map // customProxyClients 按代理地址保存复用连接池的 *http.Client。

// GetHTTPClientSystemProxy returns a cached http.Client.
// - useProxy=false: bypass proxy
// - useProxy=true: use proxy settings from system/app settings (setting key: proxy_url)
func GetHTTPClientSystemProxy(useProxy bool) (*http.Client, error) {
	if useProxy {
		currentProxyURL, err := op.SettingGetString(model.SettingKeyProxyURL)
		if err != nil {
			return nil, err
		}
		if currentProxyURL == "" {
			return nil, fmt.Errorf("proxy url is empty")
		}
		// 每次出站都标注系统代理的使用与地址(密码打码), 便于在日志中追踪出口。
		// 挂 Debug 级别: 该行在每请求路径上, Info 级别下高频刷屏。
		if log.GetLevel() <= log.DebugLevel {
			log.Debugf("outbound via system proxy: %s", MaskProxySecret(currentProxyURL))
		}

		clientLock.RLock()
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			clientLock.RUnlock()
			return systemProxyClient, nil
		}
		clientLock.RUnlock()

		clientLock.Lock()
		defer clientLock.Unlock()

		// Re-check after acquiring write lock.
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			return systemProxyClient, nil
		}

		client, err := newHTTPClientCustomProxy(currentProxyURL)
		if err != nil {
			return nil, err
		}
		if systemProxyClient != nil {
			systemProxyClient.CloseIdleConnections()
		}
		systemProxyClient = client
		systemProxyURL = currentProxyURL
		return systemProxyClient, nil
	}

	clientLock.RLock()
	if systemDirectClient != nil {
		clientLock.RUnlock()
		return systemDirectClient, nil
	}
	clientLock.RUnlock()

	clientLock.Lock()
	defer clientLock.Unlock()

	if systemDirectClient != nil {
		return systemDirectClient, nil
	}
	client, err := newHTTPClientNoProxy()
	if err != nil {
		return nil, err
	}
	systemDirectClient = client
	return systemDirectClient, nil
}

// GetHTTPClientCustomProxy returns a cached http.Client for each proxy URL.
// proxyURL supports: http, https, socks, socks5, socks5h
func GetHTTPClientCustomProxy(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return nil, fmt.Errorf("proxy url is empty")
	}
	if cached, ok := customProxyClients.Load(proxyURL); ok {
		return cached.(*http.Client), nil
	}
	client, err := newHTTPClientCustomProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	// 条目数软上限: 渠道反复改代理地址会不断换新键, 无限累积废弃 Transport。
	// 超限时整体清空重来——活跃的代理地址组合远小于该上限, 清空只发生在
	// 配置剧烈变动的极端场景, 连接池重建的代价可接受。
	if size := proxyClientsApproxSize(); size >= maxCustomProxyClients {
		customProxyClients.Range(func(key, val any) bool {
			if c, ok := val.(*http.Client); ok {
				c.CloseIdleConnections()
			}
			customProxyClients.Delete(key)
			return true
		})
	}
	actual, loaded := customProxyClients.LoadOrStore(proxyURL, client)
	if loaded {
		client.CloseIdleConnections()
	}
	return actual.(*http.Client), nil
}

// maxCustomProxyClients 自定义代理客户端缓存的软上限。
const maxCustomProxyClients = 64

// proxyClientsApproxSize 粗略统计当前缓存条目数, 仅用于软上限判定。
func proxyClientsApproxSize() int {
	n := 0
	customProxyClients.Range(func(any, any) bool {
		n++
		return true
	})
	return n
}

func clonedDefaultTransport() (*http.Transport, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not *http.Transport")
	}
	cloned := transport.Clone()
	// 上游集中在少数几个 provider 域名: 默认 MaxIdleConnsPerHost=2 会让并发超过 2
	// 就持续拆除空闲连接并反复 TLS 握手, 直接抬高上游延迟。显式放大每主机空闲连接池。
	cloned.MaxIdleConns = 512
	cloned.MaxIdleConnsPerHost = 128
	cloned.IdleConnTimeout = 90 * time.Second
	return cloned, nil
}

// maxUpstreamRedirects 是渠道出站 HTTP 客户端允许跟随的最大重定向次数。
// 超过该上限视为异常(重定向环或过长跳转链)并中止跟随, 避免凭据反复重发与转发资源被占用。
const maxUpstreamRedirects = 5

// upstreamCheckRedirect 是所有渠道出站 HTTP 客户端共用的重定向安全策略:
//   - 限制总跳转次数不超过 maxUpstreamRedirects;
//   - 禁止 HTTPS→非 HTTPS 降级, 即便同源也拒绝;
//   - 禁止跨源重定向: 下一跳 host(主机名+端口归一化后)与上一跳不同即拒绝,
//     防止配置在上游 A 的凭据(X-Api-Key / Authorization / X-Goog-Api-Key /
//     Proxy-Authorization / Cookie 等自定义认证头)被 3xx 带到另一主机 B。
//
// 被拒绝时 client.Do 返回错误以及 Body 已关闭的 3xx 响应, 调用方按错误处理,
// 不会把 3xx 当作成功 SSE/JSON 透传。该策略集中在渠道 HTTP 客户端创建处
// (newHTTPClientNoProxy / newHTTPClientCustomProxy); 模型同步(helper/fetch.go)与
// 流式/非流式透传(relay/upstream.go)均经 ChannelHttpClient 取得同一组客户端, 统一受保护。
func upstreamCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= maxUpstreamRedirects {
		return fmt.Errorf("upstream redirect limit %d exceeded", maxUpstreamRedirects)
	}
	prev := via[len(via)-1]
	// 禁止 HTTPS 降级到非 HTTPS(同源也不允许)。
	if strings.EqualFold(prev.URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("refused insecure redirect from %s to %s", prev.URL.String(), req.URL.String())
	}
	// 禁止跨源(不同主机名或端口)重定向。
	if !sameRedirectHost(prev.URL, req.URL) {
		return fmt.Errorf("refused cross-origin redirect from %q to %q", prev.URL.Host, req.URL.Host)
	}
	return nil
}

// sameRedirectHost 判断两个 URL 是否指向同一源: 主机名与端口归一化后完全相同。
// 缺省端口按 scheme 补齐(https→443, http→80), 避免 example.com 与 example.com:80 被误判为跨源。
func sameRedirectHost(a, b *url.URL) bool {
	return normalizeRedirectHost(a) == normalizeRedirectHost(b)
}

func normalizeRedirectHost(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return host + ":" + port
}

func newHTTPClientNoProxy() (*http.Client, error) {
	cloned, err := clonedDefaultTransport()
	if err != nil {
		return nil, err
	}
	cloned.Proxy = nil
	return &http.Client{Transport: cloned, CheckRedirect: upstreamCheckRedirect}, nil
}

// MaskProxySecret 返回用于日志展示的代理地址: 保留协议、主机、端口与用户名(含 {account}
// 解析出的别名), 密码段以 **** 打码, 避免 userinfo 凭据进入日志。
// 地址无法解析时返回固定占位符, 不回显原文(原文可能含密码)。
func MaskProxySecret(proxyURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil {
		return "(invalid proxy url)"
	}
	if parsed.User == nil {
		return parsed.String()
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.UserPassword(parsed.User.Username(), "****")
	}
	return parsed.String()
}

func newHTTPClientCustomProxy(proxyURLStr string) (*http.Client, error) {
	cloned, err := clonedDefaultTransport()
	if err != nil {
		return nil, err
	}

	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}

	switch proxyURL.Scheme {
	case "http", "https":
		cloned.Proxy = http.ProxyURL(proxyURL)
	case "socks", "socks5", "socks5h":
		socksDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("invalid socks proxy: %w", err)
		}
		cloned.Proxy = nil
		// 优先使用支持 context 的拨号，使请求超时能取消底层 SOCKS 连接。
		if contextDialer, ok := socksDialer.(proxy.ContextDialer); ok {
			cloned.DialContext = contextDialer.DialContext
		} else {
			cloned.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return socksDialer.Dial(network, addr)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
	}

	return &http.Client{Transport: cloned, CheckRedirect: upstreamCheckRedirect}, nil
}
