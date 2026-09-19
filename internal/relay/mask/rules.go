package mask

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Rule 一条脱敏正则规则。Group 为捕获组号: 0 表示整匹配替换;
// 1 表示只替换第 1 个捕获组(如 CONNSTR 只脱密码组、SECRET 只脱值组), 保留其余文本。
type Rule struct {
	Label       string         // 规则类型标签, 如 "PHONE"
	Pattern     *regexp.Regexp // 编译后的正则
	Group       int            // 捕获组号
	Description string         // 规则说明
}

// Validator 可选校验函数, 对正则命中的原文做真实性校验以防误报。
// 正则只能锁形状, 校验函数锁真实性(如银行卡 Luhn、身份证 ISO 7064 校验位)。
type Validator func(s string) bool

// RuleMeta 规则的对外元信息, 供配置层展示与默认开关判定。
// JSON tag 使用 snake_case, 与前端 MaskRuleMeta 类型对齐。
type RuleMeta struct {
	Label          string `json:"label"`
	Description    string `json:"description"`
	DefaultEnabled bool   `json:"default_enabled"`
}

// builtinValidators 按标签索引的校验函数表。引擎对每条命中先跑对应校验, 不通过则跳过。
// 规则结构本身不含校验字段, 校验是包内按标签分派的关注点。
var builtinValidators = map[string]Validator{
	"CARD":   luhnOK,
	"IDCARD": idcard18OK,
	"PHONE":  phoneOK,
	"EMAIL":  emailOK,
	"JWT":    jwtOK,
	"MAC":    macOK,
	"SECRET": secretOK,
	"USCC":   usccOK,
}

// BuiltinRules 内置正则规则, 按 docs/脱敏开发/02 顺序排列。默认全关, 由调用方按 enabledRules 开启。
// 正则译自 maskit transparent.py:52-114, 因 Go 标准库 regexp(RE2)不支持前后向断言,
// 用 \b 词边界与字符类替代; 误报由校验函数兜底。
var BuiltinRules = []Rule{
	{
		Label:       "PRIVATE_KEY",
		Pattern:     regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----.{20,}?-----END[^-]*PRIVATE KEY-----`),
		Group:       0,
		Description: "PEM 私钥整块替换(最高危, 形态固定零误报)",
	},
	{
		Label:       "API_KEY",
		Pattern:     regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{50,}|AIza[0-9A-Za-z_-]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|[sr]k_(?:live|test)_[A-Za-z0-9]{20,}|cli-[A-Za-z0-9_-]{10,}|ding-[A-Za-z0-9_-]{10,})`),
		Group:       0,
		Description: "GitHub PAT / Google / Slack / Stripe / OpenAI / 钉钉 等凭据前缀",
	},
	{
		Label:       "CONNSTR",
		Pattern:     regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:@/]+:([^\s@/]{4,})@`),
		Group:       1,
		Description: "数据库连接串密码(只脱密码组, 保留 scheme/user/host)",
	},
	{
		Label:       "PHONE",
		Pattern:     regexp.MustCompile(`(?:\+86[-\s]?|\b(?:86[-\s]?)?)1[3-9][0-9][-\s]?[0-9]{4}[-\s]?[0-9]{4}\b`),
		Group:       0,
		Description: "国内手机号(含 +86 与分隔符, 校验排除全同号)",
	},
	{
		Label:       "EMAIL",
		Pattern:     regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		Group:       0,
		Description: "邮箱(校验合法用户名/域名与 TLD)",
	},
	{
		Label:       "IDCARD",
		Pattern:     regexp.MustCompile(`\b[1-9][0-9]{16}[0-9Xx]\b`),
		Group:       0,
		Description: "18 位身份证(省份+日期+ISO 7064 校验位)",
	},
	{
		Label:       "CARD",
		Pattern:     regexp.MustCompile(`\b[0-9]{13,19}\b`),
		Group:       0,
		Description: "银行卡(13-19 位 + Luhn 校验)",
	},
	{
		Label:       "SECRET",
		Pattern:     regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|密码|口令|令牌|密钥|秘钥|密匙|凭据|凭证|私钥|授权码|访问密钥|接口密钥)["'“”「」]?\s*[:=：＝]\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|“[^”]*”|「[^」]*」|[^\s"'“”「」,;{}\[\]]+)`),
		Group:       1,
		Description: "password=/token=/api_key= 赋值凭据(中英文关键词, 值须含数字/符号)",
	},
	{
		Label:       "JWT",
		Pattern:     regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{6,}`),
		Group:       0,
		Description: "JWT 三段式(header base64 解码含 alg)",
	},
	{
		Label:       "TOKEN",
		Pattern:     regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/-]*[0-9._~+/-][A-Za-z0-9._~+/-]*=*)`),
		Group:       1,
		Description: "Bearer Token(只脱 token 值, 保留 Bearer 关键字); token 值须含至少一个非字母字符(数字/分隔符), 避免误脱敏 'bearer here' 等英文单词",
	},
	{
		Label:       "MAC",
		Pattern:     regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5,}|[0-9A-Fa-f]{2}(?:-[0-9A-Fa-f]{2}){5}|[0-9A-Fa-f]{4}(?:\.[0-9A-Fa-f]{4}){2})\b`),
		Group:       0,
		Description: "MAC 地址(冒号链贪婪成整链候选、由校验限定恰 6 组防截断 IPv6; 连字符 2-2-2-2-2-2 或点 4-4-4, \\b 边界)",
	},
	{
		Label:       "USCC",
		Pattern:     regexp.MustCompile(`\b[0-9A-HJ-NP-RTUWXY]{18}\b`),
		Group:       0,
		Description: "统一社会信用代码(18 位 31 进制 + MOD 31 校验位)",
	},
}

// BuiltinRuleMeta 内置规则的对外元信息。硬约束: 全部默认关闭(NovaVeil 面向网关多用户,
// 误报影响面更大, 首次引入应保守)。管理员按需在配置层开启, 详见 docs/脱敏开发/02 §2.2。
var BuiltinRuleMeta = []RuleMeta{
	{"PRIVATE_KEY", "PEM 私钥整块替换", false},
	{"API_KEY", "GitHub PAT / Google / Slack / Stripe / OpenAI / 钉钉 凭据", false},
	{"CONNSTR", "数据库连接串密码", false},
	{"PHONE", "国内手机号", false},
	{"EMAIL", "邮箱", false},
	{"IDCARD", "18 位身份证", false},
	{"CARD", "银行卡(Luhn)", false},
	{"SECRET", "赋值凭据(password= 等)", false},
	{"JWT", "JWT", false},
	{"TOKEN", "Bearer Token", false},
	{"MAC", "MAC 地址", false},
	{"USCC", "统一社会信用代码", false},
}

// ---- 校验函数(防误报), 译自 maskit transparent.py:410-612 ----

// luhnOK 银行卡 Luhn 校验, 至少 12 位数字。防 10 位串当卡号。
func luhnOK(num string) bool {
	digits := make([]int, 0, len(num))
	for _, c := range num {
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
		}
	}
	if len(digits) < 12 {
		return false
	}
	s, dbl := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		s += d
		dbl = !dbl
	}
	return s%10 == 0
}

// idcardW 身份证前 17 位加权因子, idcardCode 为校验码对照表(ISO 7064 MOD 11-2)。
var (
	idcardW         = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	idcardCode      = "10X98765432"
	idcardProvinces = map[string]bool{
		"11": true, "12": true, "13": true, "14": true, "15": true,
		"21": true, "22": true, "23": true, "31": true, "32": true,
		"33": true, "34": true, "35": true, "36": true, "37": true,
		"41": true, "42": true, "43": true, "44": true, "45": true,
		"46": true, "50": true, "51": true, "52": true, "53": true,
		"54": true, "61": true, "62": true, "63": true, "64": true,
		"65": true, "71": true, "81": true, "82": true,
	}
)

// idcard18OK 18 位身份证校验: 省份 + 真实日期 + ISO 7064 校验位。防时间戳/雪花 ID 当身份证。
func idcard18OK(num string) bool {
	if len(num) != 18 {
		return false
	}
	for i := 0; i < 17; i++ {
		if num[i] < '0' || num[i] > '9' {
			return false
		}
	}
	if !idcardProvinces[num[:2]] {
		return false
	}
	y, err1 := strconv.Atoi(num[6:10])
	m, err2 := strconv.Atoi(num[10:12])
	d, err3 := strconv.Atoi(num[12:14])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	// 用 time.Date 校验真实日期(自动拒绝 2 月 30 日等), 并验证往返一致。
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return false
	}
	if y < 1880 || y > time.Now().Year() {
		return false
	}
	total := 0
	for i := 0; i < 17; i++ {
		total += int(num[i]-'0') * idcardW[i]
	}
	last := num[17]
	if last >= 'a' && last <= 'z' {
		last -= 32 // 小写 x 归大写 X
	}
	return last == idcardCode[total%11]
}

// phoneOK 国内手机号校验: 1[3-9] 开头 + 排除全同号。防 11111111111。
func phoneOK(numStr string) bool {
	digits := make([]byte, 0, len(numStr))
	for i := 0; i < len(numStr); i++ {
		c := numStr[i]
		if c >= '0' && c <= '9' {
			digits = append(digits, c)
		}
	}
	// +86 前缀: 13 位且以 86 开头时剥除国家码。
	if len(digits) == 13 && digits[0] == '8' && digits[1] == '6' {
		digits = digits[2:]
	}
	if len(digits) != 11 {
		return false
	}
	if digits[0] != '1' || digits[1] < '3' || digits[1] > '9' {
		return false
	}
	allSame := true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			allSame = false
			break
		}
	}
	return !allSame
}

// emailOK 邮箱校验: 合法用户名/域名 + TLD≥2 + 无连续双点。防连接串误当邮箱。
func emailOK(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "@") || strings.HasPrefix(s, "@") || strings.HasSuffix(s, "@") {
		return false
	}
	at := strings.LastIndex(s, "@")
	local, domain := s[:at], s[at+1:]
	if len(local) < 1 || len(domain) < 3 || !strings.Contains(domain, ".") {
		return false
	}
	if strings.HasPrefix(local, ".") || strings.Contains(local, "..") || strings.Contains(domain, "..") {
		return false
	}
	// 域名首尾不能是点(空标签), 如 "a@.com" / "a@com." 非法。
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	dparts := strings.Split(domain, ".")
	tld := dparts[len(dparts)-1]
	return len(tld) >= 2
}

// jwtOK JWT 校验: header 段 base64url 解码后必须含 "alg"。只按三段形态匹配会误判长 base64 串。
func jwtOK(token string) bool {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return false
	}
	head := token[:dot]
	pad := (4 - len(head)%4) % 4
	decoded, err := base64.URLEncoding.DecodeString(head + strings.Repeat("=", pad))
	if err != nil {
		return false
	}
	return strings.Contains(string(decoded), `"alg"`)
}

// secretOK 赋值凭据值校验: 值须含数字或符号(非纯字母)。防 CamelCase 方法名等纯字母标识符误报。
func secretOK(s string) bool {
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return true // 出现非字母(数字或符号)即通过
		}
	}
	return false
}

// macOK MAC 地址校验: 冒号链候选须恰为 6 组。冒号形态正则用 {5,} 贪婪把整条冒号链收为
// 一个候选, 使 IPv6(8 组)等更长链在此处被整体拒绝、原样保留, 避免截取前 6 组误脱敏;
// 连字符/点分形态形状已由正则锁定(恰 6 组/3 段), 直接送过。
func macOK(s string) bool {
	if strings.Contains(s, ":") {
		return strings.Count(s, ":") == 5
	}
	return true
}

// ---- 统一社会信用代码(USCC)校验, 译自 GB 32100-2015 / GB/T 17710 MOD 31-3 ----

// usccAlphabet 31 进制字符集(GB 32100-2015), 禁 I/O/S/V/Z。
const usccAlphabet = "0123456789ABCDEFGHJKLMNPQRTUWXY"

// usccWeight 为 GB 32100-2015 前 17 位自左向右的权重: 3^i mod 31 (i=0..16)。
var usccWeight = [17]int{1, 3, 9, 27, 19, 26, 16, 17, 20, 29, 25, 13, 8, 24, 10, 30, 28}

// usccVal 返回字符在 31 进制字符集中的值; 非法字符返回 -1。
func usccVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'H':
		return int(c-'A') + 10
	case c >= 'J' && c <= 'N':
		return int(c-'J') + 18
	case c >= 'P' && c <= 'R':
		return int(c-'P') + 23
	case c == 'T' || c == 'U':
		return int(c-'T') + 26
	case c == 'W':
		return 28
	case c == 'X':
		return 29
	case c == 'Y':
		return 30
	default:
		return -1
	}
}

// usccOK 校验位 = (31 - Σ(前17位×权重) mod 31) mod 31。
// 仅验证字符集与校验位, 不证明登记主体存在。
func usccOK(s string) bool {
	if len(s) != 18 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		v := usccVal(s[i])
		if v < 0 {
			return false
		}
		sum += v * usccWeight[i]
	}
	check := usccVal(s[17])
	if check < 0 {
		return false
	}
	return check == (31-sum%31)%31
}
