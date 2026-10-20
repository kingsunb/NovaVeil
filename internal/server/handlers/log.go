package handlers

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

// defaultFailureLimit 失败摘要查询接口未指定 limit 时的默认返回条数。
const defaultFailureLimit = 50

func init() {
	router.NewGroupRouter("/api/v1/log").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/overview/stream", http.MethodGet).
				Handle(streamOverview),
		).
		AddRoute(
			router.NewRoute("/failures", http.MethodGet).
				Handle(listFailures),
		).
		AddRoute(
			router.NewRoute("/:id/request-body", http.MethodGet).
				Handle(getRequestBody),
		).
		AddRoute(
			router.NewRoute("/:id/response-body", http.MethodGet).
				Handle(getResponseBody),
		).
		AddRoute(
			router.NewRoute("/:request_id/:round/stop", http.MethodPost).
				Handle(interruptRound),
		).
		AddRoute(
			router.NewRoute("/:request_id/stop-request", http.MethodPost).
				Handle(stopRequest),
		).
		AddRoute(
			router.NewRoute("/client-stats", http.MethodGet).
				Handle(listClientStats),
		).
		AddRoute(
			router.NewRoute("/clear", http.MethodDelete).
				Handle(clearLog),
		).
		AddRoute(
			router.NewRoute("/stop-all", http.MethodPost).
				Handle(stopAllRequests),
		).
		AddRoute(
			router.NewRoute("/resume-all", http.MethodPost).
				Handle(resumeAllRequests),
		).
		AddRoute(
			router.NewRoute("/stop-all-state", http.MethodGet).
				Handle(getStopAllState),
		).
		AddRoute(
			router.NewRoute("/errors", http.MethodGet).
				Handle(listErrorLogs),
		).
		AddRoute(
			router.NewRoute("/errors", http.MethodDelete).
				Handle(clearErrorLogs),
		)
}

// stopRequest 整体终止一个进行中的请求: 转发循环在最近安全点退出, 不再发起新尝试。
func stopRequest(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("request_id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if !relay.StopRequestByID(id) {
		resp.Error(c, http.StatusNotFound, "请求不存在")
		return
	}
	resp.Success(c, "请求已停止")
}

// interruptRound 中止请求当前轮次匹配的上游调用。
func interruptRound(c *gin.Context) {
	requestID, err := strconv.ParseUint(c.Param("request_id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "请求 ID 无效")
		return
	}
	round, err := strconv.Atoi(c.Param("round"))
	if err != nil || round < 1 {
		resp.Error(c, http.StatusBadRequest, "轮次无效")
		return
	}
	if !relay.Interrupt(requestID, round) {
		// 未命中意味着目标轮次已结束或该请求已被清理, 不能再中止;
		// 仍以 204 兼容既有 toast, 但响应体带字段说明, 前端可以据此调整提示。
		resp.Success(c, gin.H{
			"interrupted": false,
			"reason":      "target round already finished or request no longer active",
		})
		return
	}
	resp.Success(c, gin.H{"interrupted": true})
}

// clearLog 删除全部已完成的请求记录，并在释放记录引用后主动执行垃圾回收。
func clearLog(c *gin.Context) {
	relay.Clear()
	runtime.GC()
	c.Status(http.StatusNoContent)
}

// stopAllRequests 实现「日志页一键停止所有请求」: 立即置位全局停止标志并终止全部在途请求;
// 后续新请求以 503 拒绝, 直到显式调用 /resume-all 清标志。
// 返回被终止的在途请求数与当前停止状态, 供前端即时反馈。
func stopAllRequests(c *gin.Context) {
	stopped := relay.StopAllRequests()
	resp.Success(c, gin.H{
		"stopped":    stopped,
		"is_stopped": true,
	})
}

// resumeAllRequests 解除全局停止状态, 恢复新请求接收; 已被 503 拒绝的请求需客户端重试。
func resumeAllRequests(c *gin.Context) {
	relay.ClearStopAll()
	resp.Success(c, gin.H{
		"is_stopped": false,
	})
}

// getStopAllState 返回当前全局停止状态; 不修改任何状态, 供前端按钮在挂载/重连时立即校准视觉。
func getStopAllState(c *gin.Context) {
	resp.Success(c, gin.H{
		"is_stopped": relay.IsAllStopped(),
	})
}

// getRequestBody 返回指定请求的原始请求体。
func getRequestBody(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "请求 ID 无效")
		return
	}
	resp.Success(c, relay.RequestBody(id))
}

// getResponseBody 返回指定请求当前保存的响应体。
func getResponseBody(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "请求 ID 无效")
		return
	}
	resp.Success(c, relay.ResponseBody(id))
}

// listFailures 返回最近失败请求的摘要列表, 支持 limit(默认 50, 上限为缓冲容量)与 class 过滤。
func listFailures(c *gin.Context) {
	limit := defaultFailureLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			resp.Error(c, http.StatusBadRequest, "limit 无效")
			return
		}
		limit = parsed
	}
	resp.Success(c, relay.FailureSummaries(limit, relay.ErrClass(c.Query("class"))))
}

// listErrorLogs 从数据库分页读取持久化的错误日志, 支持 limit(默认 50)与 class 过滤。
func listErrorLogs(c *gin.Context) {
	limit := op.ErrorLogListLimitDefault
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			resp.Error(c, http.StatusBadRequest, "limit 无效")
			return
		}
		limit = parsed
	}
	logs, err := op.ErrorLogList(c.Request.Context(), limit, c.Query("class"))
	if err != nil {
		log.Errorf("failed to list error logs: %v", err)
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	resp.Success(c, logs)
}

// clearErrorLogs 清空全部持久化的错误日志记录。
func clearErrorLogs(c *gin.Context) {
	if err := op.ErrorLogDeleteAll(c.Request.Context()); err != nil {
		log.Errorf("failed to clear error logs: %v", err)
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	c.Status(http.StatusNoContent)
}

// streamOverview 逐条发送建立连接时的概览及后续请求更新。
func streamOverview(c *gin.Context) {
	prepareSSE(c)
	snapshot, updates := relay.OpenRequestStream()
	defer relay.CloseRequestStream(updates)
	for _, request := range snapshot {
		if err := sse.Encode(c.Writer, sse.Event{Event: "log", Data: request}); err != nil {
			return
		}
		c.Writer.Flush()
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := c.Writer.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			c.Writer.Flush()
		case request, ok := <-updates:
			if !ok {
				return
			}
			if err := sse.Encode(c.Writer, sse.Event{Event: "log", Data: request}); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

// prepareSSE 设置实时日志连接需要的响应头。
func prepareSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	// no-store: 禁止任何缓存存储 SSE 响应; no-transform: 禁止中间代理(Cloudflare 等)
	// 对响应体做压缩/转换 — CF 的 zstd/gzip 压缩会缓冲数据块, 破坏 SSE 逐条实时推送。
	c.Header("Cache-Control", "no-store, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

// listClientStats 返回按 IP 维度的调用统计(去重 IP 数、请求次数、首次/最后时间)。
func listClientStats(c *gin.Context) {
	resp.Success(c, op.ClientStatList())
}
