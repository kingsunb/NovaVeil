package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/gjson"
)

// probeResult 是一次测试/体检请求的结论。
type probeResult struct {
	Accepted  bool
	Status    int
	Detail    string
	LatencyMS int64
}

// ocProbeSelectChannelKey 按 Key ID 选渠道密钥; 空 ID 仅非 keyed 渠道沿用首条。
func ocProbeSelectChannelKey(channel model.Channel, keyID string) (model.ChannelKey, error) {
	candidates := channelKeyTestCandidates(channel)
	if len(candidates) == 0 {
		return model.ChannelKey{}, fmt.Errorf("渠道未配置任何密钥")
	}
	if keyID == "" {
		if channel.Keyed {
			return model.ChannelKey{}, fmt.Errorf("密钥型渠道需指定渠道 Key")
		}
		return candidates[0], nil
	}
	for _, candidate := range candidates {
		if candidate.ID == keyID {
			return candidate, nil
		}
	}
	return model.ChannelKey{}, fmt.Errorf("渠道密钥 %q 不存在", keyID)
}

// probeOnceWithAccount 发送一次测试请求并按状态码判定结论。
// account 为空时从池内选号, 非空时钉住账号供批量账号体检复用。
func probeOnceWithAccount(ctx context.Context, channel model.Channel, modelName, keyID, account string, buildRaw func(llm.APIFormat) (*httpclient.Request, error)) (*probeResult, error) {
	if isWarpProxy(channel) {
		return nil, errWarpProbeUnsupported
	}
	selected, err := ocProbeSelectChannelKey(channel, keyID)
	if err != nil {
		return nil, err
	}
	effective, err := effectiveChannelForKey(channel, selected)
	if err != nil {
		return nil, err
	}
	proxyAccount := ""
	if proxyNeedsOpencodeAccount(channel) {
		candidate := account
		if candidate == "" {
			candidate = pickOpencodeProxyAccountForKey(ctx, channel.ID, keyID, "")
		}
		resolved, resolveErr := helper.ResolveProxyTemplate(*channel.ChannelProxy, candidate)
		if resolveErr != nil {
			return nil, fmt.Errorf("代理模板解析失败")
		}
		addr := resolved.String()
		effective.ChannelProxy = &addr
		proxyAccount = candidate
	}
	format, _, target := testRequestTarget(effective, modelName, "")
	raw, err := buildRaw(format)
	if err != nil {
		return nil, fmt.Errorf("构造测试请求失败: %w", err)
	}
	logRequest := startTestRequest(modelName, raw.Body, target, nil)
	if account != "" {
		mu.Lock()
		logRequest.AccountProbe = true
		logRequest.ClientIP = ocProbeClientIP
		publishRequestLocked(logRequest)
		mu.Unlock()
	}
	started := time.Now()
	built, err := buildPassthroughRequest(format, raw, effective, uuid.NewString(), generateOpencodeSessionID())
	if err != nil {
		finishTestRequest(logRequest, "", nil, err)
		return nil, fmt.Errorf("构造出站请求失败: %w", err)
	}
	if isOpencodeFreeModel(effective, modelName) {
		applyOpencodeFreeLaneHeaders(effective, built, modelName)
		if isChatFormat(format) {
			built.Body, _, _ = shapeOpencodeFreeBody(built.Body, isOpencodeV1Lane(effective), formatToProtocol(format))
		} else if formatToProtocol(format) == model.UpstreamProtocolResponses {
			built.Body, _ = ensureOfficialOpencodeInstructions(built.Body, opencodeLaneKey(effective), modelName)
		}
	}
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
		return nil, fmt.Errorf("测试请求失败: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	latency := time.Since(started).Milliseconds()
	usage, _ := parseProbeUsage(body, raw.Body)
	result := &probeResult{
		Status:    response.StatusCode,
		Detail:    truncateSnippet(string(body)),
		LatencyMS: latency,
	}
	var probeErr error
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Accepted = true
	case response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusRequestEntityTooLarge ||
		response.StatusCode == http.StatusUnprocessableEntity:
		if looksLikeContextOverflow(result.Detail) {
			result.Accepted = false
		} else {
			probeErr = fmt.Errorf("请求被拒绝但不像是上下文超限: %s %s", response.Status, result.Detail)
		}
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		probeErr = fmt.Errorf("测试被认证、限流或上游故障拒绝: %s %s", response.Status, result.Detail)
	default:
		probeErr = fmt.Errorf("上游返回异常状态: %s %s", response.Status, result.Detail)
	}
	finishTestRequest(logRequest, result.Detail, usage, probeErr)
	if proxyAccount != "" {
		switch {
		case result.Accepted:
			markOpencodeProxySuccessForKey(channel.ID, keyID, proxyAccount, true)
		case response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests:
			markOpencodeProxyRejectedForKey(channel.ID, keyID, proxyAccount)
		}
	}
	if probeErr != nil {
		return result, probeErr
	}
	return result, nil
}

// contextOverflowHints 是上游报上下文超限时的常见措辞。
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

// parseProbeUsage 从响应体里取上游统计的用量, 取不到时按请求体估算。
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
	estimated, _ := sanitizeUsage(&llm.Usage{}, string(requestBody), defaultTokenDensity)
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
