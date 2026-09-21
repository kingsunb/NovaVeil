package model

import (
	"errors"
	"strings"
)

// 渠道模型的上游原生协议。空字符串表示沿用渠道类型，与未引入该字段时的行为一致。
const (
	UpstreamProtocolChat      = "chat"
	UpstreamProtocolResponses = "responses"
	UpstreamProtocolAnthropic = "anthropic"
)

// CanonicalUpstreamProtocol 校验并规范化模型行上的上游协议。
// 只接受空字符串、chat、responses、anthropic；前后空白会被去掉。
func CanonicalUpstreamProtocol(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case UpstreamProtocolChat, UpstreamProtocolResponses, UpstreamProtocolAnthropic:
		return strings.TrimSpace(raw), nil
	default:
		return "", errInvalidUpstreamProtocol
	}
}

// errInvalidUpstreamProtocol 是协议取值超出允许集合时的错误。
var errInvalidUpstreamProtocol = errors.New("上游协议只允许空、chat、responses、anthropic")

// OpenCodeTier 按渠道 BaseURL 区分 OpenCode 的 Zen 与 Go 档。
// 无法识别时返回空字符串。Go 必须先于 Zen 判断，因为 /zen/go 同时包含 /zen。
func OpenCodeTier(baseURL string) string {
	value := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case strings.Contains(value, "/zen/go"):
		return "go"
	case strings.Contains(value, "/zen"):
		return "zen"
	default:
		return ""
	}
}

// OpencodeZenSeedProtocols 是从 models.opencode.ai 的 opencode 提供方核实过的
// Zen 出厂手工模型协议。这些模型没有单独的 provider.npm，继承提供方
// @ai-sdk/openai-compatible，对应 Chat Completions。未列入的名字不猜测。
var OpencodeZenSeedProtocols = map[string]string{
	"deepseek-v4-flash-free":      UpstreamProtocolChat,
	"mimo-v2.5-free":              UpstreamProtocolChat,
	"hy3-free":                    UpstreamProtocolChat,
	"nemotron-3-ultra-free":       UpstreamProtocolChat,
	"nemotron-3.5-lightning-free": UpstreamProtocolChat,
	"laguna-s-2.1-free":           UpstreamProtocolChat,
}

// OpencodeSeedProtocols 返回该 BaseURL 档位上已核实的出厂协议副本。
// Go 档出厂没有手工模型，公开目录里也没有与 Zen 六个名字相同的条目，因此返回 nil。
func OpencodeSeedProtocols(baseURL string) map[string]string {
	if OpenCodeTier(baseURL) != "zen" {
		return nil
	}
	out := make(map[string]string, len(OpencodeZenSeedProtocols))
	for name, protocol := range OpencodeZenSeedProtocols {
		out[name] = protocol
	}
	return out
}
