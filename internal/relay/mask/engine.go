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
	defer e.store.End(sessionKey)
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

// ---- 特征预检（未含必含特征则跳过该规则正则，避免全量扫描）----

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

var apiPrefixes = []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "sk-", "sk_live_", "sk_test_", "rk_live_", "rk_test_", "cli-", "ding-"}

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
// 分两遍:
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

// ---- 跨规则候选收集与一次替换(文档 02 §五 重构) ----

// maskCandidates 对 text 用多条 rules 收集命中候选, 重叠区间整体覆盖后一次替换。
//
// 与旧 applyRule(逐规则两遍收集/替换)的区别: 跨规则统一收集候选, 重叠区间按优先级
// 选一组不重叠的, 一次替换完成, 避免短词截断完整凭据。优先级(排序键):
//  1. origStart 升序(先出现的先选);
//  2. 自定义词(TERM)优先——rules 中 TERM 已排在正则前, 同起点 TERM 先选;
//  3. origEnd 降序(同起点长匹配优先, 不以短匹配截断完整凭据, 如 CONNSTR 完整密码整体覆盖 API_KEY 前缀命中);
//  4. ruleIdx 升序(同起点同长度时按 rules 顺序)。
//
// 已有占位符片段跳过(不收集、不替换, 占位符原样保留)。seen 跨规则去重同一原文:
// 同一原文只产出一条 Match(首次命中规则标签), 但在不重叠的多处位置都替换为同一占位符,
// 保证多轮会话内同一实体占位符一致。
func maskCandidates(text string, rules []Rule, mapping *Mapping, matches *[]Match, seen map[string]bool) string {
	if text == "" || len(rules) == 0 {
		return text
	}
	type cand struct {
		origStart, origEnd int // 捕获组(应脱敏原文)在 text 中的绝对区间
		isTerm             bool
		ruleIdx            int
		orig               string
		rule               Rule
	}
	var cands []cand
	// 仅在非占位符片段中收集候选(占位符原样保留, 不在其中收集, 避免自定义词把占位符劈开)。
	for _, sr := range nonPlaceholderRanges(text) {
		seg := text[sr[0]:sr[1]]
		for i, rule := range rules {
			if !ruleMayHit(seg, rule.Label) {
				continue
			}
			validator := builtinValidators[rule.Label]
			for _, loc := range rule.Pattern.FindAllStringSubmatchIndex(seg, -1) {
				orig := captureGroup(seg, loc, rule.Group)
				if orig == "" {
					continue
				}
				if validator != nil && !validator(orig) {
					continue
				}
				os, oe := captureGroupBounds(loc, rule.Group)
				cands = append(cands, cand{
					origStart: sr[0] + os,
					origEnd:   sr[0] + oe,
					isTerm:    rule.Label == "TERM",
					ruleIdx:   i,
					orig:      orig,
					rule:      rule,
				})
			}
		}
	}
	if len(cands) == 0 {
		return text
	}
	// 排序: origStart 升序 → TERM 优先 → origEnd 降序(同起点长匹配优先) → ruleIdx 升序。
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.origStart != b.origStart {
			return a.origStart < b.origStart
		}
		if a.isTerm != b.isTerm {
			return a.isTerm
		}
		if a.origEnd != b.origEnd {
			return a.origEnd > b.origEnd
		}
		return a.ruleIdx < b.ruleIdx
	})
	// 贪心选不重叠区间: origStart < lastEnd(与已选重叠)的丢弃, 避免嵌套占位符。
	var b strings.Builder
	prev := 0
	lastEnd := -1
	for _, c := range cands {
		if c.origStart < lastEnd {
			continue // 与已选区间重叠, 跳过(被覆盖区域整体跳过, 不产生嵌套占位符)
		}
		b.WriteString(text[prev:c.origStart])
		ph := mapping.Recall(c.orig, c.rule.Label)
		if !seen[c.orig] {
			seen[c.orig] = true
			*matches = append(*matches, Match{Label: c.rule.Label, Original: c.orig, Placeholder: ph})
		}
		b.WriteString(ph)
		prev = c.origEnd
		lastEnd = c.origEnd
	}
	b.WriteString(text[prev:])
	return b.String()
}

// nonPlaceholderRanges 返回 text 中非占位符片段的绝对区间 [start, end)。
// 与 nonPlaceholderSegments 逻辑一致, 仅返回区间而非字符串, 供 maskCandidates 计算候选绝对位置。
func nonPlaceholderRanges(text string) [][2]int {
	locs := PlaceholderRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return [][2]int{{0, len(text)}}
	}
	ranges := make([][2]int, 0, len(locs)+1)
	lastEnd := 0
	for _, loc := range locs {
		if loc[0] > lastEnd {
			ranges = append(ranges, [2]int{lastEnd, loc[0]})
		}
		lastEnd = loc[1]
	}
	if lastEnd < len(text) {
		ranges = append(ranges, [2]int{lastEnd, len(text)})
	}
	return ranges
}

// captureGroupBounds 返回捕获组在 submatch index loc 中的相对区间 [start, end)。
// group=0 取整匹配; group>0 取对应捕获组; 捕获组未参与匹配时返回 (-1,-1)。
func captureGroupBounds(loc []int, group int) (int, int) {
	if group == 0 {
		return loc[0], loc[1]
	}
	return loc[2*group], loc[2*group+1]
}

// ---- JSON 感知扫描 ----

// mapJSONStringValues 对合法 JSON 文本, 仅对解码后的字符串值调用 mask, 键名与非字符串字段
// (number/bool/null)不变。用 token 流式重建紧凑 JSON, 保留键序与数字原字面量, 只替换命中的
// 字符串值: 对紧凑输入, 除被脱敏的字符串值外字节逐字一致, 保证脱敏→还原 roundtrip 字节稳定
// (网关不应因脱敏改变请求体格式)。解码失败或非单值时回退为对整段文本调用 mask(纯文本处理)。
func mapJSONStringValues(body string, mask func(string) string) string {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var b strings.Builder
	if err := maskJSONStream(dec, &b, mask); err != nil {
		return mask(body) // 解码失败回退纯文本
	}
	if dec.More() {
		return mask(body) // 非单值(尾随垃圾), 回退纯文本
	}
	return b.String()
}

// maskJSONStream 读取一个 JSON 值并写入 b; 字符串值调用 mask, 键名与其他标量原样保留。
func maskJSONStream(dec *json.Decoder, b *strings.Builder, mask func(string) string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			return maskJSONObject(dec, b, mask)
		case '[':
			return maskJSONArray(dec, b, mask)
		}
		return fmt.Errorf("mask: unexpected delim %q", d)
	}
	writeJSONScalar(b, tok, mask)
	return nil
}

// maskJSONObject 读取对象内容并写入 b; 键原样(不脱敏), 值递归(字符串值 mask)。
func maskJSONObject(dec *json.Decoder, b *strings.Builder, mask func(string) string) error {
	b.WriteByte('{')
	first := true
	for dec.More() {
		if !first {
			b.WriteByte(',')
		}
		first = false
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		writeJSONString(b, key) // 键名不脱敏
		b.WriteByte(':')
		if err := maskJSONStream(dec, b, mask); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // 消耗 '}'
		return err
	}
	b.WriteByte('}')
	return nil
}

// maskJSONArray 读取数组内容并写入 b; 元素递归(字符串值 mask)。
func maskJSONArray(dec *json.Decoder, b *strings.Builder, mask func(string) string) error {
	b.WriteByte('[')
	first := true
	for dec.More() {
		if !first {
			b.WriteByte(',')
		}
		first = false
		if err := maskJSONStream(dec, b, mask); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // 消耗 ']'
		return err
	}
	b.WriteByte(']')
	return nil
}

// writeJSONScalar 写标量 token; string 调用 mask, json.Number 原字面量, bool/null 原样。
func writeJSONScalar(b *strings.Builder, tok json.Token, mask func(string) string) {
	switch v := tok.(type) {
	case string:
		writeJSONString(b, mask(v))
	case json.Number:
		b.WriteString(v.String()) // 保留原数字字面量(UseNumber)
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	}
}

// writeJSONString 写 JSON 字符串字面量(带引号 + 标准转义)。
func writeJSONString(b *strings.Builder, s string) {
	out, err := json.Marshal(s)
	if err != nil {
		// 不可达: 任意 string 都可 marshal; 兜底写裸串避免静默丢数据。
		b.WriteByte('"')
		b.WriteString(s)
		b.WriteByte('"')
		return
	}
	b.Write(out)
}
