package eval

import (
	"net/http"

	"github.com/kingsunb/NovaVeil/internal/relay"
)

// isFatalEvalStatus 判断评估失败是否为确定性失效: 上游返回 401/403/404。
// 这些状态码表示凭据失效或模型不存在, 重试不会变好, 可触发免费渠道模型自动清理;
// 429/限流/5xx/网络/超时等瞬时或非必然失败不会命中(其无状态码或状态码不在白名单内)。
func isFatalEvalStatus(err error) bool {
	code, ok := relay.UpstreamStatusCode(err)
	if !ok {
		return false
	}
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound
}
