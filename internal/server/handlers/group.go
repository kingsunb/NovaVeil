package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

// writeGroupOpError 把 op 层分组操作错误映射到合适的 HTTP 状态码后写回,
// 让 4xx 与 5xx 分离, 前端 toast / i18n 能区分「输入错误」与「服务异常」。
func writeGroupOpError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, op.ErrGroupNotFound):
		resp.Error(c, http.StatusNotFound, "分组不存在")
	case errors.Is(err, op.ErrGroupRefTargetMissing):
		// 引用成员指向不存在的目标分组, 属「目标资源缺失」, 400 即可, i18n 友好。
		resp.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, op.ErrGroupReferencedBy):
		resp.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, op.ErrGroupNameRequired),
		errors.Is(err, op.ErrGroupItemMissingTarget),
		errors.Is(err, op.ErrGroupDuplicateRef),
		errors.Is(err, op.ErrGroupDuplicateMember),
		errors.Is(err, op.ErrGroupSelfReference),
		errors.Is(err, op.ErrGroupRefDepthExceeded),
		errors.Is(err, op.ErrGroupRefCycle):
		resp.Error(c, http.StatusBadRequest, err.Error())
	default:
		resp.Error(c, http.StatusInternalServerError, err.Error())
	}
}

func init() {
	router.NewGroupRouter("/api/v1/group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getGroupList),
		).
		AddRoute(
			router.NewRoute("/runtime/stream", http.MethodGet).
				Handle(streamGroupRuntime),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createGroup),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateGroup),
		).
		AddRoute(
			router.NewRoute("/active/:id", http.MethodPost).
				Handle(updateGroupActiveItem),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteGroup),
		).
		AddRoute(
			router.NewRoute("/cooldown/clear/:id", http.MethodPost).
				Handle(clearGroupCooldown),
		).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Handle(testGroup),
		)
}

// streamGroupRuntime 向前端发送分组实时运行状态。
func streamGroupRuntime(c *gin.Context) {
	prepareSSE(c)
	snapshot, updates := relay.OpenRouteStream()
	defer relay.CloseRouteStream(updates)
	for _, update := range snapshot {
		if err := sse.Encode(c.Writer, sse.Event{Event: "runtime", Data: update}); err != nil {
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
		case update, ok := <-updates:
			if !ok {
				return
			}
			if err := sse.Encode(c.Writer, sse.Event{Event: "runtime", Data: update}); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func getGroupList(c *gin.Context) {
	resp.Success(c, op.GroupList())
}

func createGroup(c *gin.Context) {
	var group model.Group
	if err := c.ShouldBindJSON(&group); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.GroupCreate(&group, c.Request.Context()); err != nil {
		writeGroupOpError(c, err)
		return
	}
	resp.Success(c, group)
}

func updateGroup(c *gin.Context) {
	var req model.GroupUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	group, err := op.GroupUpdate(&req, c.Request.Context())
	if err != nil {
		writeGroupOpError(c, err)
		return
	}
	resp.Success(c, group)
}

// updateGroupActiveItem 更新分组当前手动指定的渠道模型成员。
func updateGroupActiveItem(c *gin.Context) {
	groupID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var req model.GroupActiveItemUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	group, err := op.GroupActiveItemUpdate(groupID, &req, c.Request.Context())
	if err != nil {
		writeGroupOpError(c, err)
		return
	}
	resp.Success(c, group)
}

func deleteGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.GroupDel(id, c.Request.Context()); err != nil {
		writeGroupOpError(c, err)
		return
	}
	// 回收已删除分组的进程内路由状态与会话粘合, 防止跨建删的条目永久滞留。
	relay.RemoveGroupRoute(id)
	resp.Success(c, "分组已删除")
}

// clearGroupCooldown 清理指定分组下全部成员级冷却、Key 冷却与限速窗口, 含引用成员间接命中的渠道;
// 返回清理结果供前端展示统计信息。op 与 relay 之间存在包级相互 import 风险, 故拆为多步:
// 1) op.GroupClearKeyCooldown 解析分组引用链, 收集关联渠道 ID 列表(不直接操作 relay 内部状态);
// 2) handler 逐个渠道调 relay.ClearChannelKeyStateForUI 清 Key 冷却表与限速窗口;
// 3) handler 调 relay.ResetGroupCooldown 清本分组 RouteState 的 Cooldowns/Levels/PostCommitStrikes/emergencyBlocks 并 SSE 推送;
// 步骤 2/3 互不依赖, 一方失败仅影响对应部分的统计计数, 整体不阻断。
func clearGroupCooldown(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	result, channelIDs, err := op.GroupClearKeyCooldown(id)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	for _, channelID := range channelIDs {
		keyCooldowns, rateWindows := relay.ClearChannelKeyStateForUI(channelID)
		result.KeyCooldowns += keyCooldowns
		result.RateWindows += rateWindows
	}
	memberItems, ok := relay.ResetGroupCooldown(id)
	if !ok {
		log.Warnf("reset group cooldown may be incomplete, group=%d", id)
	}
	result.MemberItems = memberItems
	resp.Success(c, result)
}

// testGroup 按分组测试: 对分组内每个成员(渠道+模型)按真实路由逻辑发起一条测试消息,
// 逐成员返回连通性与回复内容。整体预算 30 分钟, 足以覆盖大分组的串行最坏情况。
func testGroup(c *gin.Context) {
	var request struct {
		ID      int    `json:"id" binding:"required"` // 待测试的分组主键。
		Message string `json:"message"`               // 测试消息内容, 为空时由后端使用默认值。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	results, err := relay.TestGroup(ctx, request.ID, request.Message)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, results)
}
