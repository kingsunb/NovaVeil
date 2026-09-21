package helper

import (
	"net/http"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/client"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// ChannelHttpClient 根据渠道代理配置创建 HTTP 客户端, 并以 Debug 日志标注每次出站
// 实际使用的代理: 渠道专属代理输出完整地址(密码打码, 含 {account} 解析出的别名),
// 便于在日志中按渠道与别名追踪每个请求的出口。该函数挂在每请求热路径上,
// 日志降为 Debug 级别, 避免 Info 级别下高频刷屏与 url.Parse 开销。
func ChannelHttpClient(channel *model.Channel) (*http.Client, error) {
	if !channel.Proxy {
		if log.GetLevel() <= log.DebugLevel {
			log.Debugf("channel %d(%s) outbound direct: proxy disabled", channel.ID, channel.Name)
		}
		return client.GetHTTPClientSystemProxy(false)
	}
	if channel.ChannelProxy == nil || strings.TrimSpace(*channel.ChannelProxy) == "" {
		if log.GetLevel() <= log.DebugLevel {
			log.Debugf("channel %d(%s) outbound via system proxy", channel.ID, channel.Name)
		}
		return client.GetHTTPClientSystemProxy(true)
	}
	addr := strings.TrimSpace(*channel.ChannelProxy)
	if log.GetLevel() <= log.DebugLevel {
		log.Debugf("channel %d(%s) outbound via channel proxy: %s", channel.ID, channel.Name, client.MaskProxySecret(addr))
	}
	return client.GetHTTPClientCustomProxy(addr)
}
