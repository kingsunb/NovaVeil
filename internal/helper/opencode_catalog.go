package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// opencodeCatalogURL 是 OpenCode 公开能力目录。按提供方区分 Zen 与 Go，
// 用 npm SDK 判断 chat / responses / anthropic，同时提取上下文窗口、输出上限、
// 推理/工具调用能力与输入模态供管理台展示。
const opencodeCatalogURL = "https://models.opencode.ai/api.json"

// opencodeCatalogMaxBytes 限制目录响应体，避免异常响应把同步任务撑满内存。
const opencodeCatalogMaxBytes = 16 << 20

// OpencodeModelInfo 是目录里单个模型的协议与能力元数据。
type OpencodeModelInfo struct {
	Protocol           string   // chat / responses / anthropic。
	Name               string   // 目录展示名; 客户端形态消息用, 空表示目录未提供。
	ContextWindow      int      // 上下文窗口大小(token); 0 表示目录未提供。
	MaxOutput          int      // 最大输出 token; 0 表示目录未提供。
	Reasoning          bool     // 是否支持推理(thinking/reasoning)。
	ToolCall           bool     // 是否支持工具调用(function calling)。
	InputModalities    []string // 输入模态: text/image/audio/video/pdf 等。
	MaxReasoningEffort string   // 目录声明的最大思考等级; 空表示未声明可选等级。
}

// FetchOpencodeCatalog 读取公开目录，返回该渠道档位上已识别的模型协议与能力元数据。
// 键只包含能映射到 chat/responses/anthropic 的模型；目录失败返回 error，
// 调用方必须保留模型行上已有的协议。BaseURL 不是 Zen/Go 时返回空表且无错误。
func FetchOpencodeCatalog(ctx context.Context, channel model.Channel) (map[string]OpencodeModelInfo, error) {
	tier := model.OpenCodeTier(channel.BaseURL)
	if tier == "" {
		return map[string]OpencodeModelInfo{}, nil
	}
	if err := ResolveChannelProxyTemplate(&channel); err != nil {
		return nil, err
	}
	client, err := ChannelHttpClient(&channel)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opencodeCatalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build opencode catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("opencode catalog returned HTTP %d: %s", resp.StatusCode, readUpstreamErrorSnippet(resp.Body))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, opencodeCatalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > opencodeCatalogMaxBytes {
		return nil, fmt.Errorf("opencode catalog exceeds %d bytes", opencodeCatalogMaxBytes)
	}
	return ParseOpencodeCatalog(body, tier)
}

type opencodeCatalogFile map[string]opencodeCatalogProvider

type opencodeCatalogProvider struct {
	ID     string                          `json:"id"`
	API    string                          `json:"api"`
	NPM    string                          `json:"npm"`
	Models map[string]opencodeCatalogModel `json:"models"`
}

type opencodeCatalogModel struct {
	ID               string                           `json:"id"`
	Name             string                           `json:"name"`
	Provider         *opencodeCatalogModelProvider    `json:"provider"`
	Limit            *opencodeCatalogModelLimit       `json:"limit"`
	Reasoning        bool                             `json:"reasoning"`
	ToolCall         bool                             `json:"tool_call"`
	Modalities       *opencodeCatalogModalities       `json:"modalities"`
	ReasoningOptions []opencodeCatalogReasoningOption `json:"reasoning_options"`
}

type opencodeCatalogReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type opencodeCatalogModelProvider struct {
	NPM string `json:"npm"`
}

type opencodeCatalogModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type opencodeCatalogModalities struct {
	Input []string `json:"input"`
}

// maxEffortFromOptions 取 effort 枚举里最大的等级; none 不是等级, 跳过。
func maxEffortFromOptions(options []opencodeCatalogReasoningOption) string {
	for _, option := range options {
		if option.Type != "effort" {
			continue
		}
		for i := len(option.Values) - 1; i >= 0; i-- {
			level := strings.ToLower(strings.TrimSpace(option.Values[i]))
			if level == "" || level == "none" {
				continue
			}
			return level
		}
	}
	return ""
}

// ParseOpencodeCatalog 从目录 JSON 抽出指定档位的模型协议与能力元数据。
// tier 为 zen 或 go。提供方 id 精确匹配优先（opencode / opencode-go）；
// 没有精确 id 时才用 api 地址里的 /zen 或 /zen/go 兜底。
// 模型级 provider.npm 覆盖提供方 npm。无法识别的 SDK 不进入结果。
func ParseOpencodeCatalog(data []byte, tier string) (map[string]OpencodeModelInfo, error) {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier != "zen" && tier != "go" {
		return map[string]OpencodeModelInfo{}, nil
	}
	var file opencodeCatalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 OpenCode 目录失败: %w", err)
	}
	exactID := "opencode"
	if tier == "go" {
		exactID = "opencode-go"
	}
	var exact, fallback []opencodeCatalogProvider
	for key, provider := range file {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			id = key
		}
		if !catalogProviderMatchesTier(id, provider.API, tier) {
			continue
		}
		if strings.EqualFold(id, exactID) {
			exact = append(exact, provider)
		} else {
			fallback = append(fallback, provider)
		}
	}
	chosen := exact
	if len(chosen) == 0 {
		chosen = fallback
	}
	out := make(map[string]OpencodeModelInfo)
	for _, provider := range chosen {
		for name, entry := range provider.Models {
			modelName := strings.TrimSpace(entry.ID)
			if modelName == "" {
				modelName = strings.TrimSpace(name)
			}
			if modelName == "" {
				continue
			}
			npm := provider.NPM
			if entry.Provider != nil && strings.TrimSpace(entry.Provider.NPM) != "" {
				npm = entry.Provider.NPM
			}
			protocol, ok := protocolForSDK(npm)
			if !ok {
				continue
			}
			if _, exists := out[modelName]; exists {
				continue
			}
			info := OpencodeModelInfo{Protocol: protocol, Name: strings.TrimSpace(entry.Name)}
			info.MaxReasoningEffort = maxEffortFromOptions(entry.ReasoningOptions)
			if entry.Limit != nil {
				info.ContextWindow = entry.Limit.Context
				info.MaxOutput = entry.Limit.Output
			}
			info.Reasoning = entry.Reasoning
			info.ToolCall = entry.ToolCall
			if entry.Modalities != nil {
				info.InputModalities = entry.Modalities.Input
			}
			out[modelName] = info
		}
	}
	return out, nil
}

func catalogProviderMatchesTier(id, api, tier string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	api = strings.ToLower(strings.TrimSpace(api))
	goProvider := id == "opencode-go" || strings.Contains(api, "opencode.ai/zen/go")
	switch tier {
	case "go":
		return goProvider
	case "zen":
		if goProvider {
			return false
		}
		return id == "opencode" || strings.Contains(api, "opencode.ai/zen")
	default:
		return false
	}
}

// protocolForSDK 把目录里的提供方 SDK 映射到出站协议。
// @ai-sdk/openai 的默认语言模型走 Responses API；
// @ai-sdk/openai-compatible 走 Chat Completions；
// 包名含 anthropic 的走 Messages。其余 SDK 不猜测。
func protocolForSDK(npm string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(npm))
	switch {
	case value == "":
		return "", false
	case strings.Contains(value, "anthropic"):
		return model.UpstreamProtocolAnthropic, true
	case strings.Contains(value, "openai-compatible"):
		return model.UpstreamProtocolChat, true
	case value == "@ai-sdk/openai" || strings.HasSuffix(value, "/openai"):
		return model.UpstreamProtocolResponses, true
	default:
		return "", false
	}
}
