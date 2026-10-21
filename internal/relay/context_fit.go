package relay

import (
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

// errContextWindowTooSmall 表示全部候选成员的窗口都装不下本请求。
var errContextWindowTooSmall = errors.New("没有候选模型的上下文窗口能容纳当前请求")

// estimateRequestTokens 按字符宽度粗估请求 token 数: 非 ASCII 字按 1, ASCII 每 4 字按 1。
// 全渠道共用这一基准口径; 探测实测出的密度比统一在 estimateModelTokens 叠加。
func estimateRequestTokens(body []byte) int {
	narrow, wide := 0, 0
	for i := 0; i < len(body); {
		r, size := utf8.DecodeRune(body[i:])
		if r >= 0x80 {
			wide++
		} else {
			narrow++
		}
		i += size
	}
	return wide + narrow/4
}

// tokenDensityCache 缓存「上游端点:模型」的实测 token 密度比:
// 上游实际 token / 基准口径估算值。探测实测写入, 路由过滤与后续探测构造共用,
// 保证「探测判定」与「路由过滤」对同一模型的估算同尺; 未实测的模型按 1(基准口径)处理。
var tokenDensityCache sync.Map

func tokenDensityKey(baseURL, modelName string) string {
	return strings.TrimRight(baseURL, "/") + ":" + modelName
}

// clampTokenDensity 限制密度比范围, 防止异常回包导致估算爆炸或趋零。
func clampTokenDensity(d float64) float64 {
	if d < 0.25 {
		return 0.25
	}
	if d > 16 {
		return 16
	}
	return d
}

// modelTokenDensity 返回实测密度比; 未实测的模型恒为 1(基准口径)。
// 所有渠道同一规则, 不按上游类型或渠道白名单区别对待。
func modelTokenDensity(baseURL, modelName string) float64 {
	if cached, ok := tokenDensityCache.Load(tokenDensityKey(baseURL, modelName)); ok {
		if density, ok := cached.(float64); ok && density > 0 {
			return density
		}
	}
	return 1
}

// estimateModelTokens 按「上游端点+模型」的实测密度估算请求 token 数。
// 密度比由上下文探测实测写入: 未实测的模型按基准口径, 实测过的模型按实测比例修正;
// 路由过滤与探测构造共用该函数, 探测说装得下的, 路由判定同尺。
func estimateModelTokens(body []byte, baseURL, modelName string) int {
	base := estimateRequestTokens(body)
	if base <= 0 {
		return 0
	}
	scaled := int(float64(base) * modelTokenDensity(baseURL, modelName))
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// contextFitsWindow 报告请求能否装进窗口; 窗口未知(<=0)时一律放行。
func contextFitsWindow(body []byte, contextWindow int, baseURL, modelName string) bool {
	if contextWindow <= 0 {
		return true
	}
	return estimateModelTokens(body, baseURL, modelName) <= contextWindow
}
