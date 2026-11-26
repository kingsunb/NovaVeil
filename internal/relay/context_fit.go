package relay

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/tidwall/gjson"
)

// errContextWindowTooSmall 表示全部候选成员的手动窗口都装不下本请求。
var errContextWindowTooSmall = errors.New("没有候选模型的手动上下文窗口能容纳当前请求")

// mediaTokenCost 单个媒体片段(图片/音频/视频)的固定 token 估算, 粗估用。
const mediaTokenCost = 1000

// mediaBaseEquivalent 把媒体固定 token 折算回字符基准口径, 供密度换算前叠加。
var mediaBaseEquivalent = int(math.Round(mediaTokenCost / defaultTokenDensity))

// mediaPartTypes 视为媒体载体的内容块类型(OpenAI Chat/Responses、Anthropic 等)。
var mediaPartTypes = map[string]bool{
	"image_url": true, "input_image": true, "image": true,
	"input_audio": true, "audio_url": true, "audio": true,
	"video_url": true, "video": true,
}

// mediaPayloadPaths 媒体块内承载 base64 或 URL 的字段, 取最长值视作载荷。
var mediaPayloadPaths = []string{
	"image_url.url", "image_url", "source.data", "source.url",
	"input_audio.data", "audio_url.url", "video_url.url", "video_url",
	"inline_data.data", "inlineData.data", "file_data.file_uri", "data",
}

// estimateRequestTokens 按字符宽度粗估请求 token 数: 非 ASCII 字按 1, ASCII 每 4 字按 1。
// 媒体载荷(image/audio/video 的 base64 或 URL)不计字符, 每个媒体按固定值折算。
func estimateRequestTokens(body []byte) int {
	narrow, wide := countBodyWidthBytes(body)
	payload, media := scanMediaBytes(body)
	return tokensFromWidths(narrow, wide, payload, media)
}

// estimateRequestTokensText 与字节版同口径, 避免热路径整包转换。
func estimateRequestTokensText(body string) int {
	narrow, wide := countBodyWidthText(body)
	payload, media := scanMediaText(body)
	return tokensFromWidths(narrow, wide, payload, media)
}

// tokensFromWidths 汇总字符宽度并扣除媒体载荷, 再叠加每个媒体的固定 token。
func tokensFromWidths(narrow, wide, payload, media int) int {
	narrow -= payload
	if narrow < 0 {
		narrow = 0
	}
	return wide + narrow/4 + media*mediaBaseEquivalent
}

func countBodyWidthBytes(body []byte) (narrow, wide int) {
	for i := 0; i < len(body); {
		r, size := utf8.DecodeRune(body[i:])
		if r >= 0x80 {
			wide++
		} else {
			narrow++
		}
		i += size
	}
	return
}

func countBodyWidthText(body string) (narrow, wide int) {
	for _, r := range body {
		if r >= 0x80 {
			wide++
		} else {
			narrow++
		}
	}
	return
}

func scanMediaBytes(body []byte) (payload, count int) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return 0, 0
	}
	walkMedia(gjson.ParseBytes(trimmed), &payload, &count)
	return
}

func scanMediaText(body string) (payload, count int) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return 0, 0
	}
	walkMedia(gjson.Parse(trimmed), &payload, &count)
	return
}

// walkMedia 递归遍历 JSON, 命中媒体块时累计载荷长度与数量, 且不再深入该块。
func walkMedia(v gjson.Result, payload, count *int) {
	if v.IsObject() {
		if isMediaObject(v) {
			*payload += mediaPayloadLen(v)
			*count++
			return
		}
		v.ForEach(func(_, child gjson.Result) bool {
			walkMedia(child, payload, count)
			return true
		})
		return
	}
	if v.IsArray() {
		v.ForEach(func(_, child gjson.Result) bool {
			walkMedia(child, payload, count)
			return true
		})
	}
}

func isMediaObject(v gjson.Result) bool {
	if mediaPartTypes[v.Get("type").String()] {
		return true
	}
	return v.Get("inline_data").Exists() || v.Get("inlineData").Exists() || v.Get("file_data").Exists()
}

func mediaPayloadLen(v gjson.Result) int {
	longest := 0
	for _, path := range mediaPayloadPaths {
		if r := v.Get(path); r.Type == gjson.String && len(r.Str) > longest {
			longest = len(r.Str)
		}
	}
	return longest
}

const defaultTokenDensity = op.DefaultTokenDensity

func requestTokenDensitySnapshot(channelID int, baseURL, modelName string) op.TokenDensitySnapshot {
	snapshot, err := op.TokenDensitySnapshotGet(context.Background(), channelID, baseURL, modelName)
	if err != nil {
		log.Warnf("token density load %d:%s failed: %v", channelID, modelName, err)
	}
	return snapshot
}

func modelTokenDensity(channelID int, baseURL, modelName string) float64 {
	return requestTokenDensitySnapshot(channelID, baseURL, modelName).Density
}

func observeTokenDensity(snapshot op.TokenDensitySnapshot, promptTokens int64, body string) {
	if _, err := op.TokenDensityObserve(context.Background(), snapshot, promptTokens, estimateRequestTokensText(body)); err != nil {
		log.Warnf("token density observation failed: %v", err)
	}
}

// estimateModelTokens 估算请求 token。
func estimateModelTokens(body []byte, channelID int, baseURL, modelName string) int {
	return estimateModelTokensFromBase(estimateRequestTokens(body), channelID, baseURL, modelName)
}

// estimateModelTokensFromBase 按密度换算基准估算。
func estimateModelTokensFromBase(base int, channelID int, baseURL, modelName string) int {
	return scaleBaseTokens(base, modelTokenDensity(channelID, baseURL, modelName))
}

// scaleBaseTokens 按密度比换算基准估算, 夹取到至少 1。
func scaleBaseTokens(base int, density float64) int {
	if base <= 0 {
		return 0
	}
	scaled := int(float64(base) * density)
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// estimateDefaultTokens 按未实测默认口径换算, 供拒绝消息与过滤同尺。
func estimateDefaultTokens(base int) int {
	return scaleBaseTokens(base, defaultTokenDensity)
}

// contextFitsWindow 报告请求能否装进窗口; 窗口未知视为放行, 交上游判定。
func contextFitsWindow(body []byte, contextWindow int, channelID int, baseURL, modelName string) bool {
	if contextWindow <= 0 {
		return true
	}
	return estimateModelTokens(body, channelID, baseURL, modelName) <= contextWindow
}

// roundContextObservation 汇总一轮尝试的上下文可视信息。
type roundContextObservation struct {
	EstimatedTokens int
	EffectiveWindow int
	Density         float64
	DensitySource   string
	DensitySamples  int64
}

// observeRoundContext 计算本轮成员的预估 token、生效窗口与密度来源。
func observeRoundContext(channel model.Channel, channelModel *model.ChannelModel, body []byte) roundContextObservation {
	obs := roundContextObservation{}
	if channelModel == nil {
		return obs
	}
	snapshot := requestTokenDensitySnapshot(channel.ID, channel.BaseURL, channelModel.Name)
	obs.EstimatedTokens = scaleBaseTokens(estimateRequestTokens(body), snapshot.Density)
	obs.EffectiveWindow = effectiveContextWindow(channel, channelModel)
	obs.Density = snapshot.Density
	obs.DensitySource = "default"
	obs.DensitySamples = snapshot.SampleCount()
	if snapshot.Learned() {
		obs.DensitySource = "learned"
	}
	return obs
}

// contextFitState 归纳一次扫描的过滤状态, 供日志识别未配置/被跳过/可容纳。
func contextFitState(scan contextFitScan) string {
	if len(scan.skipped) > 0 {
		return "skipped"
	}
	if scan.candidates > 0 && scan.maxWindow == 0 {
		return "unconfigured"
	}
	return "fits"
}

type contextFitScan struct {
	baseTokens      int
	candidates      int
	fitting         int
	maxWindow       int
	maxWindowTokens int
	skipped         map[int]bool
	blockedRefs     map[int]bool
}

// scanContextFit 在选路前标记窗口不足的可达叶子。
// 基准口径只对请求体估一次, 成员按各自密度比换算。
func scanContextFit(group model.Group, body []byte) contextFitScan {
	scan := contextFitScan{
		baseTokens:  estimateRequestTokens(body),
		skipped:     make(map[int]bool),
		blockedRefs: make(map[int]bool),
	}
	scan.scanGroup(group, 0, make(map[int]bool))
	return scan
}

func (s *contextFitScan) scanGroup(group model.Group, refDepth int, visiting map[int]bool) {
	if visiting[group.ID] {
		return
	}
	visiting[group.ID] = true
	defer delete(visiting, group.ID)

	for _, item := range group.Items {
		if group.Mode == model.GroupModeManual && item.ID != group.ActiveItemID {
			continue
		}
		if item.IsGroupRef() {
			if refDepth >= model.MaxGroupRefDepth {
				continue
			}
			ref, err := groupLookupFunc(item.RefGroupName)
			if err != nil {
				continue
			}
			beforeCandidates, beforeFitting := s.candidates, s.fitting
			s.scanGroup(ref, refDepth+1, visiting)
			if s.candidates > beforeCandidates && s.fitting == beforeFitting {
				s.blockedRefs[item.ID] = true
				clearSessionStickyByItem(group.ID, item.ID)
			}
			continue
		}
		if item.ChannelModel == nil {
			s.candidates++
			s.fitting++
			continue
		}
		channel, err := channelLookupFunc(item.ChannelModel.ChannelID)
		if err == nil && !channel.Enabled {
			continue
		}
		s.candidates++
		if err != nil {
			s.fitting++
			continue
		}
		window := effectiveContextWindow(channel, item.ChannelModel)
		if window > s.maxWindow {
			s.maxWindow = window
			s.maxWindowTokens = estimateModelTokensFromBase(s.baseTokens, channel.ID, channel.BaseURL, item.ChannelModel.Name)
		}
		if window <= 0 ||
			estimateModelTokensFromBase(s.baseTokens, channel.ID, channel.BaseURL, item.ChannelModel.Name) <= window {
			s.fitting++
			continue
		}
		s.skipped[item.ID] = true
		clearSessionStickyByItem(group.ID, item.ID)
	}
}
