package relay

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// errContextWindowTooSmall 表示全部候选成员的手动窗口都装不下本请求。
var errContextWindowTooSmall = errors.New("没有候选模型的手动上下文窗口能容纳当前请求")

// estimateRequestTokens 按字符宽度粗估请求 token 数: 非 ASCII 字按 1, ASCII 每 4 字按 1。
// 全渠道共用这一基准口径; 真实流量观测出的密度比统一在 estimateModelTokens 叠加。
func estimateRequestTokens(body []byte) int {
	narrow, wide := 0, 0
	for i := 0; i < len(body); {
		r, size := utf8.DecodeRune(body[i:])
		if r >= 0x80 {
			wide++
		} else {
			narrow++
		}
		i += size
	}
	return wide + narrow/4
}

// estimateRequestTokensText 与字节版同口径, 避免热路径整包转换。
func estimateRequestTokensText(body string) int {
	narrow, wide := 0, 0
	for _, r := range body {
		if r >= 0x80 {
			wide++
		} else {
			narrow++
		}
	}
	return wide + narrow/4
}

// defaultTokenDensity 未观测默认密度。
const defaultTokenDensity = 4.0 / 7.1

// tokenDensityAlpha 密度平滑权重。
const tokenDensityAlpha = 0.2

// tokenDensityMinBase 最小观测基准。
const tokenDensityMinBase = 2000

const (
	tokenDensityBootstrapSamples = 10
	tokenDensityDailyLimit       = 3
	tokenDensityTolerance        = 0.03
)

type tokenDensityState struct {
	Density      float64
	Samples      int64
	DailySamples int
	SampleDay    string
}

// tokenDensityCache 密度缓存。
var tokenDensityCache sync.Map

// tokenDensityUpdateMu 密度更新锁。
var tokenDensityUpdateMu sync.Mutex

// tokenDensitySaveLocks 按 key 串行落库。
var tokenDensitySaveLocks sync.Map

// tokenDensityReported 观测日志标记。
var tokenDensityReported sync.Map

func tokenDensityKey(channelID int, baseURL, modelName string) string {
	if channelID > 0 {
		return "c:" + strconv.Itoa(channelID) + ":" + modelName
	}
	return "u:" + strings.TrimRight(baseURL, "/") + ":" + modelName
}

// clampTokenDensity 限制密度范围。
func clampTokenDensity(d float64) float64 {
	if d < 0.25 {
		return 0.25
	}
	if d > 16 {
		return 16
	}
	return d
}

// modelTokenDensity 读取密度。
func modelTokenDensity(channelID int, baseURL, modelName string) float64 {
	key := tokenDensityKey(channelID, baseURL, modelName)
	if cached, ok := tokenDensityCache.Load(key); ok {
		if state, ok := cached.(tokenDensityState); ok && state.Density > 0 {
			return state.Density
		}
	}
	if channelID <= 0 || modelName == "" {
		return defaultTokenDensity
	}
	row, found, err := op.TokenDensityGet(context.Background(), channelID, modelName)
	if err != nil {
		log.Debugf("token density load %d:%s failed: %v", channelID, modelName, err)
		fallback := tokenDensityState{Density: defaultTokenDensity}
		if actual, loaded := tokenDensityCache.LoadOrStore(key, fallback); loaded {
			if cached, ok := actual.(tokenDensityState); ok && cached.Density > 0 {
				return cached.Density
			}
		}
		return fallback.Density
	}
	state := tokenDensityState{Density: defaultTokenDensity}
	if found && row.Density > 0 {
		state = tokenDensityState{
			Density:      clampTokenDensity(row.Density),
			Samples:      row.SampleCount,
			DailySamples: row.DailySamples,
			SampleDay:    row.SampleDay,
		}
	}
	if actual, loaded := tokenDensityCache.LoadOrStore(key, state); loaded {
		if cached, ok := actual.(tokenDensityState); ok && cached.Density > 0 {
			return cached.Density
		}
	}
	return state.Density
}

// tokenDensityDay 返回观测日期。
func tokenDensityDay(t time.Time) string {
	return t.Format("2006-01-02")
}

// observeTokenDensity 校准密度。
func observeTokenDensity(channelID int, baseURL, modelName string, promptTokens int64, body string) {
	if modelName == "" || promptTokens <= 0 {
		return
	}
	base := estimateRequestTokensText(body)
	if base < tokenDensityMinBase {
		return
	}
	observed := clampTokenDensity(float64(promptTokens) / float64(base))
	day := tokenDensityDay(time.Now())
	key := tokenDensityKey(channelID, baseURL, modelName)
	tokenDensityUpdateMu.Lock()
	state, ok := tokenDensityCache.Load(key)
	current := tokenDensityState{}
	if ok {
		current, _ = state.(tokenDensityState)
	}
	if current.Samples <= 0 && channelID > 0 {
		if row, found, err := op.TokenDensityGet(context.Background(), channelID, modelName); err == nil && found && row.Density > 0 {
			current = tokenDensityState{
				Density:      clampTokenDensity(row.Density),
				Samples:      row.SampleCount,
				DailySamples: row.DailySamples,
				SampleDay:    row.SampleDay,
			}
		}
	}
	daily := current.DailySamples
	if current.SampleDay != day {
		daily = 0
	}
	if current.Samples >= tokenDensityBootstrapSamples && daily >= tokenDensityDailyLimit {
		tokenDensityUpdateMu.Unlock()
		return
	}
	next := observed
	samples := current.Samples + 1
	if current.Samples < tokenDensityBootstrapSamples {
		if current.Samples > 0 {
			next = clampTokenDensity(current.Density + tokenDensityAlpha*(observed-current.Density))
		}
		if samples == tokenDensityBootstrapSamples {
			daily = 0
		} else {
			daily++
		}
	} else {
		switch {
		case math.Abs(observed-current.Density) <= current.Density*tokenDensityTolerance:
			next = current.Density
		default:
			next = observed
		}
		daily++
	}
	tokenDensityCache.Store(key, tokenDensityState{
		Density:      next,
		Samples:      samples,
		DailySamples: daily,
		SampleDay:    day,
	})
	tokenDensityUpdateMu.Unlock()
	if channelID > 0 {
		persistTokenDensity(key, channelID, modelName, tokenDensityState{
			Density:      next,
			Samples:      samples,
			DailySamples: daily,
			SampleDay:    day,
		})
	}
	if _, reported := tokenDensityReported.LoadOrStore(key, struct{}{}); !reported {
		log.Infof("token density %d:%s observed=%.4f ewma=%.4f prompt=%d base=%d samples=%d daily=%d", channelID, modelName, observed, next, promptTokens, base, samples, daily)
	} else {
		log.Debugf("token density %d:%s observed=%.4f ewma=%.4f prompt=%d base=%d samples=%d daily=%d", channelID, modelName, observed, next, promptTokens, base, samples, daily)
	}
}

// persistTokenDensity 内存更新后落库。
func persistTokenDensity(key string, channelID int, modelName string, state tokenDensityState) {
	lockValue, _ := tokenDensitySaveLocks.LoadOrStore(key, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	cached, ok := tokenDensityCache.Load(key)
	if !ok {
		return
	}
	current, ok := cached.(tokenDensityState)
	if !ok || current != state {
		return
	}
	if err := op.TokenDensitySave(context.Background(), channelID, modelName, state.Density, state.Samples, state.DailySamples, state.SampleDay); err != nil {
		log.Warnf("token density save %d:%s failed: %v", channelID, modelName, err)
	}
}

// InvalidateTokenDensity 清单个缓存。
func InvalidateTokenDensity(channelID int, modelName string) {
	key := tokenDensityKey(channelID, "", modelName)
	tokenDensityCache.Delete(key)
	tokenDensityReported.Delete(key)
}

// InvalidateChannelTokenDensity 清渠道全部密度缓存。
func InvalidateChannelTokenDensity(channelID int) {
	prefix := "c:" + strconv.Itoa(channelID) + ":"
	tokenDensityCache.Range(func(key, _ any) bool {
		if text, ok := key.(string); ok && strings.HasPrefix(text, prefix) {
			tokenDensityCache.Delete(text)
			tokenDensityReported.Delete(text)
		}
		return true
	})
}

// ClearTokenDensityCache 清全部缓存。
func ClearTokenDensityCache() {
	tokenDensityCache.Range(func(key, _ any) bool {
		tokenDensityCache.Delete(key)
		return true
	})
	tokenDensityReported.Range(func(key, _ any) bool {
		tokenDensityReported.Delete(key)
		return true
	})
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
		window := effectiveContextWindow(channel.ModelLimits, item.ChannelModel)
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
