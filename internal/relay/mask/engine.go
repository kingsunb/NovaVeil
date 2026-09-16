package mask

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Engine 脱敏引擎, 持有会话存储与内置规则集。
type Engine struct {
	store *SessionStore
	rules []Rule
}

// NewEngine 构造使用内置规则集的引擎。
func NewEngine(store *SessionStore) *Engine {
	return &Engine{store: store, rules: BuiltinRules}
}

// DeleteSession 回收指定会话的映射表, 供请求结束后调用以防止内存泄漏。
func (e *Engine) DeleteSession(sessionKey string) {
	if e.store != nil && sessionKey != "" {
		e.store.Delete(sessionKey)
	}
}

// SessionStore 返回引擎内置会话映射表, 供外部按需回收。
func (e *Engine) SessionStore() *SessionStore { return e.store }

// CustomTerm 自定义敏感词(人名、项目代号、内部术语), 整词匹配, 占位符标签用 TERM。
type CustomTerm struct {
	Value    string
	Category string // 分类元信息, 不参与占位符标签
}

// Match 一次命中的记录, 供审计与调试。
type Match struct {
	Label       string
	Original    string
	Placeholder string
}

// MaskResult 脱敏结果。
type MaskResult struct {
	Masked  string   // 脱敏后的文本
	Mapping *Mapping // 本轮会话映射表(供还原用)
	Matches []Match  // 命中明细
}

// Apply 对 body 做脱敏, 返回脱敏文本、会话映射与命中明细。
//
// JSON 对象/数组/字符串仅扫描解码后的字符串值, 键名与非文本字段不变。
// 其他输入按纯文本处理。所有规则在原文收集候选, 重叠区间整体覆盖后一次替换。
// 同区间自定义词优先, 不以短词截断完整凭据; 已有占位符始终保留。
//
// Fail-closed: 任何 panic(如 crypto/rand 故障)转为 error, 调用方据此 rejectRequest, 绝不放行明文。
func (e *Engine) Apply(body string, sessionKey string, enabledRules map[string]bool, terms []CustomTerm) (result MaskResult, err error) {
	// fail-closed: 捕获脱敏过程中的 panic, 转为错误, 清空半成品, 绝不返回部分脱敏文本。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mask: 脱敏失败 fail-closed: %v", r)
			result = MaskResult{}
		}
	}()

	mapping := e.store.GetOrCreate(sessionKey)
	result.Mapping = mapping
	if body == "" {
		return result, nil
	}

	var rules []Rule
	for _, term := range terms {
		if term.Value != "" {
			rules = append(rules, Rule{Label: "TERM", Pattern: customTermRegex(term.Value)})
		}
	}
	for _, rule := range e.rules {
		if enabledRules[rule.Label] {
			rules = append(rules, rule)
		}
	}
	seen := make(map[string]bool)
	maskText := func(text string) string {
		return maskCandidates(text, rules, mapping, &result.Matches, seen)
	}
	result.Masked = body
	if len(rules) != 0 {
		trimmed := strings.TrimSpace(body)
		// 裸数字仍按纯文本扫描, 保持 Apply("13800138000") 的既有用法。
		if len(trimmed) > 0 && strings.ContainsAny(trimmed[:1], "{[\"") && json.Valid([]byte(body)) {
			result.Masked = mapJSONStringValues(body, maskText)
		} else {
			result.Masked = maskText(body)
		}
	}
	return result, nil
}

// ApplyBytes 是 Apply 的 []byte 便捷封装。
func (e *Engine) ApplyBytes(body []byte, sessionKey string, enabledRules map[string]bool, terms []CustomTerm) ([]byte, *Mapping, error) {
	res, err := e.Apply(string(body), sessionKey, enabledRules, terms)
	if err != nil {
		return nil, nil, err
	}
	return []byte(res.Masked), res.Mapping, nil
}

// ---- 自定义敏感词正则缓存 ----

var customTermRegexCache sync.Map // value -> *regexp.Regexp

// isASCIIWord 判断字符串是否全为 ASCII 词字符([A-Za-z0-9_]), 用于决定是否加 \b 词边界。
// 中文等非 ASCII 词不适用 \b(中文与中文间不存在词边界), 故只对纯 ASCII 词加边界。
func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// customTermRegex 构造并缓存自定义敏感词的整词匹配正则。纯 ASCII 词加 \b 边界, 否则字面匹配。
func customTermRegex(value string) *regexp.Regexp {
	if v, ok := customTermRegexCache.Load(value); ok {
		return v.(*regexp.Regexp)
	}
	escaped := regexp.QuoteMeta(value)
	pat := escaped
	if isASCIIWord(value) {
		pat = `\b` + escaped + `\b`
	}
	rx := regexp.MustCompile(pat)
	customTermRegexCache.Store(value, rx)
	return rx
}

// ---- 特征预检(详见 docs/脱敏开发/05 §3.3)----

// ruleMayHit 快速判断文本是否含某规则的必含特征, 不含则跳过整条正则扫描,
// 避免对每个请求跑全部规则。未登记特征的标签保守返回 true(跑正则)。
func ruleMayHit(text, label string) bool {
	switch label {
	case "PRIVATE_KEY":
		return strings.Contains(text, "PRIVATE KEY")
	case "API_KEY":
		for _, p := range apiPrefixes {
			if strings.Contains(text, p) {
				return true
			}
		}
		return false
	case "CONNSTR":
		return strings.Contains(text, "://") && strings.Contains(text, "@")
	case "EMAIL":
		return strings.Contains(text, "@")
	case "PHONE":
		return hasNDigits(text, 11)
	case "IDCARD":
		return hasNDigits(text, 17)
	case "CARD":
		return hasNDigits(text, 13)
	case "SECRET":
		lower := strings.ToLower(text)
		for _, k := range secretKeywordsLower {
			if strings.Contains(lower, k) {
				return true
			}
		}
		for _, k := range secretKeywordsCN {
			if strings.Contains(text, k) {
				return true
			}
		}
		return false
	case "JWT":
		return strings.Contains(text, "eyJ")
	case "TOKEN":
		return strings.Contains(strings.ToLower(text), "bearer")
	case "MAC":
		return strings.ContainsAny(text, ":-.")
	case "USCC":
		return hasNAlnum(text, 18)
	}
	return true
}

var apiPrefixes = []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "sk-", "cli-", "ding-"}

var secretKeywordsLower = []string{"password", "passwd", "pwd", "secret", "token", "api_key", "api-key", "apikey", "access_key", "access-key", "accesskey", "private_key", "private-key", "privatekey"}
var secretKeywordsCN = []string{"密码", "口令", "令牌", "密钥", "秘钥", "密匙", "凭据", "凭证", "私钥", "授权码", "访问密钥", "接口密钥"}

// hasNDigits 判断文本是否至少含 n 个 ASCII 数字。
func hasNDigits(text string, n int) bool {
	cnt := 0
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			cnt++
			if cnt >= n {
				return true
			}
		}
	}
	return false
}

// hasNAlnum 判断文本是否含连续 n 位 ASCII 字母数字([A-Za-z0-9])。
func hasNAlnum(text string, n int) bool {
	cnt := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			cnt++
			if cnt >= n {
				return true
			}
		} else {
			cnt = 0
		}
	}
	return false
}

// ---- 单规则应用 ----

// applyRule 对 text 应用单条规则, 返回脱敏后的文本。命中明细追加到 matches。
//
// 分两遍(详见 docs/脱敏开发/06 §三):
//  1. 收集唯一原文(跳过占位符区域, 经校验函数过滤), 去重保序;
//  2. 为每个唯一原文生成/复用占位符, 再做替换(跳过占位符区域)。
//
// 去重替换防 O(n²) 退化(同一手机号出现上万次时只对唯一原文替换一次)。
func applyRule(text string, rule Rule, mapping *Mapping, matches *[]Match) string {
	if text == "" {
		return text
	}
	validator := builtinValidators[rule.Label]

	// Pass 1: 在非占位符片段中收集唯一原文。
	seen := make(map[string]bool)
	var uniqueOrig []string
	for _, seg := range nonPlaceholderSegments(text) {
		for _, loc := range rule.Pattern.FindAllStringSubmatchIndex(seg, -1) {
			orig := captureGroup(seg, loc, rule.Group)
			if orig == "" {
				continue
			}
			if validator != nil && !validator(orig) {
				continue
			}
			if !seen[orig] {
				seen[orig] = true
				uniqueOrig = append(uniqueOrig, orig)
			}
		}
	}
	if len(uniqueOrig) == 0 {
		return text
	}

	// 生成/复用占位符, 构建 replMap。
	replMap := make(map[string]string, len(uniqueOrig))
	for _, orig := range uniqueOrig {
		ph := mapping.Recall(orig, rule.Label)
		replMap[orig] = ph
		*matches = append(*matches, Match{Label: rule.Label, Original: orig, Placeholder: ph})
	}

	// Pass 2: 替换(跳过占位符区域)。
	return replaceExcludingPlaceholders(text, rule, replMap)
}

// captureGroup 从一次匹配中取出应脱敏的原文(Group=0 取整匹配, 否则取对应捕获组)。
func captureGroup(seg string, loc []int, group int) string {
	if group == 0 {
		return seg[loc[0]:loc[1]]
	}
	gs, ge := loc[2*group], loc[2*group+1]
	if gs < 0 || ge < 0 {
		return ""
	}
	return seg[gs:ge]
}

// nonPlaceholderSegments 用占位符正则把文本切成片段, 仅返回非占位符部分(占位符原样保留, 不在其中)。
// 自定义词里 2 字符的 hex 子串会把占位符劈开 → 畸形占位符 → 还原永久失败, 故只对非占位符片段做替换。
func nonPlaceholderSegments(text string) []string {
	locs := PlaceholderRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []string{text}
	}
	segs := make([]string, 0, len(locs)+1)
	lastEnd := 0
	for _, loc := range locs {
		if loc[0] > lastEnd {
			segs = append(segs, text[lastEnd:loc[0]])
		}
		lastEnd = loc[1]
	}
	if lastEnd < len(text) {
		segs = append(segs, text[lastEnd:])
	}
	return segs
}

// replaceExcludingPlaceholders 对 text 做规则替换, 但跳过已有占位符片段(原样保留)。
func replaceExcludingPlaceholders(text string, rule Rule, replMap map[string]string) string {
	if text == "" {
		return text
	}
	locs := PlaceholderRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return applyRuleSub(text, rule, replMap)
	}
	var b strings.Builder
	lastEnd := 0
	for _, loc := range locs {
		b.WriteString(applyRuleSub(text[lastEnd:loc[0]], rule, replMap))
		b.WriteString(text[loc[0]:loc[1]]) // 占位符原样保留
		lastEnd = loc[1]
	}
	b.WriteString(applyRuleSub(text[lastEnd:], rule, replMap))
	return b.String()
}

// applyRuleSub 对不含占位符的 segment 做规则替换。
// Group=0 整匹配替换; Group>0 只替换捕获组, 保留匹配内其余文本(如 Bearer sk-xxx 只换 sk-xxx)。
func applyRuleSub(segment string, rule Rule, replMap map[string]string) string {
	if segment == "" {
		return segment
	}
	locs := rule.Pattern.FindAllStringSubmatchIndex(segment, -1)
	if len(locs) == 0 {
		return segment
	}
	var b strings.Builder
	lastEnd := 0
	for _, loc := range locs {
		b.WriteString(segment[lastEnd:loc[0]])
		whole := segment[loc[0]:loc[1]]
		orig := captureGroup(segment, loc, rule.Group)
		ph, ok := replMap[orig]
		if !ok {
			// 不在替换表(可能被校验函数过滤), 原样保留。
			b.WriteString(whole)
		} else if rule.Group == 0 {
			b.WriteString(ph)
		} else {
			gs, ge := loc[2*rule.Group], loc[2*rule.Group+1]
			b.WriteString(segment[loc[0]:gs]) // 匹配内捕获组之前的部分
			b.WriteString(ph)                 // 替换捕获组
			b.WriteString(segment[ge:loc[1]]) // 匹配内捕获组之后的部分
		}
		lastEnd = loc[1]
	}
	b.WriteString(segment[lastEnd:])
	return b.String()
}
