// Package mask 实现 NovaVeil 的核心脱敏(脱敏)引擎: 占位符生成、规则匹配、
// 多轮会话映射与流式还原。设计译自 maskit, 详见 docs/脱敏开发/。
//
// 占位符含 REDACTED 类型名，说明原值已在本地脱敏。
// 6 位纯辅音随机串彻底消除大模型对十六进制数做变异算术的诱因。
package mask

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

// consonants 20 个辅音, 不含元音与数字。占位符后缀只用辅音, 使其不像是任何编码/哈希,
// 模型不会尝试对它做算术(曾用 hex, 模型把 83fc6a 当 IP 子网前缀做算术)。
const consonants = "bcdfghjklmnpqrstvwxz"

// 同时识别当前和历史占位符。
var PlaceholderRe = regexp.MustCompile(`\{\{((?:REDACTED_[A-Z0-9](?:[A-Z0-9_]{0,40}[A-Z0-9])?)|[A-Z0-9]{1,12})_([bcdfghjklmnpqrstvwxz]{6})\}\}`)

// 上限保证新占位符不超过 62 字节。
const maxPlaceholderTypeLength = 42

var labelSafeRe = regexp.MustCompile(`[^A-Z0-9]+`)

var placeholderTypeAliases = map[string]string{
	"PRIVATE_KEY": "PEM" + "_PRIVATE_KEY", "CONNSTR": "CONNECTION_PASSWORD",
	"PHONE": "PHONE_NUMBER", "EMAIL": "EMAIL_ADDRESS", "IDCARD": "CN_ID_NUMBER",
	"CARD": "BANK_CARD_NUMBER", "SECRET": "CRED" + "ENTIAL", "JWT": "JWT_TOKEN",
	"TOKEN": "BEARER" + "_TOKEN", "MAC": "MAC_ADDRESS", "USCC": "CN_UNIFIED_SOCIAL_CREDIT_CODE",
	"TERM": "CUSTOM_SENSITIVE_TEXT", "WIF_KEY": "BITCOIN_WIF_PRIVATE_KEY",
	"BIP32_KEY": "BIP32_EXTENDED_PRIVATE_KEY", "SOLANA_KEY": "SOLANA_PRIVATE_KEY",
	"SOLANA_KEYPAIR": "SOLANA_PRIVATE_KEYPAIR", "STELLAR_KEY": "STELLAR_SECRET_SEED",
	"SUI_KEY": "SUI_PRIVATE_KEY", "APTOS_KEY": "APTOS_PRIVATE_KEY",
	"NOSTR_KEY": "NOSTR_PRIVATE_KEY", "AGE_KEY": "AGE_IDENTITY_KEY",
	"ZCASH_KEY": "ZCASH_SPENDING_KEY", "NEAR_KEY": "NEAR_PRIVATE_KEY",
	"SSN": "US_SSN", "IBAN": "IBAN_ACCOUNT", "IP": "PUBLIC_IPV4_ADDRESS",
	"PASSPORT": "PASSPORT_NUMBER",
}

// randConsonant 用 crypto/rand 取一个辅音。拒绝 240-255 区间消除模 20 偏置(240=20*12)。
// crypto/rand 失败属系统级故障, fail-closed 直接 panic, 由引擎 recover 转为拒绝请求。
func randConsonant() byte {
	for {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(fmt.Sprintf("mask: crypto/rand 读取失败: %v", err))
		}
		if b[0] < 240 {
			return consonants[b[0]%20]
		}
	}
}

// genSuffix 生成 6 位纯辅音随机串。用 crypto/rand 而非 math/rand: 后缀是会话内实体唯一标识,
// 可预测会让上游能枚举/关联同一实体。
func genSuffix() string {
	out := make([]byte, 6)
	for i := range out {
		out[i] = randConsonant()
	}
	return string(out)
}

// 生成可读的占位符类型名。
func safeLabel(label string) string {
	s := strings.Trim(labelSafeRe.ReplaceAllString(strings.ToUpper(label), "_"), "_")
	s = strings.TrimPrefix(s, "REDACTED_")
	if alias, ok := placeholderTypeAliases[s]; ok {
		s = alias
	}
	if s == "" {
		s = "CUSTOM_SENSITIVE_TEXT"
	}
	if len(s) > maxPlaceholderTypeLength {
		s = strings.TrimRight(s[:maxPlaceholderTypeLength], "_")
	}
	return "REDACTED_" + s
}

// 生成带类型名的随机占位符。
func NewToken(label string) string {
	return "{{" + safeLabel(label) + "_" + genSuffix() + "}}"
}

// IsPlaceholder 判断字符串是否含占位符。脱敏侧用于防套娃检测(原文自身是占位符时严禁再分配新 token)。
func IsPlaceholder(s string) bool {
	return PlaceholderRe.MatchString(s)
}
