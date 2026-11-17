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
	"strconv"
	"sync"
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
// 注意: 官方 v1 客户端在 x-opencode-request 上发送的是 msg_ 形态(1.19.5 实抓),
// v2.3.0 已不发该头; req_ 形态与之不符, 该函数保留供内部关联/日志使用。
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
// 对齐官方 2.3.0 算法(二进制反编译): 时间段 = 真实毫秒 ×4096 + 同毫秒序号,
// 位反后取 48 位 hex; 序号必须小于 256(位 11..8 为零), 否则 hex 第 10 位
// 不是 f, 上游形态校验直接 429。随机段由信号哈希确定性派生。
// 时间取信号首次解析时刻(缓存), 同一信号在进程内恒定; 缓存超龄刷新,
// 时间保持新鲜, 重启后派生新号(官方同款语义)。官方合法号原样返回。
func SessionIDFromSignal(signal string, v1Lane bool) string {
	if officialSessionPattern.MatchString(signal) && descendingTimePartPattern.MatchString(signal[4:16]) {
		return signal
	}
	sum := sha256.Sum256(saltedSessionSeed(signal))
	mixed := sessionTimePart(signal)<<12 | uint64(sum[5])&0xff
	inverted := ^mixed & 0xFFFFFFFFFFFF
	hexPart := hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
	return "ses_" + hexPart + base62Fixed(sum[6:16], 14)
}

// sessionClockMaxAge 缓存时间的最大年龄, 超龄刷新保持时间新鲜。
const sessionClockMaxAgeMillis = 3600 * 1000

// sessionDayMillis 旧窗口测试引用的天毫秒数。
const sessionDayMillis = 24 * 3600 * 1000

// sessionClockMillis 缓存信号首次解析时的毫秒时间。
func sessionClockMillis(signal string) uint64 {
	now := uint64(time.Now().UnixMilli())
	if v, ok := sessionClock.Load(signal); ok {
		if cached, _ := v.(uint64); now >= cached && now-cached < sessionClockMaxAgeMillis {
			return cached
		}
	}
	sessionClock.Store(signal, now)
	return now
}

// sessionClock 缓存每个信号的解析时间, 会话号在进程内保持稳定。
var sessionClock sync.Map

// processSalt 进程盐, 重启重铸防跨号复用。
var (
	processSaltOnce sync.Once
	processSaltBuf  [16]byte
)

// processSalt 返回进程启动后一次性铸造的 16 字节盐。
func processSalt() []byte {
	processSaltOnce.Do(func() {
		if _, err := rand.Read(processSaltBuf[:]); err != nil {
			// crypto/rand 失败降级: 纳秒时间填充, 极低概率发生。
			ts := time.Now().UnixNano()
			for i := range processSaltBuf {
				processSaltBuf[i] = byte(ts >> uint((i*8)%64))
			}
		}
	})
	return processSaltBuf[:]
}

// saltedSessionSeed 派生种子混入进程盐隔离重启。
func saltedSessionSeed(signal string) []byte {
	seed := make([]byte, 0, len(signal)+21)
	seed = append(seed, "ses\x00"...)
	seed = append(seed, processSalt()...)
	seed = append(seed, 0)
	seed = append(seed, signal...)
	return seed
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

// IsGlobalProjectID 报告是否为无项目号占位值。
func IsGlobalProjectID(value string) bool {
	return value == globalProjectID
}

// DerivedProjectID 按渠道派生稳定的 40 位 project 号。
// v2 官方恒发 40-hex 且跨会话不变, 故按渠道稳定派生。
func DerivedProjectID(channelID int) string {
	sum := sha256.Sum256([]byte("novaveil:opencode-project:" + strconv.Itoa(channelID)))
	return hex.EncodeToString(sum[:])[:40]
}
