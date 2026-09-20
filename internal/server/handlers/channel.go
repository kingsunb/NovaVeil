package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"github.com/kingsunb/NovaVeil/internal/task"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listChannel),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateChannel),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableChannel),
		).
		AddRoute(
			router.NewRoute("/export", http.MethodPost).
				Handle(exportChannel),
		).
		AddRoute(
			router.NewRoute("/import", http.MethodPost).
				Handle(importChannel),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannel),
		).
		AddRoute(
			router.NewRoute("/fetch-model", http.MethodPost).
				Handle(fetchModel),
		).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Handle(testChannel),
		).
		AddRoute(
			router.NewRoute("/test_keys", http.MethodPost).
				Handle(testChannelKeys),
		)
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/sync", http.MethodPost).
				Handle(syncChannel),
		).
		AddRoute(
			router.NewRoute("/keys/:id", http.MethodPost).
				Handle(getChannelKeys),
		).
		AddRoute(
			router.NewRoute("/last-sync-time", http.MethodGet).
				Handle(getLastSyncTime),
		)
}

func channelKeyMasked(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

// channelAdminSummary 返回可编辑但不含明文凭据的管理快照。
// Key ID/Account/Remark 保留，前端提交 id+空 key 表示保留原 secret。
func channelAdminSummary(channel model.Channel) model.Channel {
	summary := channel
	summary.KeyMasked = channelKeyMasked(channel.Key)
	summary.Key = ""
	if channel.Keys != nil {
		summary.Keys = make([]model.ChannelKey, len(channel.Keys))
		for i, key := range channel.Keys {
			key.KeyMasked = channelKeyMasked(key.Key)
			key.Key = ""
			summary.Keys[i] = key
		}
	}
	return summary
}

func listChannel(c *gin.Context) {
	channels := op.ChannelList()
	for i := range channels {
		channels[i] = channelAdminSummary(channels[i])
	}
	resp.Success(c, channels)
}

func createChannel(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelCreate(&channel, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, channelAdminSummary(channel))
}

func updateChannel(c *gin.Context) {
	var req model.ChannelUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, channelAdminSummary(*channel))
}

func enableChannel(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.ChannelDel(id, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 多 Key 渠道删除后同步清理轮询游标与 Key 级冷却记录, 防止条目累积或同 ID 重建被残留冷却误伤。
	relay.CleanupChannelKeyState(id)
	resp.Success(c, nil)
}

func fetchModel(c *gin.Context) {
	// 兼容两种客户端形态:
	//  1. 仅传渠道 ID(新控制台): 全部连接配置以 DB 缓存为准, 避免前端伪造 base_url/type。
	//  2. 传完整渠道表单(含未保存的新渠道): 直接用提交的表单值拉取; 编辑已有渠道
	//     而未重填密钥时, 回退已存渠道的凭据, 避免"不改密钥就必须先点眼睛拿明文"的死锁。
	var request model.Channel
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if request.ID != 0 && request.BaseURL == "" {
		// 仅 {id}: 整体回退已存渠道配置(含密钥), 保持与 DB 的一致性。
		stored, err := op.ChannelGetCore(request.ID)
		if err != nil {
			resp.Error(c, http.StatusNotFound, err.Error())
			return
		}
		// 回退已存渠道同样执行完整出口校验, 防止较早版本写入的私网地址经 fetch-model 回放(审计 SEC-01)。
		if err := op.ValidateChannelEgressBaseURL(stored.BaseURL); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		models, err := helper.FetchModels(c.Request.Context(), stored)
		if err != nil {
			resp.Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		resp.Success(c, models)
		return
	}
	// 未保存/带 BaseURL 的表单: 提交值尚未经过 ChannelCreate 校验, 必须在发起探测前
	// 用完整出口校验拦截私网/环回/metadata 地址(审计 SEC-01)。
	if err := op.ValidateChannelEgressBaseURL(request.BaseURL); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	request.Key = request.PrimaryKey() // 前端只提交 keys 数组时回退取第一把, 兼容旧客户端直接传 key 字段。
	if request.Key == "" && request.ID != 0 {
		// 编辑已有渠道时管理端默认不回传密钥明文, 允许仅带渠道 ID 拉取模型:
		// 回退使用已存渠道的凭据, 避免"不改密钥就必须先点眼睛拿明文"的死锁。
		stored, err := op.ChannelGetCore(request.ID)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		request.Key = stored.PrimaryKey()
	}
	models, err := helper.FetchModels(c.Request.Context(), request)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, models)
}

// testChannel 以一条测试消息验证渠道上的单个模型是否可用; 可选 key_id 指定用哪把已保存密钥测试,
// 留空时使用第一把健康密钥。
func testChannel(c *gin.Context) {
	// web-next 的「测试连通」按钮只传 {id}；model 字段可选，缺省时从渠道上
	// 已配置的模型中取第一个，db 里没模型时返回明确错误（让前端引导用户先拉
	// 取或手动添加）。
	var request struct {
		ID      int    `json:"id" binding:"required"` // 待测试的渠道主键。
		Model   string `json:"model"`                 // 可选；缺省自动选第一个。
		Message string `json:"message"`               // 测试消息内容, 为空时由后端使用默认值。
		KeyID   string `json:"key_id"`                // 可选的指定密钥 ID, 未找到时以中文错误返回。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if request.Model == "" {
		channel, err := op.ChannelGet(request.ID)
		if err != nil {
			resp.Error(c, http.StatusNotFound, err.Error())
			return
		}
		if len(channel.Models) == 0 {
			resp.Error(c, http.StatusBadRequest, "渠道无可用模型；请先添加或拉取模型")
			return
		}
		request.Model = channel.Models[0].Name
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 300*time.Second)
	defer cancel()
	result, err := relay.TestChannel(ctx, request.ID, request.Model, request.Message, request.KeyID)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, result)
}

// testChannelKeys 对渠道配置的每一把密钥各发送一条测试消息, 逐 Key 返回有效性结果。
// 单 Key 上游超时 60s、并发池 4, 整体 30 分钟预算足够 120+ 把密钥的最坏情况(ceil(N/4)*60s)。
func testChannelKeys(c *gin.Context) {
	var request struct {
		ID      int    `json:"id" binding:"required"` // 待测试的渠道主键。
		Model   string `json:"model"`                 // 逐密钥测试使用的模型名称。
		Message string `json:"message"`               // 测试消息内容, 为空时由后端使用默认值。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	results, err := relay.TestChannelKeys(ctx, request.ID, request.Model, request.Message)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, results)
}

func syncChannel(c *gin.Context) {
	if err := task.SyncModelsTask(); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	resp.Success(c, task.GetLastSyncModelsTime())
}

// channelKeySecretView 是密钥明文查看接口的单条返回。
type channelKeySecretView struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Remark string `json:"remark,omitempty"`
}

// channelKeySecrets 把渠道密钥映射为明文查看视图: 管理列表出于安全只回掩码,
// 本视图供管理端"显示密钥"按钮按需获取明文。旧式单 Key 渠道无稳定 ID, 以空 id 唯一行返回。
func channelKeySecrets(channel model.Channel) []channelKeySecretView {
	views := make([]channelKeySecretView, 0, len(channel.Keys))
	for _, k := range channel.Keys {
		if k.Key == "" {
			continue
		}
		views = append(views, channelKeySecretView{ID: k.ID, Key: k.Key, Remark: k.Remark})
	}
	if len(views) == 0 && channel.Key != "" {
		views = append(views, channelKeySecretView{Key: channel.Key})
	}
	return views
}

// getChannelKeys 返回指定渠道的全部密钥明文, 与导出接口同级, 仅限管理员会话访问。
func getChannelKeys(c *gin.Context) {
	resp.NoStore(c)
	log.Warnf("channel key reveal id=%s ip=%s", c.Param("id"), c.ClientIP())
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	channel, err := op.ChannelGet(id)
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	resp.Success(c, channelKeySecrets(channel))
}

// channelImportResult 渠道导入结果: 成功/失败计数与逐条失败原因。
type channelImportResult struct {
	Success int      `json:"success"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors"`
}

// importChannel 解析导出格式文本并批量创建渠道, 逐条记录失败原因。
// 单条渠道失败(如名称已存在的唯一约束冲突)不中断整批导入。
func importChannel(c *gin.Context) {
	var request struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	success, failed, errors := op.ChannelImportFromText(c.Request.Context(), request.Text)
	resp.Success(c, channelImportResult{Success: success, Failed: failed, Errors: errors})
}

// exportChannel 以纯文本导出全部渠道的地址与全部密钥掩码:
// 每个渠道一段(首行 # 渠道名, 其次地址, 之后每行一把密钥掩码), 渠道间空行分隔。
// 明文上游密钥不再随导出返回(审计 SEC-03): 掩码形如 **** 末四位, 用于跨实例迁移时
// 核对渠道数量与密钥尾号, 新实例上由管理员重新录入明文。内容仅限管理员会话访问。
func exportChannel(c *gin.Context) {
	resp.NoStore(c)
	log.Warnf("channel export ip=%s", c.ClientIP())
	channels := op.ChannelList()
	var b strings.Builder
	for _, ch := range channels {
		keys := make([]string, 0, len(ch.Keys)+1)
		for _, k := range ch.Keys {
			if k.Key != "" {
				keys = append(keys, channelKeyMasked(k.Key))
			}
		}
		if len(keys) == 0 && ch.Key != "" {
			keys = append(keys, channelKeyMasked(ch.Key))
		}
		b.WriteString("# " + ch.Name + "\n")
		b.WriteString(ch.BaseURL + "\n")
		for _, key := range keys {
			b.WriteString(key + "\n")
		}
		b.WriteString("\n")
	}
	filename := "channels-" + time.Now().Format("20060102-150405") + ".txt"
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(b.String()))
}
