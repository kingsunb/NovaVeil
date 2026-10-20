package relay

// 可疑零输入用量的保守修复: 上游明确上报 usage 但 input 侧为 0 且请求非平凡时,
// 视为上游统计 bug 而非事实, 用本地估算替换并打 estimated 标记供面板区分。
// 注意与零输出重试机制(errZeroOutput)的分工: 输出为 0 由 protocol 层判整轮无效重试,
// 这里只负责 input 侧的记账修复, 不影响请求成败判定。

import (
	"slices"

	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

// suspiciousZeroMinBodyBytes 判定平凡请求的字节数下限, 低于此值视为空 ping 类请求, 不做修复。
const suspiciousZeroMinBodyBytes = 64

// suspiciousZeroEstimateSlack 输入估算上界夹击的富余字节数, 防估算随请求体异常放大。
const suspiciousZeroEstimateSlack = 8192

// sanitizeUsage 对上游上报的可疑用量做保守修复, 返回修复后的用量与是否打标。
// 规则(按序短路):
//   - usage 为 nil 且请求体平凡: 无可估算内容, 原样返回 nil;
//   - usage 非 nil 且 PromptTokens > 0: 无需修复, 原样返回 false;
//   - 缓存豁免: prompt 明细携带任意缓存类 token(cached/write_cached 及分时 TTL 字段)大于 0 时,
//     input 报 0 可能是全部命中缓存的真实口径, 不修复;
//   - 平凡请求豁免: 请求体不足 suspiciousZeroMinBodyBytes 字节, 或解析不出带非空文本的 messages
//     (如空 ping 探活), 属正常现象, 不修复;
//   - 其余情况(含 usage 为 nil 的失败请求)一律以请求体字节数近似估算输入 token
//     (est = max(1, len/3)), 并以请求体字节数派生的上界夹击防异常放大,
//     重算 TotalTokens = est + CompletionTokens。失败请求上游不回 usage, 但请求量确实
//     发出去了, 估算是日志里唯一能给出的输入规模。
//
// 返回值为深拷贝, 不改动调用方传入的原对象; 不修复时原样透传入参指针。
// 请求体参数为 string: 热路径上避免了 string→[]byte 的整包拷贝(gjson.Get 直接按字符串扫描)。
func sanitizeUsage(usage *llm.Usage, requestBody string) (*llm.Usage, bool) {
	if usage != nil && usage.PromptTokens > 0 {
		return usage, false
	}
	if usage != nil {
		if details := usage.PromptTokensDetails; details != nil &&
			(details.CachedTokens > 0 || details.WriteCachedTokens > 0 ||
				details.WriteCached5MinTokens > 0 || details.WriteCached1HourTokens > 0) {
			return usage, false
		}
	}
	if !nonTrivialRequestBody(requestBody) {
		return usage, false
	}

	fixed := llm.Usage{}
	if usage != nil {
		fixed = *usage
	}
	if fixed.PromptTokensDetails != nil {
		details := *fixed.PromptTokensDetails
		fixed.PromptTokensDetails = &details
	}
	if fixed.CompletionTokensDetails != nil {
		details := *fixed.CompletionTokensDetails
		fixed.CompletionTokensDetails = &details
	}
	fixed.PromptModalityTokenDetails = slices.Clone(fixed.PromptModalityTokenDetails)
	fixed.CompletionModalityTokenDetails = slices.Clone(fixed.CompletionModalityTokenDetails)

	estimated := max(int64(1), int64(len(requestBody))/3)
	estimated = min(estimated, int64(len(requestBody))*2+suspiciousZeroEstimateSlack)
	fixed.PromptTokens = estimated
	fixed.TotalTokens = estimated + fixed.CompletionTokens
	return &fixed, true
}

// nonTrivialRequestBody 判定原始请求体是否承载非平凡的对话内容:
// 字节数达到下限且能解析出至少一条 messages, 且各条 content 的文本总长非空
// (兼容字符串与分片数组两种 OpenAI Chat 形态)。其余情形一律按平凡请求豁免。
func nonTrivialRequestBody(body string) bool {
	if len(body) < suspiciousZeroMinBodyBytes {
		return false
	}
	messages := gjson.Get(body, "messages").Array()
	totalText := 0
	for _, message := range messages {
		content := message.Get("content")
		switch content.Type {
		case gjson.String:
			totalText += len(content.Str)
		case gjson.JSON:
			for _, part := range content.Array() {
				totalText += len(part.Get("text").Str)
			}
		}
	}
	return len(messages) > 0 && totalText > 0
}
