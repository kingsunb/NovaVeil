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

// GenerateMessageID 生成 x-opencode-request 头所需的消息 ID,
// 格式 msg_ + 12 位十六进制 + 14 位字母数字, 与 opencode 1.x 客户端的消息 ID 同形。
// 两档都注入: 实抓官方 v2 客户端同样发送此头(旧假设"v2 网关对此头 403"已被实测推翻)。
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
// 注意: 官方客户端在 x-opencode-request 上发送的是 msg_ 形态(实抓确认, 两档都发),
// req_ 形态与之不符, 该函数保留供内部关联/日志使用, 不注入上游请求头。
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
// v1Lane=true 按小时锚(v1 原始策略); false 按天锚(v2: UTC 日−哈希偏移至多 30 天)。
// 同一信号恒定派生同一号, 时间新鲜度与确定性兼得。
func SessionIDFromSignal(signal string, v1Lane bool) string {
	if officialSessionPattern.MatchString(signal) && descendingTimePartPattern.MatchString(signal[4:16]) {
		return signal
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	var inverted uint64
	if v1Lane {
		anchor := uint64(time.Now().UnixMilli()/sessionAnchorMillis*sessionAnchorMillis) << 12
		mixed := anchor | uint64(sum[5])&0xfff
		inverted = ^mixed & 0xFFFFFFFFFFFF
	} else {
		offsetDays := uint64(sum[5]%31) * sessionDayMillis
		anchor := (uint64(time.Now().UTC().Truncate(24*time.Hour).UnixMilli()) - offsetDays) << 12
		inverted = ^anchor & 0xFFFFFFFFFFFF
	}
	hexPart := hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
	return "ses_" + hexPart + base62Fixed(sum[6:16], 14)
}

const sessionAnchorMillis = 3600 * 1000

const sessionDayMillis = 24 * 3600 * 1000

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
