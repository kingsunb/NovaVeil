package relay

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// errContextWindowTooSmall 表示全部候选成员的窗口都装不下本请求。
var errContextWindowTooSmall = errors.New("没有候选模型的上下文窗口能容纳当前请求")

// estimateRequestTokens 按字符宽度粗估请求 token 数: 非 ASCII 字按 1, ASCII 每 4 字按 1。
// 这是两侧共用的基准口径: 探测填充文本几乎全为 ASCII, 其基准值即 chars/4,
// 与「4 字符约 1 token」的探测口径天然一致; 路由过滤对真实请求也用同一函数。
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

// tokenDensityCache 缓存「渠道:模型」的 tokenizer 密度比:
// 上游 usage 真实 token / 基准口径估算值。探测校准写入, 路由过滤与探测构造共用,
// 保证「测试判定」与「路由过滤」对同一模型的估算同尺; 无校准数据时按 1(基准口径)处理。
var tokenDensityCache sync.Map

func tokenDensityKey(channelID int, modelName string) string {
	return fmt.Sprintf("%d:%s", channelID, modelName)
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

// modelTokenDensity 返回已知密度比, 未知时 1(即基准口径)。
func modelTokenDensity(channelID int, modelName string) float64 {
	if cached, ok := tokenDensityCache.Load(tokenDensityKey(channelID, modelName)); ok {
		if density, ok := cached.(float64); ok && density > 0 {
			return density
		}
	}
	return 1
}

// estimateModelTokens 按「渠道+模型」的已知密度估算请求 token 数。
// 密度比由探测校准写入: 真实请求与探测填充文本虽分布不同, 但同模型的 tokenizer
// 压缩率远比固定口径稳定, 用它修正远好于不修正。
func estimateModelTokens(body []byte, channelID int, modelName string) int {
	base := estimateRequestTokens(body)
	if base <= 0 {
		return 0
	}
	scaled := int(float64(base) * modelTokenDensity(channelID, modelName))
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// contextFitsWindow 报告请求能否装进窗口; 窗口未知(<=0)时一律放行。
// 估算与上下文探测共用基准口径和密度比, 探测说装得下的, 路由判定同尺。
func contextFitsWindow(body []byte, contextWindow, channelID int, modelName string) bool {
	if contextWindow <= 0 {
		return true
	}
	return estimateModelTokens(body, channelID, modelName) <= contextWindow
}
