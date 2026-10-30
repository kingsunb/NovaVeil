package relay

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// warpControlPort 是 WARP 控制 API 的固定端口。
const warpControlPort = "1081"

// errWarpProxyInvalid 表示渠道代理模板无法推导出 WARP 控制 API 地址。
var errWarpProxyInvalid = errors.New("WARP 代理地址无效")

// errWarpProxyTemplateInvalid 表示模板未把 {account} 放在用户名段。
var errWarpProxyTemplateInvalid = errors.New("WARP 代理模板必须把 {account} 放在用户名")

// isWarpProxy 报告渠道代理是否指向 WARP 容器。
func isWarpProxy(channel model.Channel) bool {
	if channel.ChannelProxy == nil {
		return false
	}
	return strings.Contains(*channel.ChannelProxy, model.WarpProxyMarker)
}

// warpControlBase 从渠道代理地址推导控制 API 根地址。
// socks5://warp-{account}:pw@NovaVeil_WARP_socks5:1080 → http://NovaVeil_WARP_socks5:1081
func warpControlBase(channel model.Channel) (string, error) {
	if channel.ChannelProxy == nil {
		return "", errWarpProxyInvalid
	}
	// 占位符不是合法 userinfo 字符, 解析前先替换为普通账号。
	raw := strings.ReplaceAll(strings.TrimSpace(*channel.ChannelProxy), model.AccountPlaceholder, "warp-slot")
	u, err := url.Parse(raw)
	if err != nil {
		return "", errWarpProxyInvalid
	}
	host := u.Hostname()
	if host == "" {
		return "", errWarpProxyInvalid
	}
	return "http://" + net.JoinHostPort(host, warpControlPort), nil
}

// warpSlotsEndpoint 返回槽位快照地址。
func warpSlotsEndpoint(channel model.Channel) (string, error) {
	base, err := warpControlBase(channel)
	if err != nil {
		return "", err
	}
	return base + "/slots", nil
}

// warpPenalizeEgress 异步通知 WARP 淘汰指定出口, 失败静默。
func warpPenalizeEgress(controlBase, egress, reason string) {
	if controlBase == "" || egress == "" {
		return
	}
	query := url.Values{"egress": []string{egress}}
	if reason != "" {
		query.Set("reason", reason)
	}
	warpPostControlAsync(controlBase, "/penalize", query)
}

// warpRotateAll 异步触发 WARP 全池轮换, 失败静默。
func warpRotateAll(controlBase string) {
	if controlBase == "" {
		return
	}
	warpPostControlAsync(controlBase, "/rotate", url.Values{"all": []string{"1"}})
}

// warpPenalizeLatest 异步淘汰最近一次拨号出口, 用于未槽位化的旧 WARP 渠道。
func warpPenalizeLatest(channel model.Channel) {
	base, err := warpControlBase(channel)
	if err != nil {
		return
	}
	warpPostControlAsync(base, "/penalize", nil)
}

// warpPostControlAsync 非阻塞调用控制 API, 发送即返回。
func warpPostControlAsync(controlBase, path string, query url.Values) {
	endpoint := controlBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	go func() {
		client := &http.Client{
			Timeout:   3 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		}
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

// isWarpProxyRejection 判断错误是否来自 WARP 代理自身的 407/503 拒绝。
// 这类错误只说明槽位暂不可用, 不能当作出口网络失败去 penalize。
func isWarpProxyRejection(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "407") ||
		strings.Contains(text, "proxy authentication required") ||
		strings.Contains(text, "connection not allowed") ||
		strings.Contains(text, "proxyconnect") {
		return true
	}
	return strings.Contains(text, "503") &&
		(strings.Contains(text, "proxy") || strings.Contains(text, "tunnel"))
}

// isWarpSlotUnavailable 识别 HTTP 代理 CONNECT 的 503 文本。
func isWarpSlotUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "service unavailable")
}
