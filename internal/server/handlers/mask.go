package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	mask "github.com/kingsunb/NovaVeil/internal/relay/mask"
	"github.com/kingsunb/NovaVeil/internal/server/middleware"
	"github.com/kingsunb/NovaVeil/internal/server/resp"
	"github.com/kingsunb/NovaVeil/internal/server/router"
)

// 脱敏管理 API(文档 04 §二)。路由挂 /api/v1/mask, 全部需管理员鉴权。
// /test 响应含原文↔占位符映射, 绝不入库、不转发、不写日志, 仅供管理员预览规则效果。
func init() {
	router.NewGroupRouter("/api/v1/mask").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/config", http.MethodGet).
				Handle(getMaskConfig),
		).
		AddRoute(
			router.NewRoute("/config", http.MethodPut).
				Use(middleware.RequireJSON()).
				Handle(putMaskConfig),
		).
		AddRoute(
			router.NewRoute("/rules", http.MethodGet).
				Handle(getMaskRules),
		).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(testMask),
		)
}

// getMaskConfig 返回当前脱敏全局配置。设置项缺失时 op 层已收敛为全关默认, 此处直接透传。
func getMaskConfig(c *gin.Context) {
	cfg, err := op.MaskConfigGet()
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, cfg)
}

// putMaskConfig 更新脱敏全局配置: 绑定 JSON 后交 op 层校验写库, 热生效。
func putMaskConfig(c *gin.Context) {
	var cfg model.MaskConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.MaskConfigSet(cfg); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, cfg)
}

// getMaskRules 返回内置规则元信息(标签/说明/默认开关), 供管理台渲染规则开关列表。
// 元信息由脱敏引擎层统一维护, 此处仅透传, 避免本层与规则集版本耦合。
func getMaskRules(c *gin.Context) {
	resp.Success(c, mask.BuiltinRuleMeta)
}

// maskTestMatch 单次脱敏测试的命中明细。
type maskTestMatch struct {
	Label       string `json:"label"`       // 规则标签, 如 "PHONE"/"TERM", 与配置开关 ID 一致。
	Original    string `json:"original"`    // 命中原文。
	Placeholder string `json:"placeholder"` // 替换占位符, 如 "{{REDACTED_PHONE_NUMBER_bcdfgh}}"。
}

// maskTestResult 脱敏测试响应: 脱敏后文本与命中明细, 仅供管理员预览, 绝不落库。
type maskTestResult struct {
	Masked  string          `json:"masked"`  // 脱敏后文本。
	Matches []maskTestMatch `json:"matches"` // 命中明细, 按在脱敏后文本中的出现顺序排列。
}

// testMask 对输入文本执行一次脱敏并返回结果与命中明细。
//
// 用当前配置启用的内置规则与自定义词构建一次性引擎, 会话键留空(预览无需跨轮复用)。
// 响应含原文映射, 仅回写给调用方, 绝不写日志/入库(凭据安全红线, 文档 04 §2.3)。
func testMask(c *gin.Context) {
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := op.MaskConfigGet()
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 内置规则开关: 预览不受全局 Enabled 开关约束, 管理员可在启用前先看规则效果。
	enabledRules := cfg.BuiltinRuleSwitch
	// 自定义敏感词取原文, 跳过空白。
	terms := make([]mask.CustomTerm, 0, len(cfg.CustomTerms))
	for _, t := range cfg.CustomTerms {
		if v := strings.TrimSpace(t.Value); v != "" {
			terms = append(terms, mask.CustomTerm{Value: v, Category: t.Category})
		}
	}

	// 一次性引擎: 独立 SessionStore, 会话键空, 预览结束即随函数退出回收, 映射表不落库。
	engine := mask.NewEngine(mask.NewSessionStore())
	res, err := engine.Apply(req.Text, "", enabledRules, terms)
	if err != nil {
		// fail-closed: 脱敏失败绝不放行明文, 预览亦返回错误。
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 引擎已返回结构化命中明细(标签/原文/占位符), 直接映射为响应。
	matches := make([]maskTestMatch, 0, len(res.Matches))
	for _, m := range res.Matches {
		matches = append(matches, maskTestMatch{
			Label:       m.Label,
			Original:    m.Original,
			Placeholder: m.Placeholder,
		})
	}
	// 按占位符在脱敏后文本中的出现顺序排序, 与文档示例的命中顺序对齐, 便于管理员逐条核对。
	sort.SliceStable(matches, func(i, j int) bool {
		return strings.Index(res.Masked, matches[i].Placeholder) < strings.Index(res.Masked, matches[j].Placeholder)
	})

	resp.Success(c, maskTestResult{Masked: res.Masked, Matches: matches})
}
