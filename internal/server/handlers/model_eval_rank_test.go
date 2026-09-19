package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handler 包的 applyProFromEvalRank 需要读取评估排序与更新 auto 分组, 依赖真实 DB;
// 用 testutil 初始化(与 internal/op 一致), 不含凭据的业务分支不涉及 Auth/RequireJSON。
func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// newTestChannel 在 handler 测试内创建一个启用渠道并挂载指定模型(复用 op 层导出能力),
// 返回带有回填模型 ID 的渠道。
func newTestChannel(t *testing.T, ctx context.Context, name string, modelNames ...string) model.Channel {
	t.Helper()
	models := make([]model.ChannelModel, len(modelNames))
	for i, n := range modelNames {
		models[i] = model.ChannelModel{Name: n}
	}
	ch := model.Channel{
		Name:    name,
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://example.invalid",
		Keys:    []model.ChannelKey{{Key: "sk-test-" + name}},
		Models:  models,
	}
	require.NoError(t, op.ChannelCreate(&ch, ctx))
	return ch
}

// cleanupAutoGroup 删除 applyProFromEvalRank 固化的 auto 分组, 同步清理 isNameIndex 缓存,
// 避免跨测试残留脏分组。
func cleanupAutoGroup(t *testing.T, ctx context.Context) {
	t.Helper()
	if g, err := op.GroupGetByName("auto"); err == nil {
		_ = op.GroupDel(g.ID, ctx)
	}
}

// TestApplyProFromEvalRankMixedResponseFields 验证含「停用 + 失效 + 正常」时,
// /rank/apply-pro 成功响应 data 含 kept_disabled 与 cleaned_stale 字段且非空,
// 序列化结果不含 Key/Secret 凭据字段。对齐 spec 4.3.2、design 2.2.2 接口 3。
func TestApplyProFromEvalRankMixedResponseFields(t *testing.T) {
	ctx := context.Background()
	chOK := newTestChannel(t, ctx, "h-ok", "h-ok-model")
	chDis := newTestChannel(t, ctx, "h-dis", "h-dis-model")

	enabled := false
	_, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: chDis.ID, Enabled: &enabled}, ctx)
	require.NoError(t, err)

	require.NoError(t, op.ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: chOK.ID, ChannelModelID: chOK.Models[0].ID, ChannelName: chOK.Name,
		ModelName: "h-ok-model", Outcome: model.ModelEvalOK, Content: "ok",
	}))
	require.NoError(t, op.ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: chDis.ID, ChannelModelID: chDis.Models[0].ID, ChannelName: chDis.Name,
		ModelName: "h-dis-model", Outcome: model.ModelEvalOK, Content: "dis",
	}))
	// 引用不存在渠道模型的失效排序记录。
	stale := &model.ModelEvalRank{
		ChannelID: chOK.ID, ChannelModelID: 555555, ChannelName: chOK.Name,
		ModelName: "h-stale-model", Outcome: model.ModelEvalOK, Content: "stale", Position: 100,
	}
	require.NoError(t, db.GetDB().Create(stale).Error)

	t.Cleanup(func() {
		cleanupAutoGroup(t, ctx)
		_ = op.ChannelDel(chOK.ID, ctx)
		_ = op.ChannelDel(chDis.ID, ctx)
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-eval/rank/apply-pro", nil)
	applyProFromEvalRank(c)

	require.Equal(t, http.StatusOK, w.Code)

	raw := w.Body.String()
	assert.NotContains(t, raw, "sk-test", "响应不应泄露渠道 Key")
	assert.NotContains(t, raw, "secret", "响应不应泄露凭据")

	var body struct {
		Code int `json:"code"`
		Data struct {
			GroupID      int                    `json:"group_id"`
			Created      bool                   `json:"created"`
			ItemCount    int                    `json:"item_count"`
			KeptDisabled []op.GroupReplaceEntry `json:"kept_disabled"`
			CleanedStale []op.GroupReplaceEntry `json:"cleaned_stale"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, http.StatusOK, body.Code)
	assert.Equal(t, 2, body.Data.ItemCount, "正常 + 停用模型应入组")

	require.Len(t, body.Data.KeptDisabled, 1, "停用保留应反馈到 kept_disabled")
	assert.Equal(t, chDis.Models[0].ID, body.Data.KeptDisabled[0].ChannelModelID)
	assert.Equal(t, "h-dis-model", body.Data.KeptDisabled[0].ModelName)

	require.Len(t, body.Data.CleanedStale, 1, "失效清理应反馈到 cleaned_stale")
	assert.Equal(t, 555555, body.Data.CleanedStale[0].ChannelModelID)
	assert.Equal(t, "h-stale-model", body.Data.CleanedStale[0].ModelName)
}

// TestApplyProFromEvalRankEmptyReportArrays 验证仅正常模型时, kept_disabled 与
// cleaned_stale 序列化为空数组([])而非 null。对齐 design 3.1 的空明细序列化约定。
func TestApplyProFromEvalRankEmptyReportArrays(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, ctx, "h-normal", "h-normal-model")

	require.NoError(t, op.ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: ch.Models[0].ID, ChannelName: ch.Name,
		ModelName: "h-normal-model", Outcome: model.ModelEvalOK, Content: "ok",
	}))

	t.Cleanup(func() {
		cleanupAutoGroup(t, ctx)
		_ = op.ChannelDel(ch.ID, ctx)
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-eval/rank/apply-pro", nil)
	applyProFromEvalRank(c)

	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Code int `json:"code"`
		Data struct {
			KeptDisabled []op.GroupReplaceEntry `json:"kept_disabled"`
			CleanedStale []op.GroupReplaceEntry `json:"cleaned_stale"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, http.StatusOK, body.Code)
	assert.NotNil(t, body.Data.KeptDisabled, "kept_disabled 应为空数组而非 null")
	assert.Empty(t, body.Data.KeptDisabled)
	assert.NotNil(t, body.Data.CleanedStale, "cleaned_stale 应为空数组而非 null")
	assert.Empty(t, body.Data.CleanedStale)
}

// TestApplyProFromEvalRankNoRankableReturns400 验证无任何可入组排序时,
// GroupReplaceItemsByName 返回 ErrGroupReplaceNoRankable, handler 映射为 400。
func TestApplyProFromEvalRankNoRankableReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-eval/rank/apply-pro", nil)
	applyProFromEvalRank(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestApplyProFromEvalRankListErrorReturns500 验证排序列表查询失败时 handler 返回 500。
// 做法: 临时删除 model_eval_ranks 表迫使 ModelEvalRankList 失败。
func TestApplyProFromEvalRankListErrorReturns500(t *testing.T) {
	require.NoError(t, db.GetDB().Migrator().DropTable(&model.ModelEvalRank{}))
	t.Cleanup(func() {
		require.NoError(t, db.GetDB().AutoMigrate(&model.ModelEvalRank{}))
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-eval/rank/apply-pro", nil)
	applyProFromEvalRank(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestApplyProFromEvalRankGroupReplaceErrorReturns500 验证分组替换写库失败(非分类错误)时,
// handler 走 default 分支返回 500。做法: 临时删除 group_items 表迫使事务内成员写入失败。
func TestApplyProFromEvalRankGroupReplaceErrorReturns500(t *testing.T) {
	ctx := context.Background()
	ch := newTestChannel(t, ctx, "h-grperr", "h-grperr-model")
	require.NoError(t, op.ModelEvalRankUpsert(ctx, &model.ModelEvalRank{
		ChannelID: ch.ID, ChannelModelID: ch.Models[0].ID, ChannelName: ch.Name,
		ModelName: "h-grperr-model", Outcome: model.ModelEvalOK, Content: "ok",
	}))

	require.NoError(t, db.GetDB().Migrator().DropTable(&model.GroupItem{}))
	t.Cleanup(func() {
		require.NoError(t, db.GetDB().AutoMigrate(&model.GroupItem{}))
		_ = op.ChannelDel(ch.ID, ctx)
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-eval/rank/apply-pro", nil)
	applyProFromEvalRank(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}