package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"gorm.io/gorm"
)

func init() {
	router.NewGroupRouter("/api/v1/model-eval").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/run", http.MethodPost).Handle(runModelEval)).
		AddRoute(router.NewRoute("/list", http.MethodGet).Handle(listModelEvals)).
		AddRoute(router.NewRoute("/stats", http.MethodGet).Handle(listModelEvalStats)).
		AddRoute(router.NewRoute("/history/clear-failures", http.MethodPost).Handle(clearModelEvalFailures)).
		AddRoute(router.NewRoute("/probe-context", http.MethodPost).Handle(probeModelContext)).
		AddRoute(router.NewRoute("/probe-history", http.MethodGet).Handle(listContextProbeRecords)).
		AddRoute(router.NewRoute("/apply-context", http.MethodPost).Handle(applyModelContext)).
		AddRoute(router.NewRoute("/:id", http.MethodGet).Handle(getModelEval))
}

// contextProbeRequest 是一次上下文容量探测请求。
type contextProbeRequest struct {
	ChannelID int    `json:"channel_id" binding:"required"`
	Model     string `json:"model" binding:"required"`
	Tokens    int    `json:"tokens" binding:"required"`
}

// probeModelContext 按指定规模探测渠道模型能否接受该长度的请求。
func probeModelContext(c *gin.Context) {
	var request contextProbeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	record := &model.ContextProbeRecord{
		ChannelID: request.ChannelID,
		ModelName: request.Model,
		Tokens:    request.Tokens,
		CreatedAt: time.Now(),
	}
	channel, channelErr := op.ChannelGet(request.ChannelID)
	if channelErr == nil {
		record.ChannelName = channel.Name
	}
	result, err := relay.ProbeContextWindow(c.Request.Context(), request.ChannelID, request.Model, request.Tokens)
	if err != nil {
		detail := redactChannelCredentials(channel, err.Error())
		record.Detail = detail
		_ = op.SaveContextProbeRecord(record)
		resp.ErrorExposed(c, http.StatusBadRequest, detail)
		return
	}
	record.Accepted = result.Accepted
	record.Status = result.Status
	record.Detail = redactChannelCredentials(channel, result.Detail)
	record.LatencyMS = result.LatencyMS
	record.ActualTokens = result.ActualTokens
	_ = op.SaveContextProbeRecord(record)
	result.Detail = record.Detail
	resp.Success(c, result)
}

// redactChannelCredentials 抹掉文本中的渠道密钥明文。
func redactChannelCredentials(channel model.Channel, text string) string {
	if channel.Key != "" {
		text = strings.ReplaceAll(text, channel.Key, "[REDACTED]")
	}
	for _, key := range channel.Keys {
		if key.Key != "" {
			text = strings.ReplaceAll(text, key.Key, "[REDACTED]")
		}
	}
	return text
}

// listContextProbeRecords 返回探测历史, 供页面回看历次结果。
func listContextProbeRecords(c *gin.Context) {
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := op.ListContextProbeRecords(channelID, limit)
	if err != nil {
		resp.ErrorExposed(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, rows)
}

// contextApplyRequest 把手动定义或探测结果写回按模型限制。
type contextApplyRequest struct {
	ChannelID     int    `json:"channel_id" binding:"required"`
	Model         string `json:"model" binding:"required"`
	ContextWindow *int   `json:"context_window" binding:"required"` // 0 表示清除人工窗口。
}

// applyContextMu 串行化窗口保存, 防整表覆盖丢更新。
var applyContextMu sync.Mutex

// applyModelContext 把手动定义或探测出的窗口写入渠道的 model_limits, 同步不会覆盖;
// context_window 传 0 表示清除该模型的人工窗口。
func applyModelContext(c *gin.Context) {
	var request contextApplyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if *request.ContextWindow < 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	applyContextMu.Lock()
	defer applyContextMu.Unlock()
	channel, err := op.ChannelGet(request.ChannelID)
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	limits := setModelContextWindow(channel.ModelLimits, request.Model, *request.ContextWindow)
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{
		ID:          request.ChannelID,
		ModelLimits: &limits,
	}, c.Request.Context()); err != nil {
		resp.ErrorExposed(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

// setModelContextWindow 在按模型限制表中写入或清除人工窗口; 无其他配置时删除空条目。
func setModelContextWindow(limits map[string]model.ChannelModelLimit, modelName string, window int) map[string]model.ChannelModelLimit {
	next := make(map[string]model.ChannelModelLimit, len(limits)+1)
	for name, limit := range limits {
		next[name] = limit
	}
	keys := make([]string, 0, 1)
	for name := range next {
		if strings.EqualFold(name, modelName) {
			keys = append(keys, name)
		}
	}
	if len(keys) == 0 {
		keys = append(keys, modelName)
	}
	for _, key := range keys {
		limit := next[key]
		if window > 0 {
			value := window
			limit.ContextWindow = &value
		} else {
			limit.ContextWindow = nil
		}
		if limit.ContextWindow == nil && limit.MaxOutput == nil && limit.ThinkingLevel == "" {
			delete(next, key)
		} else {
			next[key] = limit
		}
	}
	return next
}

// runModelEval 复用渠道测试的真实出站逻辑，仅模型评估入口保存评估历史。
func runModelEval(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ChannelModelID int `json:"channel_model_id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channelModel, err := op.ChannelModelGet(request.ChannelModelID)
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	channel, err := op.ChannelGetCore(channelModel.ChannelID)
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	if !channel.Enabled {
		resp.Error(c, http.StatusBadRequest, "请先启用渠道再进行评估")
		return
	}
	started := time.Now()
	record := model.ModelEval{
		ModelEvalSummary: model.ModelEvalSummary{
			ChannelID:      channel.ID,
			ChannelModelID: channelModel.ID,
			ChannelName:    channel.Name,
			ChannelType:    channel.Type,
			ModelName:      channelModel.Name,
			CreatedAt:      started,
		},
		Prompt: model.ModelEvalPrompt,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	result, testErr := relay.TestChannelKeyFailover(ctx, channel.ID, channelModel.Name, record.Prompt)
	record.CompletedAt = time.Now()
	record.LatencyMS = time.Since(started).Milliseconds()
	if testErr != nil {
		record.Outcome = model.ModelEvalError
		switch {
		case errors.Is(testErr, context.DeadlineExceeded):
			record.Error = "评估超时（最长等待 10 分钟）"
		case errors.Is(testErr, context.Canceled):
			record.Error = "评估请求已取消"
		default:
			record.Error = testErr.Error()
		}
		// 上游错误可能回显请求凭据，先替换已知 Key，再由 op 层统一脱敏。
		record.Error = redactChannelCredentials(channel, record.Error)
	} else {
		record.Content = result.Content
		record.Outcome = model.ModelEvalContentOutcome(result.Content)
		record.LatencyMS = result.ElapsedMS
		record.PromptTokens = result.PromptTokens
		record.CompletionTokens = result.CompletionTokens
	}
	// 客户端离开页面/超时后仍保存已结束的评估；出站请求本身继续遵循取消信号。
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer saveCancel()
	if err := op.ModelEvalCreate(saveCtx, &record); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 上游失败也是一条已保存的评估结果，以 outcome 区分；HTTP 错误用于接口/存储失败。
	resp.Success(c, record)
}

func listModelEvals(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ChannelID int    `form:"channel_id" binding:"omitempty,min=1"`
		ModelName string `form:"model_name" binding:"max=512"`
		Outcome   string `form:"outcome" binding:"omitempty,oneof=ok violation error"`
		Query     string `form:"query" binding:"max=200"`
		Page      int    `form:"page" binding:"omitempty,min=1,max=1000000"`
		PageSize  int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	}
	if err := c.ShouldBindQuery(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	page, err := op.ModelEvalList(c.Request.Context(), op.ModelEvalFilter{
		ChannelID: request.ChannelID,
		ModelName: request.ModelName,
		Outcome:   request.Outcome,
		Query:     request.Query,
		Page:      request.Page,
		PageSize:  request.PageSize,
	})
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, page)
}

func listModelEvalStats(c *gin.Context) {
	resp.NoStore(c)
	stats, err := op.ModelEvalStatsList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"items": stats})
}

func getModelEval(c *gin.Context) {
	resp.NoStore(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	record, err := op.ModelEvalGet(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		resp.Error(c, http.StatusNotFound, "评估记录不存在")
		return
	}
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, record)
}

func clearModelEvalFailures(c *gin.Context) {
	resp.NoStore(c)
	removed, err := op.ModelEvalClearFailures(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"removed": removed})
}
