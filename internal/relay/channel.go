package relay

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/doubao"
	"github.com/looplj/axonhub/llm/transformer/gemini"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// clientHeaderPlaceholder 匹配自定义 Header 值中引用的客户端请求头占位符 {client_header:xxx}。
// 运行时将 xxx 替换为客户端请求头 xxx 的实际值, 用于透传 User-Agent、X-Request-Id 等头到上游。
var clientHeaderPlaceholder = regexp.MustCompile(`\{client_header:[^}]+\}`)

// supportsNativeFormat 判断客户端协议是否为这一跳的原生格式; 同协议时可整包透传, 否则需经 pipeline 转换。
// 当渠道开启 PassThroughBodyEnabled (完全渠道透传) 时, 任意客户端协议均视为原生格式, 直接原样透传至上游。
// channelModel 提供 OpenCode 模型行上的上游协议; 为空或协议为空时仍按渠道类型判断。
func supportsNativeFormat(channel model.Channel, channelModel *model.ChannelModel, format llm.APIFormat) bool {
	// 完全渠道透传: 渠道声明上游支持多协议, 任意客户端格式均原样透传。
	// custom (固定回复) 渠道除外 — 它不对接真实上游, 透传无意义。
	// 完全透传优先于模型协议。
	if channel.PassThroughBodyEnabled && channel.Type != model.ChannelProviderCustom {
		return true
	}
	switch outboundProvider(channel, channelModel) {
	case model.ChannelProviderOpenAI:
		// OpenAI 渠道原生支持全部 OpenAI 系接口: 对话、图片生成/编辑/变体、视频、语音合成/转写/翻译、嵌入。
		switch format {
		case llm.APIFormatOpenAIChatCompletion,
			llm.APIFormatOpenAIImageGeneration,
			llm.APIFormatOpenAIImageEdit,
			llm.APIFormatOpenAIImageVariation,
			llm.APIFormatOpenAIVideo,
			llm.APIFormatOpenAISpeech,
			llm.APIFormatOpenAITranscription,
			llm.APIFormatOpenAITranslation,
			llm.APIFormatOpenAIEmbedding:
			return true
		}
		return false
	case model.ChannelProviderOpenAIResponses:
		return format == llm.APIFormatOpenAIResponse
	case model.ChannelProviderAnthropic:
		return format == llm.APIFormatAnthropicMessage
	default:
		return false
	}
}

// clientFormatLabel 返回客户端协议在日志中的展示名。
func clientFormatLabel(format llm.APIFormat) string {
	switch format {
	case llm.APIFormatOpenAIResponse:
		return "openai_responses"
	case llm.APIFormatAnthropicMessage:
		return "anthropic"
	case llm.APIFormatOpenAIImageGeneration:
		return "openai_image_generation"
	case llm.APIFormatOpenAIImageEdit:
		return "openai_image_edit"
	case llm.APIFormatOpenAIImageVariation:
		return "openai_image_variation"
	case llm.APIFormatOpenAIVideo:
		return "openai_video"
	case llm.APIFormatOpenAISpeech:
		return "openai_audio_speech"
	case llm.APIFormatOpenAITranscription:
		return "openai_audio_transcription"
	case llm.APIFormatOpenAITranslation:
		return "openai_audio_translation"
	case llm.APIFormatOpenAIEmbedding:
		return "openai_embedding"
	default:
		return "openai_chat"
	}
}

// upstreamTypeLabel 返回这一跳实际上游协议在日志中的展示名; 与 clientFormatLabel 统一为
// 下划线机器可读形式 (openai_chat / openai_responses / anthropic …), 便于前后端按
// 字符串直接比对协议。自定义固定回复渠道保留中文人类可读名, 属不同协议类型, 不在统一范围内。
// 完全透传或没有模型协议时展示渠道类型; OpenCode 模型协议非空时展示该协议。
func upstreamTypeLabel(channel model.Channel, channelModel *model.ChannelModel) string {
	channelType := outboundProvider(channel, channelModel)
	if channelType == model.ChannelProviderOpenAI {
		return "openai_chat"
	}
	if channelType == model.ChannelProviderCustom {
		return "自定义固定回复"
	}
	return string(channelType)
}

// outboundProvider 决定这一跳出站转换器使用的协议。
// 顺序: 完全透传仍用渠道类型; OpencodeCompat 且模型协议为 chat/responses/anthropic
// 时用该协议; 其余情况用 channel.Type。自定义渠道即使写了协议也忽略。
func outboundProvider(channel model.Channel, channelModel *model.ChannelModel) model.ChannelProvider {
	if channel.PassThroughBodyEnabled && channel.Type != model.ChannelProviderCustom {
		return channel.Type
	}
	if provider, ok := modelProtocolProvider(channel, channelModel); ok {
		return provider
	}
	return channel.Type
}

// modelProtocolProvider 在 OpenCode 兼容渠道上把模型协议映射到现有转换器。
// 自定义渠道、未开启 OpencodeCompat、协议为空或无法识别时返回 false。
func modelProtocolProvider(channel model.Channel, channelModel *model.ChannelModel) (model.ChannelProvider, bool) {
	if channel.Type == model.ChannelProviderCustom || !channel.OpencodeCompat || channelModel == nil {
		return "", false
	}
	switch channelModel.UpstreamProtocol {
	case model.UpstreamProtocolChat:
		return model.ChannelProviderOpenAI, true
	case model.UpstreamProtocolResponses:
		return model.ChannelProviderOpenAIResponses, true
	case model.UpstreamProtocolAnthropic:
		return model.ChannelProviderAnthropic, true
	default:
		return "", false
	}
}

// keylessPlaceholderKey 无密钥渠道在转换路径使用的占位凭据。
// openai/openai_responses/volcengine 出站转换器无条件构造 Bearer 认证, 空明文会在
// pipeline 的 FinalizeAuthHeaders 阶段以 "bearer token is required" 失败(该阶段先于
// OnOutboundRawRequest 钩子执行); 先用占位符让认证合法写入, 再由钩子统一删除认证头,
// 保证无密钥渠道经转换后的上游请求同样不携带任何认证头。
const keylessPlaceholderKey = "keyless-placeholder"

// buildOutbound 按这一跳的有效协议构造出站转换器, 并判断客户端请求能否直接透传。
// channelModel 参与 OpenCode 模型协议选择; 完全透传时忽略该协议。
func buildOutbound(channel model.Channel, channelModel *model.ChannelModel, format llm.APIFormat) (transformer.Outbound, bool, error) {
	key := channel.PrimaryKey()
	if key == "" {
		key = keylessPlaceholderKey
	}
	staticKey := auth.NewStaticKeyProvider(key)
	passthrough := supportsNativeFormat(channel, channelModel, format)
	switch outboundProvider(channel, channelModel) {
	case model.ChannelProviderOpenAI:
		outbound, err := openai.NewOutboundTransformerWithConfig(&openai.Config{PlatformType: openai.PlatformOpenAI, BaseURL: channel.BaseURL, APIKeyProvider: staticKey})
		return outbound, passthrough, err
	case model.ChannelProviderOpenAIResponses:
		outbound, err := responses.NewOutboundTransformerWithConfig(&responses.Config{BaseURL: channel.BaseURL, APIKeyProvider: staticKey})
		return outbound, passthrough, err
	case model.ChannelProviderAnthropic:
		outbound, err := anthropic.NewOutboundTransformerWithConfig(&anthropic.Config{Type: anthropic.PlatformDirect, BaseURL: channel.BaseURL, APIKeyProvider: staticKey})
		return outbound, passthrough, err
	case model.ChannelProviderGemini:
		outbound, err := gemini.NewOutboundTransformerWithConfig(gemini.Config{BaseURL: channel.BaseURL, APIKeyProvider: staticKey})
		return outbound, false, err
	case model.ChannelProviderVolcengine:
		outbound, err := doubao.NewOutboundTransformerWithConfig(&doubao.Config{BaseURL: channel.BaseURL, APIKeyProvider: staticKey})
		return outbound, false, err
	case model.ChannelProviderCustom:
		// 固定回复渠道按 OpenAI Chat 出站构造: 真实传输被 cannedTransport 顶替,
		// BaseURL 仅为占位, 永远不会被拨号; 永不透传, 一律经管线转换到客户端协议。
		outbound, err := openai.NewOutboundTransformerWithConfig(&openai.Config{PlatformType: openai.PlatformOpenAI, BaseURL: cannedBaseURL, APIKeyProvider: staticKey})
		return outbound, false, err
	default:
		return nil, false, fmt.Errorf("unsupported channel provider: %s", channel.Type)
	}
}

// thinkingBudgets 返回思考等级在各协议下对应的 token 预算, Anthropic 与 Gemini 使用。
// max 档在 Gemini 上使用 -1 表示交由模型动态决定最大思考预算。
func thinkingBudgets(level string) (anthropicBudget int, geminiBudget int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "minimal":
		return 1024, 512, true
	case "low":
		return 2048, 1024, true
	case "medium":
		return 8192, 8192, true
	case "high":
		return 16384, 24576, true
	case "xhigh":
		return 32768, 32768, true
	case "max":
		return 64000, -1, true
	default:
		return 0, 0, false
	}
}

// openAIEffortLevel 校验等级是否为 OpenAI reasoning_effort 的合法取值。
func openAIEffortLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return normalized, true
	default:
		return "", false
	}
}

// lookupModelLimit 按请求模型名读取按模型限制配置, 先精确匹配再忽略大小写匹配。
func lookupModelLimit(limits map[string]model.ChannelModelLimit, modelName string) (model.ChannelModelLimit, bool) {
	if len(limits) == 0 || modelName == "" {
		return model.ChannelModelLimit{}, false
	}
	if limit, ok := limits[modelName]; ok {
		return limit, true
	}
	for name, limit := range limits {
		if strings.EqualFold(name, modelName) {
			return limit, true
		}
	}
	return model.ChannelModelLimit{}, false
}

// applyChannelModelLimits 将模型输出与思考配置按渠道协议写入上游请求体。
// 经过出站转换后请求体始终是渠道协议的原生格式, 因此这里可以按渠道类型直接注入。
// 限制按请求中的真实模型名从渠道的 model_limits 中读取, 未配置则保持上游默认行为。
func applyChannelModelLimits(channel model.Channel, request *httpclient.Request) error {
	modelName := gjson.GetBytes(request.Body, "model").String()
	limit, _ := lookupModelLimit(channel.ModelLimits, modelName)
	maxOutput := limit.MaxOutput
	thinkingLevel := limit.ThinkingLevel

	if maxOutput == nil && thinkingLevel == "" {
		return nil
	}

	body := request.Body
	set := func(path string, value interface{}) error {
		next, err := sjson.SetBytes(body, path, value)
		if err != nil {
			return fmt.Errorf("apply channel model limit %q: %w", path, err)
		}
		body = next
		return nil
	}

	level := strings.ToLower(strings.TrimSpace(thinkingLevel))

	switch channel.Type {
	case model.ChannelProviderOpenAI:
		if maxOutput != nil && *maxOutput > 0 {
			if err := set("max_tokens", *maxOutput); err != nil {
				return err
			}
		}
		// off 表示该渠道不接受思考参数: 除不注入外还要删除转换层写入的 reasoning_effort
		// (如 Anthropic 自适应思考经协议转换映射出的取值), 否则客户端的思考偏好仍会发给
		// 不支持该参数的上游并触发校验错误; 未配置时维持上游默认行为。
		if level == "off" {
			next, delErr := sjson.DeleteBytes(body, "reasoning_effort")
			if delErr != nil {
				return fmt.Errorf("delete channel reasoning_effort: %w", delErr)
			}
			body = next
		} else if level != "" {
			if effort, ok := openAIEffortLevel(level); ok {
				if err := set("reasoning_effort", effort); err != nil {
					return err
				}
			}
		}
	case model.ChannelProviderOpenAIResponses:
		if maxOutput != nil && *maxOutput > 0 {
			if err := set("max_output_tokens", *maxOutput); err != nil {
				return err
			}
		}
		if level != "" && level != "off" {
			if effort, ok := openAIEffortLevel(level); ok {
				if err := set("reasoning.effort", effort); err != nil {
					return err
				}
			}
		}
	case model.ChannelProviderAnthropic:
		budget, _, hasBudget := thinkingBudgets(level)
		maxTokens := 0
		if maxOutput != nil {
			maxTokens = *maxOutput
		}
		// Anthropic 要求 max_tokens 大于思考预算, 配置过小时自动抬高避免上游直接拒绝。
		if hasBudget && maxTokens > 0 && maxTokens <= budget {
			maxTokens = budget + 1024
		}
		if maxTokens > 0 {
			if err := set("max_tokens", maxTokens); err != nil {
				return err
			}
		}
		if hasBudget {
			if err := set("thinking.type", "enabled"); err != nil {
				return err
			}
			if err := set("thinking.budget_tokens", budget); err != nil {
				return err
			}
		}
	case model.ChannelProviderGemini:
		if maxOutput != nil && *maxOutput > 0 {
			if err := set("generationConfig.maxOutputTokens", *maxOutput); err != nil {
				return err
			}
		}
		// Gemini 用 0 表示关闭思考, 其余等级映射为具体预算。
		_, geminiBudget, hasBudget := thinkingBudgets(level)
		if level == "off" || hasBudget {
			budget := 0
			if hasBudget {
				budget = geminiBudget
			}
			if err := set("generationConfig.thinkingConfig.thinkingBudget", budget); err != nil {
				return err
			}
		}
	case model.ChannelProviderVolcengine:
		if maxOutput != nil && *maxOutput > 0 {
			if err := set("max_tokens", *maxOutput); err != nil {
				return err
			}
		}
		// 豆包思考模型仅区分开与关。
		if level == "off" {
			if err := set("thinking.type", "disabled"); err != nil {
				return err
			}
		} else if level != "" {
			if err := set("thinking.type", "enabled"); err != nil {
				return err
			}
		}
	}

	request.Body = body
	if len(request.JSONBody) > 0 {
		request.JSONBody = slices.Clone(body)
	}
	return nil
}

// applyChannelConfig 按渠道配置覆盖上游请求的参数并追加自定义 Header; model 与 stream 由转发流程决定, 不允许覆盖。
// randomValue 为请求级一次性解析的随机头值, 同一请求的普通动态头与所有重试复用此值。
// opencodeSession 可选: 传入时在动态头之后覆盖 x-opencode-session, 不把该值写进其他动态头。
// 未传入时保持探测路径的旧行为(由 injectRandomHeaders 用 randomValue 占位)。
func applyChannelConfig(channel model.Channel, request *httpclient.Request, randomValue string, opencodeSession ...string) error {
	contentType := request.Headers.Get("Content-Type")
	// multipart/form-data 请求(图片编辑/变体、语音转写/翻译): 跳过全部 JSON 参数改写
	// (sjson 对 multipart 体会产生垃圾), 仅应用自定义 Header 与动态头。
	// 非对话类 JSON 接口(图片生成/视频/语音合成/嵌入): 跳过 max_tokens/reasoning_effort
	// 等对话参数注入, ParamOverride 仍生效。
	chatFormat := isChatFormat(llm.APIFormat(request.APIFormat))
	if !strings.HasPrefix(contentType, "multipart/") && chatFormat {
		if err := applyChannelModelLimits(channel, request); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(contentType, "multipart/") &&
		channel.ParamOverride != nil && *channel.ParamOverride != "" {
		var overrides map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*channel.ParamOverride), &overrides); err != nil {
			return fmt.Errorf("invalid channel parameter override: %w", err)
		}
		body := request.Body
		// 覆盖键可能自带点号或冒号, 转义后再作为 sjson 路径使用, 避免被解析成嵌套路径。
		escape := strings.NewReplacer("\\", "\\\\", ".", "\\.", ":", "\\:")
		for key, value := range overrides {
			if key == "model" || key == "stream" {
				continue
			}
			next, err := sjson.SetRawBytes(body, ":"+escape.Replace(key), value)
			if err != nil {
				return fmt.Errorf("apply channel parameter %q: %w", key, err)
			}
			body = next
		}
		request.Body = body
		if len(request.JSONBody) > 0 {
			request.JSONBody = slices.Clone(body)
		}
	}

	// 转换器已经写入的认证等敏感 Header 不允许被自定义配置覆盖。
	for _, header := range channel.CustomHeader {
		if request.Headers.Get(header.HeaderKey) != "" && httpclient.IsSensitiveHeader(header.HeaderKey) {
			continue
		}
		// 值中的 {client_header:xxx} 片段替换为客户端请求头 xxx 的实际值。
		value := clientHeaderPlaceholder.ReplaceAllStringFunc(header.HeaderValue, func(placeholder string) string {
			return request.Headers.Get(placeholder[len("{client_header:") : len(placeholder)-1])
		})
		request.Headers.Set(header.HeaderKey, value)
	}
	// 动态头在静态自定义 Header 之后注入。普通动态头共用 randomValue。
	// 转发路径传入的 opencode 会话号在这之后覆盖 x-opencode-session, 调试日志看到的是最终值。
	injectRandomHeaders(channel, randomValue, request)
	if len(opencodeSession) > 0 {
		applyResolvedOpencodeSession(channel, opencodeSession[0], request)
	}
	// [debug] 打印最终发往上游的完整请求头, 便于排查 opencode 兼容头等注入是否生效。
	// 仅 Debug 级别输出; Authorization / X-Api-Key / X-Goog-Api-Key 仅显示前缀, 不泄露完整凭据。
	if log.GetLevel() <= log.DebugLevel {
		keys := make([]string, 0, len(request.Headers))
		for k := range request.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			v := request.Headers.Get(k)
			if httpclient.IsSensitiveHeader(k) && len(v) > 8 {
				v = v[:8] + "***"
			}
			sb.WriteString("\n  ")
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
		}
		log.Debugf("channel %d(%s) outbound headers:%s", channel.ID, channel.Name, sb.String())
	}
	return nil
}
