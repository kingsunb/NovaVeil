package relay

import (
	"errors"
	"unicode/utf8"
)

// errContextWindowTooSmall 表示全部候选成员的窗口都装不下本请求。
var errContextWindowTooSmall = errors.New("没有候选模型的上下文窗口能容纳当前请求")

// estimateRequestTokens 按字符宽度粗估请求 token 数: 非 ASCII 字按 1, ASCII 每 4 字按 1。
// 不引分词器是因为各上游编码表不同, 这里只用于排除明显装不下的成员。
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

// contextFitsWindow 报告请求能否装进窗口; 窗口未知(<=0)时一律放行。
func contextFitsWindow(body []byte, contextWindow int) bool {
	if contextWindow <= 0 {
		return true
	}
	return estimateRequestTokens(body) <= contextWindow
}
