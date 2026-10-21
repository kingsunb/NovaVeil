package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// contextProbeMaxTokens 是单次探测允许的最大标称规模(十进制 1M)。
const contextProbeMaxTokens = 1_000_000

// contextProbeWords 是填充文本的词汇表。
// 用随机常见英文单词而非随机字母数字串: 后者的 BPE 密度约 2.5-3 字符/token,
// 按 4 字符/token 生成会让实际 token 数比目标大 40% 左右; 英文单词序列的密度
// 接近自然文本, 与「4 字符约 1 token」的基准口径一致。
var contextProbeWords = []string{
	"signal", "process", "network", "quality", "context", "buffer", "verify",
	"token", "channel", "request", "response", "upstream", "gateway", "latency",
	"budget", "stream", "retry", "cache", "probe", "limit", "scale", "record",
	"history", "invoice", "balance", "mountain", "river", "garden", "morning",
	"evening", "student", "teacher", "library", "machine", "engine", "picture",
	"weather", "travel", "holiday", "kitchen", "chapter", "opinion", "measure",
	"collect", "compare", "explain", "imagine", "deliver", "prepare", "consider",
}

// ContextProbeResult 是一次上下文探测的结论。
type ContextProbeResult struct {
	Tokens    int    `json:"tokens"`
	Accepted  bool   `json:"accepted"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
	// ActualTokens 是上游报告的实际输入 token 数: 优先取响应 usage, 被拒时取
	// 拒绝体里的 requested N tokens; 0 表示未取到, 此时不做密度校准与重测。
	ActualTokens int64 `json:"actual_tokens"`
}

// ProbeContextWindow 按标称规模探测渠道模型的上下文容量。
// 实测到 ActualTokens 即写密度缓存。
// 超 10% 容差时按实测密度重测一次并覆盖缓存。
// 出站构造与业务转发同形, 探测请求只留 1 个输出 token。
func ProbeContextWindow(ctx context.Context, channelID int, modelName string, tokens int) (*ContextProbeResult, error) {
	if strings.TrimSpace(modelName) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if tokens <= 0 || tokens > contextProbeMaxTokens {
		return nil, fmt.Errorf("tokens 超出范围: %d", tokens)
	}
	// 留出冗余再探测: 贴上限容易因 chat 模板开销或上游计数差异踩线。
	// 标称值原样返回给调用方(result.Tokens), 「应用」写回的窗口即标称而非扣除后的值。
	nominal := tokens
	target := probeTargetTokens(nominal)
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}
	constructed := probeConstructedTokens(target, modelTokenDensity(channel.BaseURL, modelName))
	result, err := probeOnce(ctx, channel, modelName, constructed)
	if err != nil {
		return nil, err
	}
	result.Tokens = nominal
	if result.ActualTokens > 0 {
		measured := recordProbeDensity(channel.BaseURL, modelName, int(result.ActualTokens), constructed)
		if math.Abs(float64(result.ActualTokens)-float64(nominal)) > float64(probeTolerance(nominal)) {
			result = probeRecalibrate(ctx, channel, modelName, nominal, measured, result)
		}
	}
	return result, nil
}

// recordProbeDensity 写入实测密度并返回夹取后的比值。
func recordProbeDensity(baseURL, modelName string, actual, constructed int) float64 {
	if constructed <= 0 {
		return 1
	}
	density := clampTokenDensity(float64(actual) / float64(constructed))
	tokenDensityCache.Store(tokenDensityKey(baseURL, modelName), density)
	return density
}

// probeRecalibrate 按实测密度重测一次并覆盖密度缓存。
// 重测失败保留首测结论。
// 校准失败不升级成探测失败。
func probeRecalibrate(ctx context.Context, channel model.Channel, modelName string, nominal int, measured float64, first *ContextProbeResult) *ContextProbeResult {
	retryConstructed := probeConstructedTokens(probeRetryTarget(nominal), measured)
	retry, err := probeOnce(ctx, channel, modelName, retryConstructed)
	if err != nil {
		return first
	}
	retry.Tokens = nominal
	if retry.ActualTokens > 0 {
		recordProbeDensity(channel.BaseURL, modelName, int(retry.ActualTokens), retryConstructed)
	}
	return retry
}

// probeTargetTokens 计算扣除冗余后的首测基准规模:
// 1M 档按 2% 留(1,000,000 → 980,000), 其余档位留 1,000 token。
func probeTargetTokens(tokens int) int {
	margin := 1000
	if tokens >= 1_000_000 {
		margin = tokens / 50
	}
	target := tokens - margin
	if target < 1 {
		target = 1
	}
	return target
}

// probeTolerance 返回实测值与标称值允许的偏差: 标称的 10%(1M 档即 100K)。
func probeTolerance(nominal int) int {
	return max(1, nominal/10)
}

// probeRetryTarget 返回超差重测的目标规模: 容忍下限(标称-容差, 1M 档即 900K)。
func probeRetryTarget(nominal int) int {
	target := nominal - probeTolerance(nominal)
	if target < 1 {
		return 1
	}
	return target
}

// probeConstructedTokens 把基准口径的目标规模换算成当前实测密度下的构造规模。
func probeConstructedTokens(target int, ratio float64) int {
	if ratio <= 0 {
		return target
	}
	constructed := int(float64(target) / ratio)
	if constructed < 1 {
		return 1
	}
	return constructed
}

// overflowRequestedRe 从拒绝体提取「你实际发了多少 token」的报告值。
var overflowRequestedRe = regexp.MustCompile(`(?i)requested (?:about )?([\d,]+) tokens`)

// promptTooLongRe 覆盖 Anthropic 的 prompt is too long 措辞。
var promptTooLongRe = regexp.MustCompile(`(?i)prompt is too long:?\s+([\d,]+) tokens`)

// parseRequestedOverflowTokens 解析上游口径的请求规模, 拿不到返回 false。
// 例: OpenRouter 拒绝体「you requested about 1817292 tokens」→ 1817292。
func parseRequestedOverflowTokens(detail string) (int64, bool) {
	for _, re := range []*regexp.Regexp{overflowRequestedRe, promptTooLongRe} {
		match := re.FindStringSubmatch(detail)
		if match == nil {
			continue
		}
		value, err := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
		if err == nil && value > 0 {
			return value, true
		}
	}
	return 0, false
}

// probeOnce 发送一次指定构造规模的探测请求并按状态码判定容量。
func probeOnce(ctx context.Context, channel model.Channel, modelName string, tokens int) (*ContextProbeResult, error) {
	candidates := channelKeyTestCandidates(channel)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("渠道未配置任何密钥")
	}
	effective, err := effectiveChannelForKey(channel, candidates[0])
	if err != nil {
		return nil, err
	}
	// 免费档按出口 IP 轮换: 探测必须用池内账号, 否则永远撞同一个被标记的 IP。
	proxyAccount := ""
	if proxyNeedsOpencodeAccount(channel) {
		candidate := pickOpencodeProxyAccount(ctx, channel.ID)
		if resolved, resolveErr := helper.ResolveProxyTemplate(*channel.ChannelProxy, candidate); resolveErr == nil {
			addr := resolved.String()
			effective.ChannelProxy = &addr
			proxyAccount = candidate
		}
	}
	format, _, target := testRequestTarget(effective, modelName, "")
	raw, err := newTestRequest(format, modelName, buildContextProbeText(tokens), 1)
	if err != nil {
		return nil, fmt.Errorf("构造探测请求失败: %w", err)
	}
	// 探测也进请求日志面板, 便于与真实请求对照排查。
	logRequest := startTestRequest(modelName, raw.Body, target, nil)
	started := time.Now()
	built, err := buildPassthroughRequest(format, raw, effective, uuid.NewString(), generateOpencodeSessionID())
	if err != nil {
		finishTestRequest(logRequest, "", nil, err)
		return nil, fmt.Errorf("构造出站请求失败: %w", err)
	}
	if isOpencodeFreeModel(effective, modelName) {
		applyOpencodeFreeLaneHeaders(effective, built, modelName)
		if isChatFormat(format) {
			built.Body, _ = shapeOpencodeFreeBody(built.Body, isOpencodeV1Lane(effective))
		}
	}
	// 收敛输出预算, 探测只测输入容量。
	built.Body = clampProbeOutputBudget(built.Body, format)
	if len(built.JSONBody) > 0 {
		built.JSONBody = slices.Clone(built.Body)
	}
	client, err := helper.ChannelHttpClient(&effective)
	if err != nil {
		finishTestRequest(logRequest, "", nil, err)
		return nil, fmt.Errorf("构造渠道客户端失败: %w", err)
	}
	httpReq, err := httpclient.BuildHttpRequest(ctx, built)
	if err != nil {
		finishTestRequest(logRequest, "", nil, err)
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	response, err := client.Do(httpReq)
	if err != nil {
		finishTestRequest(logRequest, "", nil, err)
		return nil, fmt.Errorf("探测请求失败: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	latency := time.Since(started).Milliseconds()
	usage, actual := parseProbeUsage(body, raw.Body)
	result := &ContextProbeResult{
		Tokens:    tokens,
		Status:    response.StatusCode,
		Detail:    truncateSnippet(string(body)),
		LatencyMS: latency,
	}
	if actual {
		result.ActualTokens = usage.PromptTokens
	} else if reported, ok := parseRequestedOverflowTokens(string(body)); ok {
		// 完整响应体解析, 截断只用于展示。
		result.ActualTokens = reported
	}
	var probeErr error
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Accepted = true
	case response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusRequestEntityTooLarge ||
		response.StatusCode == http.StatusUnprocessableEntity:
		// 只有响应体指向上下文超限才是容量结论; 其余 4xx 多为模型或参数问题。
		if looksLikeContextOverflow(result.Detail) {
			result.Accepted = false
		} else {
			probeErr = fmt.Errorf("请求被拒绝但不像是上下文超限: %s %s", response.Status, result.Detail)
		}
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		probeErr = fmt.Errorf("探测被认证、限流或上游故障拒绝: %s %s", response.Status, result.Detail)
	default:
		probeErr = fmt.Errorf("上游返回异常状态: %s %s", response.Status, result.Detail)
	}
	finishTestRequest(logRequest, result.Detail, usage, probeErr)
	// 探测结果回填账号池: 成功记成功, 403 按 IP 被标记冷却, 下次自动换号。
	if proxyAccount != "" {
		switch {
		case result.Accepted:
			markOpencodeProxySuccess(channel.ID, proxyAccount)
		case response.StatusCode == http.StatusForbidden:
			markOpencodeProxyForbidden(channel.ID, proxyAccount)
		}
	}
	if probeErr != nil {
		return nil, probeErr
	}
	return result, nil
}

// clampProbeOutputBudget 把探测输出预算收敛到 1。
func clampProbeOutputBudget(body []byte, format llm.APIFormat) []byte {
	clamped := body
	var err error
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		if clamped, err = sjson.SetBytes(clamped, "max_tokens", 1); err != nil {
			return body
		}
		if clamped, err = sjson.DeleteBytes(clamped, "reasoning_effort"); err != nil {
			return body
		}
	case llm.APIFormatOpenAIResponse:
		if clamped, err = sjson.SetBytes(clamped, "max_output_tokens", 1); err != nil {
			return body
		}
		if clamped, err = sjson.DeleteBytes(clamped, "reasoning"); err != nil {
			return body
		}
	case llm.APIFormatAnthropicMessage:
		if clamped, err = sjson.DeleteBytes(clamped, "thinking"); err != nil {
			return body
		}
		if clamped, err = sjson.SetBytes(clamped, "max_tokens", 1); err != nil {
			return body
		}
	}
	return clamped
}

// contextOverflowHints 是上游报上下文超限时的常见措辞, 用于把容量结论与参数错误分开。
var contextOverflowHints = []string{
	"context_length", "context length", "context window", "maximum context",
	"too many tokens", "too long", "exceed", "max_tokens", "token limit",
	"上下文", "超出", "过长", "太长",
}

// looksLikeContextOverflow 判断拒绝原因是否指向上下文容量。
func looksLikeContextOverflow(detail string) bool {
	lowered := strings.ToLower(detail)
	for _, hint := range contextOverflowHints {
		if strings.Contains(lowered, hint) {
			return true
		}
	}
	return false
}

// truncateSnippet 截断响应片段, 按 UTF-8 边界裁剪避免出现半个汉字。
func truncateSnippet(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	cut := 500
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}

// buildContextProbeText 按「4 字符约 1 token」的基准口径生成指定规模的填充文本。
// 密度修正由调用方在传入规模前完成, 本函数只保证基准口径的字符量。
func buildContextProbeText(tokens int) string {
	target := tokens * 4
	var builder strings.Builder
	builder.Grow(target + 16)
	for builder.Len() < target {
		builder.WriteString(contextProbeWords[rand.Intn(len(contextProbeWords))])
		builder.WriteByte(' ')
	}
	return builder.String()
}

// parseProbeUsage 从响应体里取上游统计的用量: 先按非流式 JSON 取, 再扫 SSE 的
// data 行取最后一个带 usage 的事件。都取不到时按请求体估算, 与转发路径同口径,
// 避免日志里显示 0/0。第二个返回值为 true 表示这是上游上报的真实值。
func parseProbeUsage(body []byte, requestBody []byte) (*llm.Usage, bool) {
	if usage, ok := usageFromPayload(body); ok {
		return usage, true
	}
	var found *llm.Usage
	for _, line := range strings.Split(string(body), "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if usage, ok := usageFromPayload([]byte(payload)); ok {
			found = usage
		}
	}
	if found != nil {
		return found, true
	}
	estimated, _ := sanitizeUsage(&llm.Usage{}, string(requestBody))
	return estimated, false
}

// usageFromPayload 解析单个 JSON 载荷里的 usage 字段, 兼容 OpenAI 与 Anthropic 口径。
func usageFromPayload(payload []byte) (*llm.Usage, bool) {
	raw := gjson.GetBytes(payload, "usage")
	if !raw.Exists() || !raw.IsObject() {
		return nil, false
	}
	var usage llm.Usage
	if err := json.Unmarshal([]byte(raw.Raw), &usage); err != nil {
		return nil, false
	}
	if usage.PromptTokens == 0 {
		usage.PromptTokens = raw.Get("input_tokens").Int()
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = raw.Get("output_tokens").Int()
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	// Anthropic 的缓存读量不在 prompt_tokens_details 里, 单独映射。
	if usage.PromptTokensDetails == nil {
		if cached := raw.Get("cache_read_input_tokens").Int(); cached > 0 {
			usage.PromptTokensDetails = &llm.PromptTokensDetails{CachedTokens: cached}
		}
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return nil, false
	}
	return &usage, true
}
