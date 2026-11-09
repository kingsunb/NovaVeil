package cmd

import (
	"net"
	"strconv"
	"strings"
)

// desktopURL 监听地址转本机浏览器可访问地址。
func desktopURL(host string, port int) string {
	h := strings.TrimSpace(host)
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		h = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(h, strconv.Itoa(port))
}
