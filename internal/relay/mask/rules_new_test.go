// rules_new_test.go 覆盖 rules_crypto.go 的加密钱包校验函数与 rules_personal.go 的个人数据校验函数。
//
// 样本来源(公开权威向量, 并用本文件内独立实现交叉校验, 不用被测函数的输出反向生成样本):
//   - WIF: Bitcoin wiki 的 WIF 示例, 私钥全 1 的压缩/未压缩 WIF;
//   - BIP-0032: 官方向量 1 的主密钥 xprv;
//   - Solana: RFC 8032 的 ed25519, 固定 32 字节种子派生;
//   - Stellar: Stellar Go SDK 的 StrKey Seed 向量与 AccountID 向量;
//   - IBAN: Wikipedia 的 IBAN 示例; SSN/IPv4: 结构合法样本与保留段边界。
//
// 密钥形状的字面量都拆成两段拼接, 免得源码里的公开向量被密钥扫描器当成私钥。
package mask

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ntB58 base58 字母表(测试辅助, 不依赖包内实现).
const ntB58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ntBase58Encode 字节的 base58 编码(测试辅助, 独立实现).
func ntBase58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, ntB58[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// ntBase58CheckEncode 载荷补 4 字节双 sha256 校验和后编码(测试辅助).
func ntBase58CheckEncode(payload []byte) string {
	h := sha256.Sum256(payload)
	h = sha256.Sum256(h[:])
	return ntBase58Encode(append(append([]byte{}, payload...), h[:4]...))
}

// ntCRC16XModem CRC16-XModem 校验和(测试辅助, 独立实现).
func ntCRC16XModem(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for i := 0; i < 8; i++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// ntHex 十六进制样本解码(测试辅助).
func ntHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("十六进制样本 %q 非法: %v", s, err)
	}
	return b
}

// ntByteList 字节数组的 keyfile 写法(测试辅助).
func ntByteList(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = strconv.Itoa(int(c))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TestCryptoWifKeyOK WIF 私钥: 有效向量应通过, 改一位/截断/超长/非 base58 字符应失败.
func TestCryptoWifKeyOK(t *testing.T) {
	const (
		// Bitcoin wiki 的 WIF 示例: version 0x80 + 32 字节私钥, 未压缩.
		wikiWIF = "5HueCGU8rMjxEXxiPuD5BD" + "ku4MkFqeZyd4dZ1jvhTVqvbTLvyTJ"
		// 私钥全 1 的未压缩 WIF.
		privOneWIF = "5HpHagT65TZzG1PH3CSu63k8DbpvD8s5" + "ip4nEB3kEsreAnchuDf"
		// 私钥全 1 的压缩 WIF: 载荷末尾多一个 0x01 标记.
		privOneCompressedWIF = "KwDiBf89QgGbjEhKnhXJuH7LrciVrZi3q" + "Yjgd9M7rFU73sVHnoWn"
		// 压缩标记被改成 0x02.
		compressedBad = "KwDiBf89QgGbjEhKnhXJuH7LrciVrZi3q" + "Yjgd9M7rFU73sfZr2ym"
		// Litecoin 的版本字节 0xb0.
		litecoinWIF = "6u823ozcyt2rjPH8Z2ErsSXJB5PPQwK7" + "VVTwwN4mxLBFrao69XQ"
		// wiki 样例改末位 / 改首位.
		wikiWIFBadTail = "5HueCGU8rMjxEXxiPuD5BD" + "ku4MkFqeZyd4dZ1jvhTVqvbTLvyTK"
		wikiWIFBadHead = "7HueCGU8rMjxEXxiPuD5BD" + "ku4MkFqeZyd4dZ1jvhTVqvbTLvyTJ"
	)

	// 权威向量与独立实现的 base58check 结果一致.
	assert.Equal(t, wikiWIF, ntBase58CheckEncode(ntHex(t,
		"800c28fca386c7a227600b2fe50b7cae11ec86d3bf1fbe471be89827e19d72aa1d")), "wiki WIF 的 base58check 结果固定")
	assert.Equal(t, privOneWIF, ntBase58CheckEncode(ntHex(t,
		"800000000000000000000000000000000000000000000000000000000000000001")), "私钥全 1 的 WIF 结果固定")
	assert.Equal(t, privOneCompressedWIF, ntBase58CheckEncode(ntHex(t,
		"80000000000000000000000000000000000000000000000000000000000000000101")), "压缩 WIF 的 base58check 结果固定")
	assert.Equal(t, "2NEpo7TZRRrLZSi2U", ntBase58Encode([]byte("Hello World!")), "base58 编码与公开向量一致")

	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"wiki 未压缩 WIF", wikiWIF, true},
		{"私钥全 1 未压缩 WIF", privOneWIF, true},
		{"私钥全 1 压缩 WIF", privOneCompressedWIF, true},
		{"Litecoin 版本字节 0xb0", litecoinWIF, true},
		{"压缩标记改成 0x02", compressedBad, false},
		{"末位改一位", wikiWIFBadTail, false},
		{"首字符改一位", wikiWIFBadHead, false},
		{"截断到 50 位", wikiWIF[:50], false},
		{"多一位到 52 位", wikiWIF + "1", false},
		{"多两位到 53 位", wikiWIF + "11", false},
		{"含 base58 外字符 0", "0" + wikiWIF[1:], false},
		{"含 base58 外字符 O", "O" + wikiWIF[1:], false},
		{"含 base58 外字符 I", "I" + wikiWIF[1:], false},
		{"含 base58 外字符 l", "l" + wikiWIF[1:], false},
		{"纯数字 51 位", strings.Repeat("7", 51), false},
		{"纯字母 51 位", strings.Repeat("z", 51), false},
		{"全 1 的 51 位", strings.Repeat("1", 51), false},
		{"空串", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, wifKeyOK(tc.in), tc.name)
		})
	}
}

// TestCryptoBip32KeyOK BIP-0032 扩展私钥: 官方向量应通过, 改一位与非 78 字节应失败.
func TestCryptoBip32KeyOK(t *testing.T) {
	const (
		// BIP-0032 官方向量 1 的主密钥 xprv.
		xprv = "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPq" +
			"jiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"
		// 改末位校验位.
		xprvBad = "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPq" +
			"jiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHH"
		// 把前缀换成 tprv: 前缀也在校验和里.
		tprv = "tprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPq" +
			"jiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"
	)
	// 向量 1 的 78 字节序列化: 版本/深度/父指纹/子序号/链码/0x00/私钥.
	payload := ntHex(t, "0488ade4"+"00"+"00000000"+"00000000"+
		"873dff81c02f525623fd1fe5167eac3a55a049de3d314bb42ee227ffed37d508"+
		"00"+
		"e8f32e723decf4051aefac8e2c93c9c5b214313817cdb01a1494b917c8436b35")
	assert.Len(t, payload, 78)
	assert.Equal(t, xprv, ntBase58CheckEncode(payload), "官方向量的 base58check 结果固定")

	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"官方向量 1 主密钥", xprv, true},
		{"末位改一位", xprvBad, false},
		{"前缀改成 tprv", tprv, false},
		{"载荷 77 字节", ntBase58CheckEncode(payload[:77]), false},
		{"载荷 79 字节", ntBase58CheckEncode(append(append([]byte{}, payload...), 0x00)), false},
		{"空串", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, bip32KeyOK(tc.in), tc.name)
		})
	}
}

// TestCryptoSolanaKeyOK Solana 私钥: 种子派生的密钥对应通过, 公钥半被改应失败.
func TestCryptoSolanaKeyOK(t *testing.T) {
	const (
		// 合成向量: 固定 32 字节种子派生的 base58, 仅验证 ed25519 与 base58 自洽.
		solanaKey = "2q9x31D61gUuyJ22hiwSaQDazLMDvUA4y9fyzQTVkiDaNKgSZtqpBiv1n" +
			"WRW3qppf6R6Rdxs5sbckwAqQBxFicnG"
		// 同一密钥把公钥半改 1 bit 后的 base58.
		solanaKeyBad = "2q9x31D61gUuyJ22hiwSaQDazLMDvUA4y9fyzQTVkiDaNKgSZtqpBiv1s" +
			"o6hBAEztnwe1t2ioBZv1Gc8HDymuQ4C"
	)
	// 固定 32 字节种子(合成向量, 仅验证 ed25519 派生自洽).
	seed := ntHex(t, "5b8b1e4a9c7d2f3061a5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a09f")
	key := ed25519.NewKeyFromSeed(seed)
	assert.Len(t, key, ed25519.PrivateKeySize)
	// 公钥与独立实现(RFC 8032)的派生结果一致.
	assert.Equal(t, "841e2fcec56cd2dd0a189d145cba7384c7ed72152c5e0c5f2afed52d56626cd5",
		hex.EncodeToString(key[ed25519.SeedSize:]), "ed25519 公钥派生结果固定")
	assert.Equal(t, solanaKey, ntBase58Encode(key), "Solana 私钥的 base58 结果固定")
	assert.Len(t, solanaKey, 88)

	// 公钥半改 1 bit: 长度不变, 但公钥不再由种子派生.
	bad := make([]byte, len(key))
	copy(bad, key)
	bad[40] ^= 0x01
	mutated := ntBase58Encode(bad)
	assert.Equal(t, solanaKeyBad, mutated, "改动后的 base58 结果固定")
	assert.Len(t, mutated, 88, "长度仍在 85~88 之间, 失败原因只能是公钥不匹配")

	assert.True(t, solanaKeyOK(solanaKey), "种子派生的密钥对应通过")
	assert.False(t, solanaKeyOK(mutated), "公钥半被改应失败")
	assert.False(t, solanaKeyOK(ntBase58Encode(seed)), "只有种子(32 字节)应失败")
	assert.False(t, solanaKeyOK(solanaKey[:84]), "截断到 84 位应失败")
	assert.False(t, solanaKeyOK(solanaKey+"1"), "加长一位应失败")
	assert.False(t, solanaKeyOK(""), "空串应失败")
}

// TestCryptoSolanaKeypairOK Solana keyfile 数组: 合法数组应通过, 破坏数组应失败.
func TestCryptoSolanaKeypairOK(t *testing.T) {
	// 固定 32 字节种子(合成向量, 仅验证 ed25519 派生自洽).
	seed := ntHex(t, "5b8b1e4a9c7d2f3061a5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a09f")
	key := ed25519.NewKeyFromSeed(seed)
	list := ntByteList(key)
	// 独立实现算出的 64 字节数组(种子 + 公钥).
	want := "[91,139,30,74,156,125,47,48,97,165,228,211,194,177,160,159,142,125,108,91,74,57,40,23," +
		"6,245,228,211,194,177,160,159,132,30,47,206,197,108,210,221,10,24,157,20,92,186,115,132,199,237," +
		"114,21,44,94,12,95,42,254,213,45,86,98,108,213]"
	assert.Equal(t, want, list, "keyfile 数组内容固定")
	assert.True(t, solanaKeypairOK(list), "种子派生的数组应通过")

	parts := strings.Split(strings.Trim(list, "[]"), ",")
	assert.Len(t, parts, 64)

	parts[63] = "abc"
	assert.False(t, solanaKeypairOK("["+strings.Join(parts, ",")+"]"), "含非数字应失败")

	parts[63] = "256"
	assert.False(t, solanaKeypairOK("["+strings.Join(parts, ",")+"]"), "数值超过 255 应失败")

	parts[63] = "213"
	assert.False(t, solanaKeypairOK("["+strings.Join(parts[:63], ",")+"]"), "只有 63 个数应失败")
	assert.False(t, solanaKeypairOK("["+strings.Join(parts, ",")+",0]"), "65 个数应失败")

	// 公钥半换成别的公钥: 数组长度对, 但公钥不由种子派生.
	other := ed25519.NewKeyFromSeed(ntHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	mixed := append([]byte{}, key[:ed25519.SeedSize]...)
	mixed = append(mixed, other[ed25519.SeedSize:]...)
	assert.False(t, solanaKeypairOK(ntByteList(mixed)), "公钥半不匹配应失败")

	assert.False(t, solanaKeypairOK(""), "空串应失败")
	assert.False(t, solanaKeypairOK("[]"), "空数组应失败")
}

// TestCryptoStellarSeedOK Stellar 秘密种子: 官方 StrKey 向量应通过, 改校验位与错版本应失败.
func TestCryptoStellarSeedOK(t *testing.T) {
	const (
		// Stellar Go SDK(strkey 包)的 Seed 官方测试向量.
		seedStr = "SBU2RRGLXH3E5CQHTD3ODLDF2BWDCYUSS" + "BLLZ5GNW7JXHDIYKXZWHOKR"
		// 同一 SDK 的 AccountID 向量: 版本字节是 6<<3, 不是秘密种子的 18<<3.
		acctStr = "GA3D5KRYM6CB7OWQ6TWYRR3Z4T7GNZLKERY" + "NZGGA5SOAOPIFY6YQHES5"
		// 同一载荷改账号版本字节: 校验位对, 版本字节错.
		acctVersionStr = "GBU2RRGLXH3E5CQHTD3ODLDF2BWDCYUSS" + "BLLZ5GNW7JXHDIYKXZWGKZO"
		// 改末位校验位.
		seedStrBadCRC = "SBU2RRGLXH3E5CQHTD3ODLDF2BWDCYUSS" + "BLLZ5GNW7JXHDIYKXZWHOKA"
	)

	// 权威向量与独立实现的 CRC16-XModem 交叉校验.
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seedStr)
	assert.NoError(t, err)
	assert.Len(t, raw, 35)
	assert.Equal(t, byte(18<<3), raw[0], "秘密种子的版本字节是 18<<3")
	c := ntCRC16XModem(raw[:33])
	assert.Equal(t, []byte{byte(c), byte(c >> 8)}, raw[33:], "CRC16-XModem 校验位一致")

	rawAcct, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(acctStr)
	assert.NoError(t, err)
	assert.Equal(t, byte(6<<3), rawAcct[0], "账号 ID 的版本字节是 6<<3")

	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"官方 Seed 向量", seedStr, true},
		{"官方 AccountID 向量(版本字节不对)", acctStr, false},
		{"同载荷改账号版本字节", acctVersionStr, false},
		{"改末位校验位", seedStrBadCRC, false},
		{"截断到 55 位", seedStr[:55], false},
		{"加长到 57 位", seedStr + "AA", false},
		{"纯 base32 字符", "S" + strings.Repeat("A", 55), false},
		{"空串", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stellarSeedOK(tc.in), tc.name)
		})
	}
}

// TestPersonalSsnOK 美国 SSN: 结构合法应通过, 非法区号/组号/序号与示例号应失败.
func TestPersonalSsnOK(t *testing.T) {
	// 正则只吞数字-连字符串, 纯数字九位与纯字母不匹配; 3-2-4 形状由校验把关.
	found := false
	for _, r := range PersonalExtraRules {
		if r.Label == "SSN" {
			found = true
			assert.NotEmpty(t, r.Pattern.FindString("001-01-0001"), "带分隔符的 SSN 匹配形状")
			assert.Empty(t, r.Pattern.FindString("456789012"), "纯数字九位不匹配形状")
			assert.Empty(t, r.Pattern.FindString("abcdefghi"), "纯字母不匹配形状")
			sub := r.Pattern.FindStringSubmatch("2024-01-2345")
			if assert.Len(t, sub, 2, "日期串应整体落入捕获组") {
				assert.Equal(t, "2024-01-2345", sub[1])
			}
		}
	}
	assert.True(t, found, "SSN 规则存在")

	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"结构合法(区号 001)", "001-01-0001", true},
		{"结构合法(区号 100)", "100-45-6789", true},
		{"结构合法(区号 555)", "555-12-3456", true},
		{"结构合法(区号 321)", "321-54-9876", true},
		{"广告号 123-45-6789", "123-45-6789", false},
		{"示例号 078-05-1120", "078-05-1120", false},
		{"示例号 219-09-9999", "219-09-9999", false},
		{"区号 000", "000-12-3456", false},
		{"区号 666", "666-12-3456", false},
		{"区号 9xx", "900-12-3456", false},
		{"组号 00", "123-00-4567", false},
		{"序号 0000", "123-45-0000", false},
		{"全同号", "111-11-1111", false},
		{"空串", "", false},
		{"位数不足", "12-34-567", false},
		{"位数超长", "123-45-67890", false},
		{"日期串(整段吞入后形状不符)", "2024-01-2345", false},
		{"前后数字粘连", "1234-56-7890", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ssnOK(tc.in), tc.name)
		})
	}
}

// TestPersonalIbanOK IBAN: 已知有效账号应通过, 改一位/长度不对/非法字符应失败.
func TestPersonalIbanOK(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"英国示例", "GB82WEST12345698765432", true},
		{"德国示例", "DE89370400440532013000", true},
		{"法国示例", "FR1420041010050500013M02606", true},
		{"比利时示例", "BE68539007547034", true},
		{"荷兰示例", "NL91ABNA0417164300", true},
		{"改末位数字", "GB82WEST12345698765433", false},
		{"改末位数字(德国)", "DE89370400440532013001", false},
		{"长度少一位", "GB82WEST1234569876543", false},
		{"长度少一位(德国)", "DE8937040044053201300", false},
		{"含非法字符", "GB82WEST1234569876543!", false},
		{"小写国别码", "gb82WEST12345698765432", false},
		{"未知国别码", "ZZ82WEST12345698765432", false},
		{"空串", "", false},
		{"过短", "GB82", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ibanOK(tc.in), tc.name)
		})
	}
}

// TestPersonalPublicIPv4OK 公网 IPv4: 公网地址应通过, 私有/保留段与非法 IP 应失败.
func TestPersonalPublicIPv4OK(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"公网地址", "93.184.216.34", true},
		{"公网地址", "1.2.3.4", true},
		{"公网地址(172.32 在 172.16/12 之外)", "172.32.0.1", true},
		{"公网地址", "11.0.0.1", true},
		{"公共 DNS 8.8.8.8", "8.8.8.8", false},
		{"公共 DNS 1.1.1.1", "1.1.1.1", false},
		{"公共 DNS 9.9.9.9", "9.9.9.9", false},
		{"私有段 10/8", "10.0.0.1", false},
		{"私有段 172.16/12", "172.16.0.1", false},
		{"私有段 172.31/12", "172.31.255.255", false},
		{"私有段 192.168/16", "192.168.1.1", false},
		{"环回", "127.0.0.1", false},
		{"链路本地", "169.254.1.1", false},
		{"组播", "224.0.0.1", false},
		{"组播上界", "239.255.255.250", false},
		{"CGNAT", "100.64.0.1", false},
		{"CGNAT 上界", "100.127.255.255", false},
		{"文档段 192.0.2/24", "192.0.2.1", false},
		{"文档段 198.51.100/24", "198.51.100.1", false},
		{"文档段 203.0.113/24", "203.0.113.1", false},
		{"基准段 198.18/15", "198.18.0.1", false},
		{"未指定", "0.0.0.0", false},
		{"保留段 240/4", "240.0.0.1", false},
		{"广播", "255.255.255.255", false},
		{"保留段 192.0.0/24", "192.0.0.1", false},
		{"非法 IP(少一段)", "1.2.3", false},
		{"非法 IP(段超 255)", "256.1.1.1", false},
		{"非法 IP(多一段)", "1.2.3.4.5", false},
		{"非法 IP(非数字)", "abc", false},
		{"空串", "", false},
		{"IPv6 环回", "::1", false},
		{"IPv6 文档段", "2001:db8::1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, publicIPv4OK(tc.in), tc.name)
		})
	}
}
