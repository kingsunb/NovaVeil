package relay

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm/httpclient"
)

// contextProbeMaxTokens 是单次探测允许的最大规模。
const contextProbeMaxTokens = 1 << 20

// contextProbeAlphabet 用于生成不可压缩的填充文本。
const contextProbeAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// ContextProbeResult 是一次上下文探测的结论。
type ContextProbeResult struct {
	Tokens    int    `json:"tokens"`
	Accepted  bool   `json:"accepted"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

// ProbeContextWindow 用指定 token 规模的请求探测渠道模型能否接受。
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
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	latency := time.Since(started).Milliseconds()
	result := &ContextProbeResult{Tokens: tokens, Status: response.StatusCode, Detail: truncateSnippet(string(body)), LatencyMS: latency}
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
	finishTestRequest(logRequest, result.Detail, nil, probeErr)
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

// buildContextProbeText 按 4 字符约 1 token 的口径生成不可压缩填充文本。
func buildContextProbeText(tokens int) string {
	target := tokens * 4
	var builder strings.Builder
	builder.Grow(target + 16)
	for builder.Len() < target {
		length := 6 + rand.Intn(4)
		for i := 0; i < length; i++ {
			builder.WriteByte(contextProbeAlphabet[rand.Intn(len(contextProbeAlphabet))])
		}
		builder.WriteByte(' ')
	}
	return builder.String()
}
