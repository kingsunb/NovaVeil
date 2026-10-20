package handlers

import (
	"net/http"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/conversation").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/clear", http.MethodDelete).
				Handle(clearConversations),
		).
		AddRoute(
			router.NewRoute("/stats", http.MethodGet).
				Handle(getConversationStats),
		)
}

// clearConversations 立即删除全部对话留存归档(审计日志): 先落盘内存待写队列, 再清空 data/conversations 下所有文件。
// 这是破坏性操作, 仅单管理员可触发; 返回已删除文件数供前端反馈。
func clearConversations(c *gin.Context) {
	removed, err := op.ClearConversationArchives(c.Request.Context())
	if err != nil {
		log.Errorf("failed to clear conversation archives: %v", err)
		resp.Error(c, http.StatusInternalServerError, "清理对话归档失败: "+err.Error())
		return
	}
	resp.Success(c, gin.H{"removed": removed})
}

// getConversationStats 返回当前对话留存目录的占用快照供设置页展示。
// 数据来源含运行期设置项(保留天数/容量上限)、内存待写字节、累计丢弃次数,
// 以及目录扫描得到的文件数/总占用/最旧最新日期。读取不阻断业务流量。
func getConversationStats(c *gin.Context) {
	stats, err := op.GetConversationStats(c.Request.Context())
	if err != nil {
		log.Errorf("failed to read conversation stats: %v", err)
		resp.Error(c, http.StatusInternalServerError, "读取对话统计失败: "+err.Error())
		return
	}
	resp.Success(c, stats)
}
