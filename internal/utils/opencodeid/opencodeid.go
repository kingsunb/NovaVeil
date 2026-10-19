// Package opencodeid 生成与 opencode 二进制一致格式的会话 ID（ses_<12 hex><14 alnum>）。
//
// opencode.ai/zen 上游会校验 x-opencode-session 的值格式，非 opencode 格式的值会被拒绝。
// 转发路径（internal/relay）与模型同步/探测路径（internal/helper）都需要注入该头，
// 因此算法抽到本公共包，避免两处实现漂移。
package opencodeid

import (
	"crypto/rand"
	"crypto/sha1"
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
	return generateOpencodePrefixedID("ses_")
}

// GenerateMessageID 生成 v1 档(/zen) x-opencode-request 头所需的递增消息 ID,
// 格式 msg_ + 12 位十六进制 + 14 位字母数字, 与 opencode 1.x 客户端的消息 ID 同形。
// opencode2api 在 v1 档每个模型请求上发送此头(递增 msg_ id); v2 档(/inference)不发
// (v2 网关对 x-opencode-request 返回 403 FreeTierError)。
func GenerateMessageID() string {
	return generateOpencodePrefixedID("msg_")
}

// generateOpencodePrefixedID 生成 <prefix> + 12 位十六进制 + 14 位字母数字的 ID,
// 复用 opencode 的毫秒计数器保证同毫秒内递增。会话 ID(ses_)与 v1 请求消息 ID(msg_)共用,
// 保证两处注入值格式与递增性一致。算法逆向自 opencode 二进制中的 tU(!0) 函数。
func generateOpencodePrefixedID(prefix string) string {
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

	// ID 使用按位取反(^), 与 opencode 的 tU(true) → ~$ 一致。
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

// defaultProjectSignal 是固定默认项目信号, 与 opencode 官方客户端的 directory-based
// project ID 语义对齐: 无项目信号的请求共享同一稳定项目 ID。
const defaultProjectSignal = "directory:novaveil:default-project"

// projectIDHash 预先计算固定默认项目信号的 SHA-1, 生成裸 40-hex 项目 ID。
// opencode v2 官方客户端用 Hash.fast = SHA-1 生成项目 ID(packages/util/src/hash.ts),
// 格式为裸 40-hex, 不带 prj_ 前缀(packages/schema/src/project-id.ts 无前缀)。
var projectIDHash = sha1.Sum([]byte(defaultProjectSignal))

// ProjectID 返回 opencode 关联项目 ID, 格式为裸 40-hex(SHA-1),
// 对所有请求稳定不变。与 opencode v2 官方客户端的 directory-based project ID 格式一致。
func ProjectID() string {
	return hex.EncodeToString(projectIDHash[:])
}
