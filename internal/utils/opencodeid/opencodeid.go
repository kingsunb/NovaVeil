// Package opencodeid 生成与 opencode 二进制一致格式的会话 ID（ses_<12 hex><14 alnum>）。
//
// opencode.ai/zen 上游会校验 x-opencode-session 的值格式，非 opencode 格式的值会被拒绝。
// 转发路径（internal/relay）与模型同步/探测路径（internal/helper）都需要注入该头，
// 因此算法抽到本公共包，避免两处实现漂移。
package opencodeid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"regexp"
	"sync/atomic"
	"time"
)

// opencodeIDCharset opencode ID 随机后缀使用的字符集: [0-9A-Za-z], 共 62 个字符。
// 与 opencode 二进制中的字符表完全一致。
const opencodeIDCharset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// opencodeIDCounter 与 opencodeIDTimestamp 实现 opencode 的 ID 计数器:
// 同一毫秒内多次生成时计数器递增, 跨毫秒时重置为 0。与 opencode 的 tU() 函数行为一致。
var opencodeIDCounter atomic.Int64
var opencodeIDTimestamp atomic.Int64

// GenerateSessionID 生成 opencode 格式的会话 ID:
//   - 前缀 "ses_"
//   - 12 个十六进制字符: 由 ~(timestamp_ms * 4096 + counter) 的低 6 字节(大端)编码
//   - 14 个随机字符: 从 [0-9A-Za-z] 中选取
//
// 会话 ID 用 descending 编码(时间部分取反), 与 opencode 的 Identifier.descending() 一致,
// 算法逆向自 opencode 二进制中的 tU(!0) 函数。
func GenerateSessionID() string {
	return generateOpencodePrefixedID("ses_", true)
}

// GenerateMessageID 生成 v1 档(/zen) x-opencode-request 头所需的消息 ID,
// 格式 msg_ + 12 位十六进制 + 14 位字母数字, 与 opencode 1.x 客户端的消息 ID 同形。
// opencode2api 在 v1 档每个模型请求上发送此头(递增 msg_ id); v2 档(/inference)不发
// (v2 网关对 x-opencode-request 返回 403 FreeTierError)。
//
// 消息 ID 用 ascending 编码(时间部分不取反), 与 opencode 的 Identifier.ascending() 一致:
// 官方客户端只有会话 ID 走 descending, 消息 ID 是升序。取反后时间戳方向相反, 解出来的
// 时间会落在未来并把同一会话内的消息序颠倒, 属于可被上游识别的编码差异。
func GenerateMessageID() string {
	return generateOpencodePrefixedID("msg_", false)
}

// generateOpencodePrefixedID 生成 <prefix> + 12 位十六进制 + 14 位字母数字的 ID,
// 复用 opencode 的毫秒计数器保证同毫秒内递增。descending 决定时间部分的编码方向:
// true 取反(会话 id), false 保持升序(消息 id)。
func generateOpencodePrefixedID(prefix string, descending bool) string {
	now := time.Now().UnixMilli()

	// 计数器管理: 时间戳变更时重置, 每次调用递增。与 opencode 的 tU() 行为一致。
	stamp := opencodeIDTimestamp.Load()
	if stamp != now {
		if opencodeIDTimestamp.CompareAndSwap(stamp, now) {
			opencodeIDCounter.Store(0)
		}
	}
	counter := opencodeIDCounter.Add(1)

	// val = timestamp_ms * 4096 + counter, 与 opencode 的 BigInt(Y)*0x1000n+BigInt(cU) 一致。
	val := now*0x1000 + counter

	// descending 取反, 与 opencode 的 tU(true) → ~$ 一致; ascending 原样保留。
	// 统一换算成 uint64 再移位, 避免负数算术右移的符号扩展参与取字节。
	encoded := uint64(val)
	if descending {
		encoded = uint64(^val)
	}

	// 提取低 6 字节(大端序) → 12 个十六进制字符。
	buf := make([]byte, 6)
	for i := 0; i < 6; i++ {
		buf[i] = byte((encoded >> uint(40-8*i)) & 0xff)
	}
	hexPart := hex.EncodeToString(buf)

	// 生成 14 个随机字符, 从 [0-9A-Za-z] 中选取。
	randBuf := make([]byte, 14)
	if _, err := rand.Read(randBuf); err != nil {
		// crypto/rand 失败时的降级: 用时间戳填充, 极低概率发生。
		ts := time.Now().UnixNano()
		for i := range randBuf {
			randBuf[i] = byte(ts >> uint((i*8)%64))
		}
	}
	randPart := make([]byte, 14)
	for i, b := range randBuf {
		randPart[i] = opencodeIDCharset[b%62]
	}

	return prefix + hexPart + string(randPart)
}

// GenerateRequestID 生成每请求唯一的 opencode 关联请求 ID, 格式 req_<32 hex>。
// 注意: opencode v2 官方客户端不发送 x-opencode-request 头(客户端发送路径无此头,
// 见 packages/core/src/session/model-request.ts:282-292), 该函数保留供内部关联/
// 日志使用, 但不再注入到发往 opencode.ai 的上游请求头中。
func GenerateRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败时的降级: 用纳秒时间戳填充, 极低概率发生。
		ts := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(ts >> uint((i*8)%64))
		}
	}
	return "req_" + hex.EncodeToString(buf)
}

// globalProjectID 是无 Git 项目号。
const globalProjectID = "global"

// officialSessionPattern 等识别官方会话号形态。
var (
	officialSessionPattern    = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	descendingTimePartPattern = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)
	projectIDPattern          = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// SessionIDFromSignal 由信号确定性派生官方 ses_ 会话号。
func SessionIDFromSignal(signal string) string {
	if officialSessionPattern.MatchString(signal) && descendingTimePartPattern.MatchString(signal[4:16]) {
		return signal
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	return "ses_" + descendingTimePart(sum[:6]) + base62Fixed(sum[6:16], 14)
}

// descendingTimePart 取反 48 位时间部分, 落在官方 descending 形态。
func descendingTimePart(hash []byte) string {
	current := (uint64(hash[0])<<28 | uint64(hash[1])<<20 | uint64(hash[2])<<12 |
		uint64(hash[3])<<4 | uint64(hash[4])>>4) << 12
	current |= 1
	inverted := ^current & 0xFFFFFFFFFFFF
	return hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
}

// base62Fixed 把字节串编码成定宽 Base62。
func base62Fixed(data []byte, width int) string {
	n := new(big.Int).SetBytes(data)
	base := big.NewInt(62)
	out := make([]byte, width)
	remainder := new(big.Int)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, remainder)
		out[i] = opencodeIDCharset[remainder.Int64()]
	}
	return string(out)
}

// ProjectIDForLane 保留合法值, 否则回退 global。
func ProjectIDForLane(signal string, _ bool) string {
	if signal == globalProjectID || projectIDPattern.MatchString(signal) {
		return signal
	}
	return globalProjectID
}
