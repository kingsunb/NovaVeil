package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
)

// contextProbeMaxTokens 是单次探测允许的最大规模。
const contextProbeMaxTokens = 1 << 20

// contextProbeWords 是填充文本的词汇表。
// 用随机常见英文单词而非随机字母数字串: 后者的 BPE 密度约 2.5-3 字符/token,
// 按 4 字符/token 生成会让实际 token 数比目标大 40% 左右; 英文单词序列的密度
// 接近自然文本, 与「4 字符约 1 token」的口径一致。
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
	// ActualTokens 是上游 usage 报告的输入 token 数; 0 表示上游未返回
	// (免费档被强制流式时, 前若干字节里拿不到末尾的 usage 事件)。
	ActualTokens int64 `json:"actual_tokens"`
}

// ProbeContextWindow 用指定 token 规模的请求探测渠道模型能否接受。
// 各家 tokenizer 密度差异极大(实测 4~7.5 字符/token), 固定口径必然偏差过半:
// 这里先发 4000 token 的小探针测出该「渠道+模型」的真实密度比并缓存,
// 之后按比例构造正式请求; 结果偏差超 1% 时用回写后的比例重试一次。
// 出站构造与业务转发同形(渠道配置、免费档改写、代理与 Bun 传输), 只按状态码判定容量,
// 不复用会校验回答内容的透传路径: 探测请求故意只留 1 个输出 token, 回答不可用属预期。
func ProbeContextWindow(ctx context.Context, channelID int, modelName string, tokens int) (*ContextProbeResult, error) {
	if strings.TrimSpace(modelName) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if tokens <= 0 || tokens > contextProbeMaxTokens {
		return nil, fmt.Errorf("tokens 超出范围: %d", tokens)
	}
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}
	ratio := 1.0
	if cached, ok := tokenDensityCache.Load(tokenDensityKey(channelID, modelName)); ok {
		ratio = clampTokenDensity(cached.(float64))
	} else {
		// 校准探针规模很小, 成本可忽略; 失败时不缓存, 下次探测重新校准。
		calib, calibErr := probeOnce(ctx, channel, modelName, probeCalibrationTokens, 1.0)
		if calibErr != nil {
			return nil, fmt.Errorf("校准探针失败: %w", calibErr)
		}
		if calib.Accepted && calib.ActualTokens > 0 {
			ratio = clampTokenDensity(float64(calib.ActualTokens) / float64(probeCalibrationTokens))
			tokenDensityCache.Store(tokenDensityKey(channelID, modelName), ratio)
		}
	}
	result, err := probeOnce(ctx, channel, modelName, tokens, ratio)
	if err != nil {
		return nil, err
	}
	// 用主请求的真实值回写密度比, 下次探测直接命中缓存。
	if result.ActualTokens > 0 {
		tokenDensityCache.Store(tokenDensityKey(channelID, modelName), clampTokenDensity(float64(result.ActualTokens)*ratio/float64(tokens)))
	}
	// 实际值与目标偏差超 1% 时, 用回写后的最新比例重试一次, 收敛到目标附近。
	if result.Accepted && result.ActualTokens > 0 {
		deviation := math.Abs(float64(result.ActualTokens-tokens)) / float64(tokens)
		if deviation > 0.01 {
			if updated, ok := tokenDensityCache.Load(tokenDensityKey(channelID, modelName)); ok {
				latest := clampTokenDensity(updated.(float64))
				if retry, retryErr := probeOnce(ctx, channel, modelName, tokens, latest); retryErr == nil && retry.ActualTokens > 0 {
					tokenDensityCache.Store(tokenDensityKey(channelID, modelName), clampTokenDensity(float64(retry.ActualTokens)*latest/float64(tokens)))
					result = retry
				}
			}
		}
	}
	return result, nil
}

// probeCalibrationTokens 是密度校准探针的目标规模, 足够小以控制成本。
// 密度比的存储(tokenDensityCache)与夹取(clampTokenDensity)在 context_fit.go,
// 与路由过滤共用同一份, 保证「测试判定」与「路由过滤」同尺。

// probeOnce 发送一次指定规模的探测请求并按状态码判定容量。
func probeOnce(ctx context.Context, channel model.Channel, modelName string, tokens int, ratio float64) (*ContextProbeResult, error) {
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
	raw, err := newTestRequest(format, modelName, buildContextProbeText(tokens, ratio), 1)
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

// buildContextProbeText 按「4 字符约 1 token」的基准口径生成填充文本,
// 并按校准出的密度比 ratio 反推字符量: ratio = actual/claimed, 比值越大说明
// 该模型 tokenizer 越省, 需要的字符数按比例增加。
func buildContextProbeText(tokens int, ratio float64) string {
	target := int(float64(tokens) * 4 / ratio)
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
