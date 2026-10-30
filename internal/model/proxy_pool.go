package model

import "strings"

// ProxyEntry 代理池中的单个代理条目。
type ProxyEntry struct {
	ID      string `json:"id"`      // 唯一标识, 前端生成(UUID), 用于 React key 与删除定位。
	Name    string `json:"name"`    // 人类可读名称, 如 "香港-01"。
	URL     string `json:"url"`     // 代理地址, 支持 http/https/socks/socks5/socks5h。
	Enabled bool   `json:"enabled"` // 是否启用。
}

// 代理池条目数量上限, 防止误配置撑大存储与前端渲染。
const MaxProxyPoolCount = 64

// 代理条目名称与 URL 的长度上限。
const (
	MaxProxyNameLen = 100
	MaxProxyURLLen  = 2048
)

// AccountPlaceholder 渠道专属代理模板中的账号占位符, 使用某把 Key 时替换为该 Key 的生成别名。
// 代理池条目最终会作为渠道代理使用, 因此代理池同样支持该占位符。
const AccountPlaceholder = "{account}"

// WarpProxyMarker 是 WARP 代理容器的服务名标识; 渠道代理含此串即走槽位分支。
const WarpProxyMarker = "NovaVeil_WARP_socks5"

// AccountPlaceholderEscape 是 AccountPlaceholder 在 URL 中的等价百分号转义形式:
// { 与 } 不在 net/url 允许的 userinfo 字符集内, 必须先按百分号转义占位符才能通过 url.Parse;
// 解析完成后 User.Username() 返回的是解码还原的字面占位符, 再做编程式替换。
const AccountPlaceholderEscape = "%7Baccount%7D"

// HasAccountPlaceholder 报告代理模板是否含账号占位符, 兼容百分号转义形式。
func HasAccountPlaceholder(template string) bool {
	if strings.Contains(template, AccountPlaceholder) {
		return true
	}
	// 百分号转义的十六进制大小写等价。
	for _, escaped := range []string{AccountPlaceholderEscape, "%7baccount%7d", "%7Baccount%7d", "%7baccount%7D"} {
		if strings.Contains(template, escaped) {
			return true
		}
	}
	return false
}
