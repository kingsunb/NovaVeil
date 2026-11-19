package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
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
		AddRoute(router.NewRoute("/:id", http.MethodGet).Handle(getModelEval))
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
