package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
	"github.com/kingsunb/NovaVeil/internal/task"
)

func init() {
	router.NewGroupRouter("/api/v1/setting").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getSettingList),
		).
		AddRoute(
			router.NewRoute("/get", http.MethodGet).
				Handle(getSetting),
		).
		AddRoute(
			router.NewRoute("/set", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(setSetting),
		).
		AddRoute(
			router.NewRoute("/export", http.MethodPost).
				Handle(exportDB),
		).
		AddRoute(
			router.NewRoute("/import", http.MethodPost).
				Handle(importDB),
		)
}

func getSettingList(c *gin.Context) {
	settings, err := op.SettingList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, settings)
}

// getSetting 按 key 返回单条配置项（web-next RetentionField 等场景需要）。
// 与 list 的差别：单条响应对前端 useQuery 更友好（一个 key 一个 query，
// 不传整个 map）；未配置的 key 返回 404 + 「setting not found」，让前端能区分
// 「未设置」与「已设为 0」。
func getSetting(c *gin.Context) {
	key := strings.TrimSpace(c.Query("key"))
	if key == "" {
		resp.Error(c, http.StatusBadRequest, "missing key")
		return
	}
	value, err := op.SettingGetString(model.SettingKey(key))
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	resp.Success(c, model.Setting{
		Key:   model.SettingKey(key),
		Value: value,
	})
}

// maxSyncLLMIntervalHours 同步间隔上限(一年): 超过会溢出 time.Duration 且无运维意义。
const maxSyncLLMIntervalHours = 8760

func setSetting(c *gin.Context) {
	var setting model.Setting
	if err := c.ShouldBindJSON(&setting); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := setting.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	// 同步间隔写库前校验: 0/负值会让任务注册直接删除且运行期无法恢复,
	// 超大值乘 time.Hour 会整型溢出成负间隔, 一旦落库重启后依旧生效。
	if setting.Key == model.SettingKeySyncLLMInterval {
		if hours, err := strconv.Atoi(setting.Value); err != nil || hours < 1 || hours > maxSyncLLMIntervalHours {
			resp.Error(c, http.StatusBadRequest, "sync_llm_interval 需为 1-8760 之间的整数(小时)")
			return
		}
	}
	if err := op.SettingSetString(setting.Key, setting.Value); err != nil {
		// 未知 key 或设置项不存在属于客户端错误, 返回 400 而非 500。
		errMsg := err.Error()
		if strings.Contains(errMsg, "不存在") || strings.Contains(errMsg, "not found") {
			resp.Error(c, http.StatusBadRequest, errMsg)
			return
		}
		resp.Error(c, http.StatusInternalServerError, errMsg)
		return
	}
	switch setting.Key {
	case model.SettingKeySyncLLMInterval:
		hours, _ := strconv.Atoi(setting.Value)
		task.Update(string(setting.Key), time.Duration(hours)*time.Hour)
	}
	resp.Success(c, setting)
}

func exportDB(c *gin.Context) {
	resp.NoStore(c)
	log.Warnf("database export ip=%s", c.ClientIP())
	dump, err := op.DBExportAll(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=\"novaveil-export-"+time.Now().Format("20060102150405")+".json\"")
	c.JSON(http.StatusOK, dump)
}

const (
	maxDBImportBodyBytes = 128 * 1024 * 1024
	maxDBImportObjects   = 100000
)

var errDBImportTooLarge = errors.New("database import is too large")

func importDB(c *gin.Context) {
	var dump model.DBDump

	// Apply the limit before multipart parsing so both the multipart envelope and
	// its file contents are bounded, then apply the object limit after decoding.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDBImportBodyBytes)

	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				resp.Error(c, http.StatusRequestEntityTooLarge, errDBImportTooLarge.Error())
			} else {
				resp.Error(c, http.StatusBadRequest, "missing upload file field 'file'")
			}
			return
		}
		f, err := fh.Open()
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, maxDBImportBodyBytes+1))
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if int64(len(body)) > maxDBImportBodyBytes {
			resp.Error(c, http.StatusRequestEntityTooLarge, errDBImportTooLarge.Error())
			return
		}
		if err := decodeDBDump(body, &dump); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxDBImportBodyBytes+1))
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				resp.Error(c, http.StatusRequestEntityTooLarge, errDBImportTooLarge.Error())
			} else {
				resp.Error(c, http.StatusBadRequest, err.Error())
			}
			return
		}
		if int64(len(body)) > maxDBImportBodyBytes {
			resp.Error(c, http.StatusRequestEntityTooLarge, errDBImportTooLarge.Error())
			return
		}
		if err := decodeDBDump(body, &dump); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := validateDBDumpLimits(&dump); err != nil {
		resp.Error(c, http.StatusRequestEntityTooLarge, err.Error())
		return
	}

	// dry-run: 仅预检不写入, 返回新增/跳过/冲突/无效引用/校验问题供确认。
	if dryRun, _ := strconv.ParseBool(c.Query("dry_run")); dryRun {
		preview, err := op.DBImportPreview(c.Request.Context(), &dump)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		resp.Success(c, preview)
		return
	}

	result, err := op.DBImportIncremental(c.Request.Context(), &dump)
	if err != nil {
		// 校验失败时返回预检详情, 让前端展示具体问题而非笼统的 400。
		var valErr *op.DBImportValidationError
		if errors.As(err, &valErr) {
			resp.Error(c, http.StatusBadRequest, valErr.Error())
			return
		}
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	// 提交后发布同代配置: InitCache 原子刷新全部缓存(各 RefreshAll)。
	// 失败时 DB 已提交但运行态部分旧, 返回 500 让运维感知并手动重建缓存。
	if err := op.InitCache(); err != nil {
		resp.Error(c, http.StatusInternalServerError, fmt.Sprintf("数据库已提交但缓存刷新失败, 请手动重启或重试: %v", err))
		return
	}

	// 应用任务副作用: 导入的设置若包含同步间隔, 更新运行期任务调度。
	applyImportedSettingSideEffects(&dump)

	// 导入可能用已删除渠道的旧 ID 重建渠道(如整库恢复), 只清理 dump 中涉及
	// 的渠道在 relay 侧遗留的冷却 / 限速 / 粘合状态, 防止新渠道被旧状态污染;
	// 不清理 dump 之外的渠道, 避免误伤其正在进行的冷却窗口。
	for _, ch := range dump.Channels {
		if ch.ID > 0 {
			relay.CleanupChannelKeyState(ch.ID)
		}
	}

	resp.Success(c, result)
}

func validateDBDumpLimits(dump *model.DBDump) error {
	if dump == nil {
		return nil
	}
	total := len(dump.Channels) + len(dump.Groups) + len(dump.ChannelModels) + len(dump.GroupItems) + len(dump.Settings) + len(dump.APIKeys) + len(dump.ClientStats) + len(dump.UsageBuckets)
	if total > maxDBImportObjects {
		return errDBImportTooLarge
	}
	return nil
}

func decodeDBDump(body []byte, dump *model.DBDump) error {
	if dump == nil {
		return json.Unmarshal(body, &struct{}{})
	}

	if err := json.Unmarshal(body, dump); err != nil {
		return err
	}

	if dump.Version == 0 &&
		len(dump.Channels) == 0 &&
		len(dump.Groups) == 0 &&
		len(dump.ChannelModels) == 0 &&
		len(dump.GroupItems) == 0 &&
		len(dump.Settings) == 0 &&
		len(dump.APIKeys) == 0 {
		var wrapper struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Data) > 0 {
			return json.Unmarshal(wrapper.Data, dump)
		}
	}

	return nil
}

// applyImportedSettingSideEffects 对导入的设置应用与正常写接口相同的运行期副作用。
// 目前仅 sync_llm_interval 需要更新任务调度; 其余设置在 InitCache 刷新缓存时自然生效。
func applyImportedSettingSideEffects(dump *model.DBDump) {
	for _, s := range dump.Settings {
		if s.Key != model.SettingKeySyncLLMInterval {
			continue
		}
		hours, err := strconv.Atoi(s.Value)
		if err != nil || hours < 1 || hours > maxSyncLLMIntervalHours {
			continue // 导入校验已拒绝非法值, 此处防御性跳过
		}
		task.Update(string(s.Key), time.Duration(hours)*time.Hour)
	}
}
