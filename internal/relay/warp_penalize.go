package relay

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// warpSocksProxyMarker 是 WARP SOCKS5 容器的服务名标识。
// NovaVeil 靠渠道代理地址包含此串来判断是否走 WARP 反应式淘汰。
const warpSocksProxyMarker = "NovaVeil_WARP_socks5"

// isWarpSocksProxy 报告渠道代理是否指向 WARP SOCKS5 容器。
func isWarpSocksProxy(channel model.Channel) bool {
	if channel.ChannelProxy == nil {
		return false
	}
	return strings.Contains(*channel.ChannelProxy, warpSocksProxyMarker)
}

// warpPenalizeEndpoint 从渠道代理地址推导控制 API 的 penalize 端点。
// socks5://NovaVeil_WARP_socks5:1080 → http://NovaVeil_WARP_socks5:1081/penalize
func warpPenalizeEndpoint(channel model.Channel) string {
	if channel.ChannelProxy == nil {
		return ""
	}
	u, err := url.Parse(*channel.ChannelProxy)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "http://" + u.Hostname() + ":1081/penalize"
}

// warpPenalizeExit 异步通知 WARP SOCKS5 容器淘汰当前出口 IP。
// 非阻塞 fire-and-forget：发送即返回，失败静默忽略。
func warpPenalizeExit(channel model.Channel) {
	endpoint := warpPenalizeEndpoint(channel)
	if endpoint == "" {
		return
	}
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		req, err := http.NewRequest(http.MethodPost, endpoint, nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
}
