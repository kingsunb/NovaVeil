package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/eval"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/model-eval").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/queue/enqueue", http.MethodPost).Handle(enqueueModelEvalQueue)).
		AddRoute(router.NewRoute("/queue/list", http.MethodGet).Handle(listModelEvalQueue)).
		AddRoute(router.NewRoute("/queue/move-up", http.MethodPost).Handle(moveUpModelEvalQueue)).
		AddRoute(router.NewRoute("/queue/stop", http.MethodPost).Handle(stopModelEvalQueue)).
		AddRoute(router.NewRoute("/queue/clear", http.MethodPost).Handle(clearModelEvalQueue)).
		AddRoute(router.NewRoute("/queue/stream", http.MethodGet).Use(middleware.AuthStream()).Handle(streamModelEvalQueue))
}

func enqueueModelEvalQueue(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ChannelModelIDs []int `json:"channel_model_ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if len(request.ChannelModelIDs) == 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	tasks, err := op.ModelEvalQueueEnqueue(c.Request.Context(), request.ChannelModelIDs)
	if err != nil {
		writeEvalQueueOpError(c, err)
		return
	}
	eval.Notify()
	resp.Success(c, gin.H{"enqueued": tasks})
}

func listModelEvalQueue(c *gin.Context) {
	resp.NoStore(c)
	items, err := op.ModelEvalQueueList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"items": items})
}

func moveUpModelEvalQueue(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ID int64 `json:"id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	items, err := op.ModelEvalQueueMoveUp(c.Request.Context(), request.ID)
	if err != nil {
		writeEvalQueueOpError(c, err)
		return
	}
	eval.Notify()
	resp.Success(c, gin.H{"items": items})
}

func stopModelEvalQueue(c *gin.Context) {
	resp.NoStore(c)
	var request struct {
		ID int64 `json:"id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	items, err := op.ModelEvalQueueStop(c.Request.Context(), request.ID)
	if err != nil {
		writeEvalQueueOpError(c, err)
		return
	}
	eval.Notify()
	resp.Success(c, gin.H{"items": items})
}

func clearModelEvalQueue(c *gin.Context) {
	resp.NoStore(c)
	removed, err := op.ModelEvalQueueClear(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	eval.Notify()
	resp.Success(c, gin.H{"removed": removed})
}

func streamModelEvalQueue(c *gin.Context) {
	prepareSSE(c)
	snapshot, updates := eval.Subscribe()
	defer eval.Unsubscribe(updates)
	if c.Request.Context().Err() != nil {
		return
	}
	if err := sse.Encode(c.Writer, sse.Event{Event: "queue", Data: snapshot}); err != nil {
		return
	}
	c.Writer.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			if c.Request.Context().Err() != nil {
				return
			}
			if _, err := c.Writer.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			c.Writer.Flush()
		case tasks, ok := <-updates:
			if !ok || c.Request.Context().Err() != nil {
				return
			}
			if err := sse.Encode(c.Writer, sse.Event{Event: "queue", Data: tasks}); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeEvalQueueOpError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, op.ErrEvalQueueTaskNotFound):
		resp.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, op.ErrEvalQueueTaskConflict):
		resp.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, op.ErrEvalQueueMoveBounds),
		errors.Is(err, op.ErrEvalQueueModelUnavailable):
		resp.Error(c, http.StatusBadRequest, err.Error())
	default:
		resp.Error(c, http.StatusInternalServerError, err.Error())
	}
}
