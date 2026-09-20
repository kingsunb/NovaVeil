package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/keylimit"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/server/auth"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/apikey").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createAPIKey),
		).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listAPIKey),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateAPIKey),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteAPIKey),
		).
		AddRoute(
			router.NewRoute("/secret/:id", http.MethodPost).
				Handle(getAPIKeySecret),
		)
}

func createAPIKey(c *gin.Context) {
	var req model.APIKey
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if req.MaxConcurrent < 0 || req.RateLimitRPM < 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey == "" {
		generated, genErr := auth.GenerateAPIKey()
		if genErr != nil {
			resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
			return
		}
		req.APIKey = generated
	}
	if err := op.APIKeyCreate(&req, c.Request.Context()); err != nil {
		switch {
		case errors.Is(err, op.ErrAPIKeyValidation):
			// 自定义 Key 长度不足 16: 客户端输入错误(审计 SEC-05/S-L1)。
			resp.Error(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, op.ErrAPIKeyValueExists):
			// 用户提交了与现有 Key 相同的明文: 客户端冲突, 应明确 409。
			resp.Error(c, http.StatusConflict, "API key value already exists")
		default:
			resp.Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	resp.Success(c, req)
}

type apiKeySummary struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	APIKeyMasked    string `json:"api_key_masked"`
	Enabled         bool   `json:"enabled"`
	ExpireAt        int64  `json:"expire_at,omitempty"`
	SupportedModels string `json:"supported_models,omitempty"`
	MaxConcurrent   int    `json:"max_concurrent"`
	RateLimitRPM    int    `json:"rate_limit_rpm"`
	CreatedAt       int64  `json:"created_at,omitempty"`
	LastUsedAt      int64  `json:"last_used_at,omitempty"`
}

func maskedSecret(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

func apiKeyToSummary(key model.APIKey) apiKeySummary {
	return apiKeySummary{
		ID:              key.ID,
		Name:            key.Name,
		APIKeyMasked:    maskedSecret(key.APIKey),
		Enabled:         key.Enabled,
		ExpireAt:        key.ExpireAt,
		SupportedModels: key.SupportedModels,
		MaxConcurrent:   key.MaxConcurrent,
		RateLimitRPM:    key.RateLimitRPM,
		CreatedAt:       key.CreatedAt,
		LastUsedAt:      key.LastUsedAt,
	}
}

func listAPIKey(c *gin.Context) {
	apiKeys, err := op.APIKeyList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	summaries := make([]apiKeySummary, len(apiKeys))
	for i := range apiKeys {
		summaries[i] = apiKeyToSummary(apiKeys[i])
	}
	resp.Success(c, summaries)
}

func updateAPIKey(c *gin.Context) {
	var req model.APIKey
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if req.MaxConcurrent < 0 || req.RateLimitRPM < 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if err := op.APIKeyUpdate(&req, c.Request.Context()); err != nil {
		switch {
		case errors.Is(err, op.ErrAPIKeyValidation):
			resp.Error(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, op.ErrAPIKeyValueExists):
			resp.Error(c, http.StatusConflict, "API key value already exists")
		case errors.Is(err, op.ErrAPIKeyNotFound):
			resp.Error(c, http.StatusNotFound, "API key not found")
		default:
			resp.Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	resp.Success(c, apiKeyToSummary(req))
}

func deleteAPIKey(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.APIKeyDelete(idNum, c.Request.Context()); err != nil {
		if errors.Is(err, op.ErrAPIKeyNotFound) {
			resp.Error(c, http.StatusNotFound, "API key not found")
			return
		}
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 限流状态与密钥同生命周期: 删除后清理信号量与 RPM 窗口, 避免条目滞留。
	keylimit.Cleanup(idNum)
	resp.Success(c, nil)
}

// getAPIKeySecret 返回单个 API 密钥的明文, 供管理端"编辑时查看密钥"按钮按需获取。
// 列表接口出于安全只回掩码, 本接口与渠道密钥明文接口同级, 仅限管理员会话访问。
func getAPIKeySecret(c *gin.Context) {
	resp.NoStore(c)
	log.Warnf("api key reveal id=%s ip=%s", c.Param("id"), c.ClientIP())
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	apiKey, err := op.APIKeyGet(id, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, err.Error())
		return
	}
	resp.Success(c, gin.H{"api_key": apiKey.APIKey})
}
