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

// tokenDensityCache 缓存「上游端点:模型」的 tokenizer 密度比:
// 上游 usage 真实 token / 基准口径估算值。探测校准写入, 路由过滤与探测构造共用,
// 保证「测试判定」与「路由过滤」对同一模型的估算同尺。
// 仅白名单端点(tokenDensityEnabledBaseURLs, 即 usage 口径虚高的 OpenCode 免费档)
// 参与读写; 其余上游一律基准口径, 不校准不修正。
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

// tokenDensityEnabledBaseURLs 是 usage 口径与真实不符、必须校准补偿的上游端点。
// 实测 opencode.ai/inference(OpenCode 免费档)报的 prompt_tokens 虚高约一倍,
// 且同端点不同请求报告还会波动; 其余上游 usage 与基准口径一致, 一律不校准。
var tokenDensityEnabledBaseURLs = map[string]bool{
	"https://opencode.ai/inference": true,
}

// tokenDensityApplies 报告该端点是否启用密度校准。
func tokenDensityApplies(baseURL string) bool {
	return tokenDensityEnabledBaseURLs[strings.TrimRight(baseURL, "/")]
}

// modelTokenDensity 返回已知密度比; 未启用校准的端点恒为 1(即基准口径)。
func modelTokenDensity(baseURL, modelName string) float64 {
	if !tokenDensityApplies(baseURL) {
		return 1
	}
	if cached, ok := tokenDensityCache.Load(tokenDensityKey(baseURL, modelName)); ok {
		if density, ok := cached.(float64); ok && density > 0 {
			return density
		}
	}
	return 1
}

// estimateModelTokens 按「上游主机+模型」的已知密度估算请求 token 数。
// 密度比由探测校准写入: 真实请求与探测填充文本虽分布不同, 但同模型的 tokenizer
// 压缩率远比固定口径稳定, 用它修正远好于不修正。
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
// 估算与上下文探测共用基准口径和密度比, 探测说装得下的, 路由判定同尺。
func contextFitsWindow(body []byte, contextWindow int, baseURL, modelName string) bool {
	if contextWindow <= 0 {
		return true
	}
	return estimateModelTokens(body, baseURL, modelName) <= contextWindow
}
