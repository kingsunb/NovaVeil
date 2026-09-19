package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"gorm.io/gorm"
)

func init() {
	router.NewGroupRouter("/api/v1/model-eval").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/rank/list", http.MethodGet).Handle(listModelEvalRanks)).
		AddRoute(router.NewRoute("/rank/content/:id", http.MethodGet).Handle(getModelEvalRankContent)).
		AddRoute(router.NewRoute("/rank/move", http.MethodPost).Handle(moveModelEvalRank)).
		AddRoute(router.NewRoute("/rank/remove", http.MethodPost).Handle(removeModelEvalRank)).
		AddRoute(router.NewRoute("/rank/from-history", http.MethodPost).Handle(fromHistoryModelEvalRank)).
		AddRoute(router.NewRoute("/rank/apply-pro", http.MethodPost).Handle(applyProFromEvalRank))
}

func listModelEvalRanks(c *gin.Context) {
	resp.NoStore(c)
	items, err := op.ModelEvalRankList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"items": items})
}

func getModelEvalRankContent(c *gin.Context) {
	resp.NoStore(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	record, err := op.ModelEvalRankContent(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		resp.Error(c, http.StatusNotFound, "排序条目不存在")
		return
	}
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{
		"content":           record.Content,
		"content_truncated": record.ContentTruncated,
	})
}

func moveModelEvalRank(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ID        int64  `json:"id" binding:"required,min=1"`
		Direction *int   `json:"direction" binding:"omitempty,oneof=-1 1"`
		Position  *int64 `json:"position" binding:"omitempty,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if (request.Direction == nil) == (request.Position == nil) {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	var items []model.ModelEvalRankSummary
	var err error
	if request.Position != nil {
		items, err = op.ModelEvalRankSetPosition(c.Request.Context(), request.ID, *request.Position)
	} else {
		items, err = op.ModelEvalRankMove(c.Request.Context(), request.ID, *request.Direction)
	}
	if err != nil {
		writeEvalRankOpError(c, err)
		return
	}
	resp.Success(c, gin.H{"items": items})
}

func removeModelEvalRank(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ID int64 `json:"id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if _, err := op.ModelEvalRankContent(c.Request.Context(), request.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			resp.Error(c, http.StatusNotFound, "排序条目不存在")
			return
		}
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := op.ModelEvalRankRemove(c.Request.Context(), request.ID); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	items, err := op.ModelEvalRankList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"items": items})
}

func fromHistoryModelEvalRank(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		EvalID int64 `json:"eval_id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	items, err := op.ModelEvalRankFromHistory(c.Request.Context(), request.EvalID)
	if err != nil {
		writeEvalRankOpError(c, err)
		return
	}
	resp.Success(c, gin.H{"items": items})
}

func applyProFromEvalRank(c *gin.Context) {
	resp.NoStore(c)
	ranks, err := op.ModelEvalRankList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	group, report, created, err := op.GroupReplaceItemsByName(c.Request.Context(), "auto", ranks)
	if err != nil {
		switch {
		case errors.Is(err, op.ErrGroupReplaceNoRankable):
			resp.Error(c, http.StatusBadRequest, err.Error())
		default:
			resp.Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	resp.Success(c, gin.H{
		"group_id":      group.ID,
		"created":       created,
		"item_count":    len(group.Items),
		"kept_disabled": report.KeptDisabled,
		"cleaned_stale": report.CleanedStale,
	})
}

func writeEvalRankOpError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, op.ErrEvalRankNotFound):
		resp.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, op.ErrEvalRankMoveBounds),
		errors.Is(err, op.ErrEvalRankInvalidPosition),
		errors.Is(err, op.ErrEvalRankMoveErrorOutcome),
		errors.Is(err, op.ErrEvalRankModelUnavailable),
		errors.Is(err, op.ErrEvalRankErrorOutcome):
		resp.Error(c, http.StatusBadRequest, err.Error())
	default:
		resp.Error(c, http.StatusInternalServerError, err.Error())
	}
}
