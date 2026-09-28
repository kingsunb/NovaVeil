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
//   - 12 个十六进制字符: 由 ~(timestamp_ms * 4096 + counter) 的高 6 字节(大端)编码
//   - 14 个随机字符: 从 [0-9A-Za-z] 中选取
//
// 算法逆向自 opencode 二进制中的 tU(!0) 函数。
func GenerateSessionID() string {
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

	// 会话 ID 使用按位取反(^), 与 opencode 的 tU(true) → ~$ 一致。
	// Go 的 int64 对负数的 >> 做算术右移(符号扩展), 与 JS BigInt 行为一致。
	inverted := ^val

	// 提取高 6 字节(大端序) → 12 个十六进制字符。
	buf := make([]byte, 6)
	for i := 0; i < 6; i++ {
		buf[i] = byte((inverted >> uint(40-8*i)) & 0xff)
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

	return "ses_" + hexPart + string(randPart)
}

// GenerateRequestID 生成每请求唯一的 opencode 关联请求 ID, 格式 req_<32 hex>。
// 与 x-opencode-session 不同, 上游不校验该头格式, 也不参与免费档 403 判定(只查
// 会话号 + stream + 工具名), 仅用于上游请求关联与日志。沿用 opencode2api 网关侧
// 的随机 hex 形态, 而非 opencode 原生 req_ 形态。
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

// defaultProjectSignal 是固定默认项目信号, 与 opencode2api 的 default-project
// 兜底语义一致: 无项目信号的请求共享同一稳定项目 ID。
const defaultProjectSignal = "novaveil:default-project"

// projectIDHash 预先计算固定默认项目信号的 SHA-256 前 12 字节, 避免每次请求重复哈希。
var projectIDHash = sha256.Sum256([]byte("prj\x00" + defaultProjectSignal))

// ProjectID 返回 opencode 关联项目 ID, 格式 prj_<24 hex>(SHA-256 前 12 字节),
// 对所有请求稳定不变。同样不参与免费档判定, 仅为上游请求关联。
func ProjectID() string {
	return "prj_" + hex.EncodeToString(projectIDHash[:12])
}
