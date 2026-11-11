// 移植自 magpie internal/redact/more.go (MIT, Copyright (c) 2026 yetone).
// 4 条个人数据补充规则: SSN / IBAN / 公网 IPv4 / CJK 护照号. 正则锁形状, 校验函数锁真实性.
// 仅取紧凑形式, 不依赖 magpie 的 fold/back/ahead 引擎能力; 低层 helper 以 personal 前缀命名避免撞名.
package mask

import (
	"net/netip"
	"regexp"
	"strings"
)

// PersonalExtraRules 个人数据补充规则, 默认全关.
var PersonalExtraRules = []Rule{
	// 吞整段数字串, 形状不符由校验拒绝
	{
		Label:       "SSN",
		Pattern:     regexp.MustCompile(`(?:^|[^0-9A-Za-z-])((?:\d+-)+\d+)`),
		Group:       1,
		Description: "美国社会安全号(区号/组号范围校验)",
	},
	{
		Label:       "IBAN",
		Pattern:     regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`),
		Group:       0,
		Description: "国际银行账号(紧凑形式, mod 97 校验)",
	},
	// 贪婪吞整段, 多段由解析拒绝
	{
		Label:       "IP",
		Pattern:     regexp.MustCompile(`(?:^|[^0-9A-Za-z.])((?:\d{1,3}\.){3,}\d{1,3})`),
		Group:       1,
		Description: "公网 IPv4 地址(排除私有/保留段)",
	},
	{
		Label:       "PASSPORT",
		Pattern:     regexp.MustCompile(`(?:护照(?:号码|号)?|護照(?:號碼|号码|番号)?|パスポート(?:番号)?)[ \t]*(?:[:：=][ \t]*)?(?:是[ \t]*|は[ \t]*)?["']?([A-Z0-9]{6,9})`),
		Group:       1,
		Description: "护照号(CJK 上下文, 只脱号码组保留关键字)",
	},
}

// PersonalExtraRuleMeta 规则元信息, 全部默认关闭(出厂全关硬约束).
var PersonalExtraRuleMeta = []RuleMeta{
	{"SSN", "美国社会安全号", false},
	{"IBAN", "国际银行账号", false},
	{"IP", "公网 IPv4 地址", false},
	{"PASSPORT", "护照号(CJK 上下文)", false},
}

// PersonalExtraValidators 按标签索引的校验函数. PASSPORT 无校验, 不在此表, 正则命中即脱敏.
var PersonalExtraValidators = map[string]Validator{
	"SSN":  ssnOK,
	"IBAN": ibanOK,
	"IP":   publicIPv4OK,
}

// PersonalExtraMayHit 特征预检: text 不含该标签必含特征时返回 false, 引擎据此跳过正则.
// 未知标签返回 true(保守放行, 由正则与校验兜底).
func PersonalExtraMayHit(text, label string) bool {
	switch label {
	case "SSN":
		return strings.ContainsRune(text, '-') && strings.ContainsAny(text, "0123456789")
	case "IBAN":
		return strings.ContainsAny(text, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(text, "0123456789")
	case "IP":
		return strings.Count(text, ".") >= 3 && strings.ContainsAny(text, "0123456789")
	case "PASSPORT":
		return strings.Contains(text, "护照") || strings.Contains(text, "護照") || strings.Contains(text, "パスポート")
	default:
		return true
	}
}

// ---- 校验函数(防误报), 译自 magpie internal/redact/more.go ----

// ssnOK 先验 3-2-4 形状(正则吞整段后防截半), 再排除 SSA 不发放区号(000/666/900+)、组号 00、序号 0000、广告示例号与全同号. 防随机 9 位串误报.
func ssnOK(v string) bool {
	if len(v) != 11 || v[3] != '-' || v[6] != '-' {
		return false
	}
	d := strings.NewReplacer("-", "", " ", "").Replace(v)
	if len(d) != 9 || d[:3] == "000" || d[:3] == "666" || d[0] == '9' || d[3:5] == "00" || d[5:] == "0000" {
		return false
	}
	switch d {
	case "123456789", "078051120", "219099999":
		return false
	}
	return strings.Trim(d, d[:1]) != ""
}

// personalIbanLen 各国 IBAN 固定长度. 译自 magpie ibanLen.
var personalIbanLen = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22, "BH": 22, "BI": 27, "BR": 29, "BY": 28,
	"CH": 21, "CR": 22, "CY": 28, "CZ": 24, "DE": 22, "DJ": 27, "DK": 18, "DO": 28, "EE": 20, "EG": 29, "ES": 24, "FI": 18,
	"FK": 18, "FO": 18, "FR": 27, "GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28, "HN": 28, "HR": 21, "HU": 28,
	"IE": 22, "IL": 23, "IQ": 23, "IS": 26, "IT": 27, "JO": 30, "KW": 30, "KZ": 20, "LB": 28, "LC": 32, "LI": 21, "LT": 20,
	"LU": 20, "LV": 21, "LY": 25, "MC": 27, "MD": 24, "ME": 22, "MK": 19, "MN": 20, "MR": 27, "MT": 31, "MU": 30, "NI": 28,
	"NL": 18, "NO": 15, "OM": 23, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29, "RO": 24, "RS": 22, "RU": 33, "SA": 24,
	"SC": 31, "SD": 18, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "SO": 23, "ST": 25, "SV": 28, "TL": 23, "TN": 24, "TR": 26,
	"UA": 29, "VA": 22, "VG": 24, "XK": 20, "YE": 30,
}

// ibanOK 国别长度匹配 + mod 97 校验位(前 4 位移到末尾, 字母转数字 A=10..Z=35, 余数须为 1). 防随机字母数字串误报.
func ibanOK(v string) bool {
	v = strings.ReplaceAll(v, " ", "")
	if len(v) < 4 {
		return false
	}
	if n, ok := personalIbanLen[v[:2]]; !ok || n != len(v) {
		return false
	}
	r := v[4:] + v[:4]
	m := 0
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c >= '0' && c <= '9':
			m = (m*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			m = (m*100 + int(c-'A') + 10) % 97
		default:
			return false
		}
	}
	return m == 1
}

// personalNotIPv4 须排除的保留段: 文档/基准/保留等. 私有、环回、链路本地、组播、
// 未指定由 netip 内置方法判定, 此处补齐内置未覆盖的段(含 0.0.0.0/8 与 CGNAT).
var personalNotIPv4 = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// personalDNSResolvers 公共 DNS 解析器, 人人通用, 不指向具体用户. 译自 magpie resolvers(IPv4 部分).
var personalDNSResolvers = map[string]bool{
	"8.8.8.8": true, "8.8.4.4": true, "1.1.1.1": true, "1.0.0.1": true, "9.9.9.9": true, "149.112.112.112": true,
	"208.67.222.222": true, "208.67.220.220": true, "114.114.114.114": true, "223.5.5.5": true, "223.6.6.6": true,
	"119.29.29.29": true, "180.76.76.76": true,
}

// publicIPv4OK 排除私有/环回/链路本地/组播/未指定/CGNAT/文档/保留段与公共 DNS. 防内网地址与公共 DNS 误报.
func publicIPv4OK(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil || !ip.Is4() {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if personalDNSResolvers[ip.String()] {
		return false
	}
	for _, p := range personalNotIPv4 {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
