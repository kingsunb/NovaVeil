package op

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultTokenDensity 冷启动密度。
const DefaultTokenDensity = 4.0 / 7.1

// TokenDensityMinBase 最小样本基准。
const TokenDensityMinBase = 2000

const (
	tokenDensityAlpha            = 0.2
	tokenDensityBootstrapSamples = 10
	tokenDensityDailyLimit       = 3
	tokenDensityTolerance        = 0.03
)

type tokenDensityKey struct {
	channelID int
	model     string
}

type tokenDensityEntry struct {
	key      tokenDensityKey
	baseURL  string
	row      model.TokenDensity
	reported bool
}

// TokenDensitySnapshot 绑定观测生命周期。
type TokenDensitySnapshot struct {
	Density float64
	entry   *tokenDensityEntry
}

// Learned 报告密度是否来自真实流量观测。
func (s TokenDensitySnapshot) Learned() bool {
	return s.entry != nil && s.entry.row.SampleCount > 0
}

// SampleCount 返回已观测样本数, 未观测为 0。
func (s TokenDensitySnapshot) SampleCount() int64 {
	if s.entry == nil {
		return 0
	}
	return s.entry.row.SampleCount
}

var (
	tokenDensityMu    sync.Mutex
	tokenDensityCache = make(map[tokenDensityKey]*tokenDensityEntry)
)

// TokenDensitySnapshotGet 读取当前观测凭据。
func TokenDensitySnapshotGet(ctx context.Context, channelID int, baseURL, modelName string) (TokenDensitySnapshot, error) {
	fallback := TokenDensitySnapshot{Density: DefaultTokenDensity}
	if channelID <= 0 || modelName == "" || db.GetDB() == nil {
		return fallback, nil
	}
	tokenDensityMu.Lock()
	defer tokenDensityMu.Unlock()
	key := tokenDensityKey{channelID: channelID, model: modelName}
	if entry, ok := tokenDensityCache[key]; ok {
		if entry.baseURL != baseURL {
			return fallback, nil
		}
		return TokenDensitySnapshot{Density: entry.row.Density, entry: entry}, nil
	}
	var target struct {
		BaseURL string
	}
	err := db.GetDB().WithContext(ctx).Table("channel_models").
		Select("channels.base_url").
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Where("channel_models.channel_id = ? AND channel_models.name = ?", channelID, modelName).
		Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	if target.BaseURL != baseURL {
		return fallback, nil
	}
	row, found, err := TokenDensityGet(ctx, channelID, modelName)
	if err != nil {
		return fallback, err
	}
	if !found {
		row = model.TokenDensity{ChannelID: channelID, Model: modelName, Density: DefaultTokenDensity}
	}
	row.Density = clampTokenDensity(row.Density)
	entry := &tokenDensityEntry{key: key, baseURL: baseURL, row: row}
	tokenDensityCache[key] = entry
	return TokenDensitySnapshot{Density: row.Density, entry: entry}, nil
}

// TokenDensityObserve 仅写回有效观测。
func TokenDensityObserve(ctx context.Context, snapshot TokenDensitySnapshot, promptTokens int64, baseTokens int) (bool, error) {
	if snapshot.entry == nil {
		return false, nil
	}
	tokenDensityMu.Lock()
	defer tokenDensityMu.Unlock()
	entry := snapshot.entry
	if tokenDensityCache[entry.key] != entry {
		return false, nil
	}
	next, accepted := nextTokenDensity(entry.row, promptTokens, baseTokens, time.Now())
	if !accepted {
		return false, nil
	}
	if err := tokenDensitySave(ctx, next.ChannelID, next.Model, next.Density, next.SampleCount, next.DailySamples, next.SampleDay); err != nil {
		return false, err
	}
	entry.row = next
	observed := clampTokenDensity(float64(promptTokens) / float64(baseTokens))
	if !entry.reported {
		entry.reported = true
		log.Infof("token density %d:%s observed=%.4f ewma=%.4f prompt=%d base=%d samples=%d daily=%d", next.ChannelID, next.Model, observed, next.Density, promptTokens, baseTokens, next.SampleCount, next.DailySamples)
	} else {
		log.Debugf("token density %d:%s observed=%.4f ewma=%.4f prompt=%d base=%d samples=%d daily=%d", next.ChannelID, next.Model, observed, next.Density, promptTokens, baseTokens, next.SampleCount, next.DailySamples)
	}
	return true, nil
}

func nextTokenDensity(current model.TokenDensity, promptTokens int64, baseTokens int, now time.Time) (model.TokenDensity, bool) {
	if promptTokens <= 0 || baseTokens < TokenDensityMinBase {
		return current, false
	}
	day := now.Format("2006-01-02")
	daily := current.DailySamples
	if current.SampleDay != day {
		daily = 0
	}
	if current.SampleCount >= tokenDensityBootstrapSamples && daily >= tokenDensityDailyLimit {
		return current, false
	}
	observed := clampTokenDensity(float64(promptTokens) / float64(baseTokens))
	next := observed
	samples := current.SampleCount + 1
	if current.SampleCount < tokenDensityBootstrapSamples {
		if current.SampleCount > 0 {
			next = clampTokenDensity(current.Density + tokenDensityAlpha*(observed-current.Density))
		}
		if samples == tokenDensityBootstrapSamples {
			daily = 0
		} else {
			daily++
		}
	} else {
		if math.Abs(observed-current.Density) <= current.Density*tokenDensityTolerance {
			next = current.Density
		}
		daily++
	}
	current.Density = next
	current.SampleCount = samples
	current.DailySamples = daily
	current.SampleDay = day
	return current, true
}

func clampTokenDensity(density float64) float64 {
	return max(0.25, min(16, density))
}

func invalidateChannelTokenDensity(channelID int) {
	for key := range tokenDensityCache {
		if key.channelID == channelID {
			delete(tokenDensityCache, key)
		}
	}
}

func invalidateMissingModelTokenDensity(channelID int, models []model.ChannelModel) {
	retained := make(map[string]bool, len(models))
	for _, channelModel := range models {
		retained[channelModel.Name] = true
	}
	for key := range tokenDensityCache {
		if key.channelID == channelID && !retained[key.model] {
			delete(tokenDensityCache, key)
		}
	}
}

// TokenDensityView 密度展示行。
type TokenDensityView struct {
	ChannelID   int       `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	Model       string    `json:"model"`
	Density     float64   `json:"density"`
	SampleCount int64     `json:"sample_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TokenDensityGet 读取密度。
func TokenDensityGet(ctx context.Context, channelID int, modelName string) (model.TokenDensity, bool, error) {
	if db.GetDB() == nil {
		return model.TokenDensity{}, false, nil
	}
	var row model.TokenDensity
	err := db.GetDB().WithContext(ctx).
		Where("channel_id = ? AND model = ?", channelID, modelName).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.TokenDensity{}, false, nil
	}
	if err != nil {
		return model.TokenDensity{}, false, err
	}
	return row, true, nil
}

// TokenDensitySave 写入密度。
func TokenDensitySave(ctx context.Context, channelID int, modelName string, density float64, sampleCount int64, dailySamples int, sampleDay string) error {
	tokenDensityMu.Lock()
	defer tokenDensityMu.Unlock()
	if err := tokenDensitySave(ctx, channelID, modelName, density, sampleCount, dailySamples, sampleDay); err != nil {
		return err
	}
	delete(tokenDensityCache, tokenDensityKey{channelID: channelID, model: modelName})
	return nil
}

func tokenDensitySave(ctx context.Context, channelID int, modelName string, density float64, sampleCount int64, dailySamples int, sampleDay string) error {
	if db.GetDB() == nil {
		return nil
	}
	row := model.TokenDensity{
		ChannelID:    channelID,
		Model:        modelName,
		Density:      density,
		SampleCount:  sampleCount,
		DailySamples: dailySamples,
		SampleDay:    sampleDay,
		UpdatedAt:    time.Now(),
	}
	return db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"density":       density,
			"sample_count":  sampleCount,
			"daily_samples": dailySamples,
			"sample_day":    sampleDay,
			"updated_at":    row.UpdatedAt,
		}),
	}).Create(&row).Error
}

// TokenDensityList 列出密度。
func TokenDensityList(ctx context.Context) ([]TokenDensityView, error) {
	if db.GetDB() == nil {
		return []TokenDensityView{}, nil
	}
	rows := make([]TokenDensityView, 0)
	err := db.GetDB().WithContext(ctx).
		Table("token_densities").
		Select("token_densities.channel_id, channels.name AS channel_name, token_densities.model, token_densities.density, token_densities.sample_count, token_densities.updated_at").
		Joins("LEFT JOIN channels ON channels.id = token_densities.channel_id").
		Order("token_densities.channel_id, token_densities.model").
		Scan(&rows).Error
	return rows, err
}

// TokenDensityDelete 删除密度。
func TokenDensityDelete(ctx context.Context, channelID int, modelName string) error {
	if db.GetDB() == nil {
		return nil
	}
	tokenDensityMu.Lock()
	defer tokenDensityMu.Unlock()
	if err := db.GetDB().WithContext(ctx).
		Where("channel_id = ? AND model = ?", channelID, modelName).
		Delete(&model.TokenDensity{}).Error; err != nil {
		return err
	}
	delete(tokenDensityCache, tokenDensityKey{channelID: channelID, model: modelName})
	return nil
}

// TokenDensityClear 清空密度。
func TokenDensityClear(ctx context.Context) error {
	if db.GetDB() == nil {
		return nil
	}
	tokenDensityMu.Lock()
	defer tokenDensityMu.Unlock()
	if err := db.GetDB().WithContext(ctx).
		Where("1 = 1").
		Delete(&model.TokenDensity{}).Error; err != nil {
		return err
	}
	clear(tokenDensityCache)
	return nil
}
