// 移植自 magpie internal/redact/crypto.go (MIT, Copyright (c) 2026 yetone).
// 11 条加密钱包私钥脱敏规则: 正则锁形状, 校验函数锁真实性(base58check / ed25519 / CRC16).
// 低层 helper 以 crypto 前缀命名, 避免与同包其他规则族移植文件撞名.
package mask

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// CryptoRules 加密钱包私钥规则, 默认全关.
var CryptoRules = []Rule{
	{
		Label:       "WIF_KEY",
		Pattern:     regexp.MustCompile(`[1-9A-HJ-NP-Za-km-z]{51,}`),
		Group:       0,
		Description: "Bitcoin/Litecoin/Dogecoin WIF 私钥(base58check 校验和)",
	},
	{
		Label:       "BIP32_KEY",
		Pattern:     regexp.MustCompile(`[xyztuvYZUV]prv[1-9A-HJ-NP-Za-km-z]{107}`),
		Group:       0,
		Description: "BIP32 扩展私钥 xprv/yprv/zprv/tprv 等(78 字节 + base58check)",
	},
	{
		Label:       "SOLANA_KEY",
		Pattern:     regexp.MustCompile(`[1-9A-HJ-NP-Za-km-z]{85,}`),
		Group:       0,
		Description: "Solana 64 字节 ed25519 私钥(后半 32 字节为前半公钥)",
	},
	{
		Label:       "SOLANA_KEYPAIR",
		Pattern:     regexp.MustCompile(`\[\s*\d{1,3}(?:\s*,\s*\d{1,3}){63}\s*\]`),
		Group:       0,
		Description: "Solana keyfile 64 字节数组(ed25519 种子 + 公钥)",
	},
	{
		Label:       "STELLAR_KEY",
		Pattern:     regexp.MustCompile(`S[A-Z2-7]{55}`),
		Group:       0,
		Description: "Stellar 秘密种子 StrKey(version byte + CRC16)",
	},
	{
		Label:       "SUI_KEY",
		Pattern:     regexp.MustCompile(`suiprivkey1[0-9a-z]{58}`),
		Group:       0,
		Description: "Sui 私钥 bech32(suiprivkey1 前缀唯一, 无校验)",
	},
	{
		Label:       "APTOS_KEY",
		Pattern:     regexp.MustCompile(`(?:ed25519|secp256k1)-priv-0x[0-9a-fA-F]{64}`),
		Group:       0,
		Description: "Aptos AIP-80 私钥(ed25519/secp256k1-priv-0x 前缀唯一)",
	},
	{
		Label:       "NOSTR_KEY",
		Pattern:     regexp.MustCompile(`nsec1[02-9ac-hj-np-z]{58}`),
		Group:       0,
		Description: "Nostr NIP-19 私钥 nsec1 bech32(前缀唯一)",
	},
	{
		Label:       "AGE_KEY",
		Pattern:     regexp.MustCompile(`AGE-SECRET-KEY-1[0-9A-Z]{58}`),
		Group:       0,
		Description: "age 身份密钥 AGE-SECRET-KEY-1 bech32(前缀唯一)",
	},
	{
		Label:       "ZCASH_KEY",
		Pattern:     regexp.MustCompile(`secret-extended-key-(?:main|test)1[0-9a-z]{150,320}`),
		Group:       0,
		Description: "Zcash 扩展花费密钥 main/test(前缀唯一)",
	},
	{
		Label:       "NEAR_KEY",
		Pattern:     regexp.MustCompile(`ed25519:[1-9A-HJ-NP-Za-km-z]{86,88}`),
		Group:       0,
		Description: "NEAR ed25519 64 字节私钥 base58(前缀唯一)",
	},
}

// CryptoRuleMeta 规则元信息, 全部默认关闭(出厂全关硬约束).
var CryptoRuleMeta = []RuleMeta{
	{"WIF_KEY", "Bitcoin WIF 私钥", false},
	{"BIP32_KEY", "BIP32 扩展私钥", false},
	{"SOLANA_KEY", "Solana ed25519 私钥", false},
	{"SOLANA_KEYPAIR", "Solana keyfile 数组", false},
	{"STELLAR_KEY", "Stellar 秘密种子", false},
	{"SUI_KEY", "Sui 私钥", false},
	{"APTOS_KEY", "Aptos 私钥", false},
	{"NOSTR_KEY", "Nostr 私钥", false},
	{"AGE_KEY", "age 身份密钥", false},
	{"ZCASH_KEY", "Zcash 扩展花费密钥", false},
	{"NEAR_KEY", "NEAR ed25519 私钥", false},
}

// CryptoValidators 按标签索引的校验函数. 前缀唯一的规则不在此表, 正则命中即脱敏.
var CryptoValidators = map[string]Validator{
	"WIF_KEY":        wifKeyOK,
	"BIP32_KEY":      bip32KeyOK,
	"SOLANA_KEY":     solanaKeyOK,
	"SOLANA_KEYPAIR": solanaKeypairOK,
	"STELLAR_KEY":    stellarSeedOK,
}

// CryptoMayHit 特征预检: text 不含该标签必含特征时返回 false, 引擎据此跳过正则.
// 未知标签返回 true(保守放行, 由正则与校验兜底).
func CryptoMayHit(text, label string) bool {
	switch label {
	case "WIF_KEY":
		return cryptoB58Run(text, 51)
	case "BIP32_KEY":
		return strings.Contains(text, "prv")
	case "SOLANA_KEY":
		return cryptoB58Run(text, 85)
	case "SOLANA_KEYPAIR":
		return strings.Contains(text, "[")
	case "STELLAR_KEY":
		return cryptoBase32Run(text, 56)
	case "SUI_KEY":
		return strings.Contains(text, "suiprivkey1")
	case "APTOS_KEY":
		return strings.Contains(text, "-priv-0x")
	case "NOSTR_KEY":
		return strings.Contains(text, "nsec1")
	case "AGE_KEY":
		return strings.Contains(text, "AGE-SECRET-KEY-")
	case "ZCASH_KEY":
		return strings.Contains(text, "secret-extended-key-")
	case "NEAR_KEY":
		return strings.Contains(text, "ed25519:")
	default:
		return true
	}
}

// ---- base58 / base58check, 译自 magpie crypto.go:169-214 ----

// cryptoB58Alphabet base58 字母表(Bitcoin).
const cryptoB58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// cryptoB58Index 字节到 base58 数值的反查表, 非法字符为 -1.
var cryptoB58Index = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(cryptoB58Alphabet); i++ {
		t[cryptoB58Alphabet[i]] = int8(i)
	}
	return t
}()

// cryptoBase58 解码 base58; 含非法字符返回 nil.
func cryptoBase58(s string) []byte {
	n := new(big.Int)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		d := cryptoB58Index[s[i]]
		if d < 0 {
			return nil
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(d)))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), n.Bytes()...)
}

// cryptoBase58Check 校验 base58check 双 sha256 尾 4 字节校验和, 返回 payload; 不通过返回 nil.
func cryptoBase58Check(s string) []byte {
	b := cryptoBase58(s)
	if len(b) < 5 {
		return nil
	}
	p, sum := b[:len(b)-4], b[len(b)-4:]
	h := sha256.Sum256(p)
	h = sha256.Sum256(h[:])
	if !bytes.Equal(h[:4], sum) {
		return nil
	}
	return p
}

// ---- 校验函数(防误报), 译自 magpie crypto.go:218-292 ----

// wifKeyOK WIF 私钥: 版本字节 + 32 字节 + 可选 0x01 压缩标记, base58check 校验和.
// 防任意 51+ base58 串(短 hash/随机串)误判为 WIF.
func wifKeyOK(v string) bool {
	if len(v) != 51 && len(v) != 52 {
		return false
	}
	p := cryptoBase58Check(v)
	return len(p) == 33 || len(p) == 34 && p[33] == 1
}

// bip32KeyOK BIP32 扩展私钥: 78 字节 + base58check. 防普通 base58 串误判.
func bip32KeyOK(v string) bool {
	return len(cryptoBase58Check(v)) == 78
}

// solanaKeyOK Solana 64 字节 ed25519 私钥: 前 32 字节种子, 后 32 字节为其公钥.
// 防同长 88 字符的 Solana 交易签名(也 64 字节)误判.
func solanaKeyOK(v string) bool {
	if len(v) < 85 || len(v) > 88 {
		return false
	}
	return cryptoSeedAndPublic(cryptoBase58(v))
}

// cryptoSeedAndPublic b 为 32 字节种子 + ed25519 公钥, 校验公钥由种子派生.
func cryptoSeedAndPublic(b []byte) bool {
	if len(b) != ed25519.PrivateKeySize {
		return false
	}
	return bytes.Equal(ed25519.NewKeyFromSeed(b[:ed25519.SeedSize]), b)
}

// solanaKeypairOK Solana keyfile: 64 个 0-255 数值, 合为种子 + 公钥.
// 防任意 64 数值数组(如 sha512 摘要)误判.
func solanaKeypairOK(v string) bool {
	parts := strings.Split(strings.Trim(v, "[] \t\r\n"), ",")
	if len(parts) != ed25519.PrivateKeySize {
		return false
	}
	b := make([]byte, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n > 255 {
			return false
		}
		b[i] = byte(n)
	}
	return cryptoSeedAndPublic(b)
}

// stellarSeedOK Stellar 秘密种子 StrKey: version byte 18<<3 + 32 字节 + CRC16-XModem.
// 防任意 S 开头 base32 串误判.
func stellarSeedOK(v string) bool {
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(v)
	if err != nil || len(b) != 35 || b[0] != 18<<3 {
		return false
	}
	c := cryptoCRC16XModem(b[:33])
	return b[33] == byte(c) && b[34] == byte(c>>8)
}

// cryptoCRC16XModem CRC16-XModem 校验和(Stellar StrKey 用).
func cryptoCRC16XModem(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// ---- 特征预检 helper, 译自 magpie crypto.go:363-394 ----

// cryptoB58Run text 是否含连续 n 个 base58 字符. 字节扫描替代无前缀正则全量跑.
func cryptoB58Run(s string, n int) bool {
	run := 0
	for i := 0; i < len(s); i++ {
		if cryptoB58Index[s[i]] >= 0 {
			if run++; run >= n {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// cryptoBase32Run text 是否含连续 n 个 RFC4648 base32 大写字符(Stellar 种子).
func cryptoBase32Run(s string, n int) bool {
	run := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' || c >= '2' && c <= '7' {
			if run++; run >= n {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
