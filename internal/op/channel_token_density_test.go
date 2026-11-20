package op

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestChannelUpdateBaseURLResetsDensity(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    fmt.Sprintf("density-reset-%d", time.Now().UnixNano()),
		Type:    model.ChannelProviderOpenAI,
		BaseURL: "https://density-a.example",
		Models:  []model.ChannelModel{{Name: "density-model"}},
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	t.Cleanup(func() { _ = ChannelDel(channel.ID, context.Background()) })

	if err := TokenDensitySave(ctx, channel.ID, "density-model", 0.87, 3, 1, "2026-11-19"); err != nil {
		t.Fatalf("TokenDensitySave: %v", err)
	}

	stale := requireTokenDensitySnapshot(t, channel, "density-model")
	newBaseURL := "https://density-b.example"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, BaseURL: &newBaseURL}, ctx); err != nil {
		t.Fatalf("ChannelUpdate: %v", err)
	}

	var count int64
	if err := db.GetDB().Model(&model.TokenDensity{}).Where("channel_id = ?", channel.ID).Count(&count).Error; err != nil {
		t.Fatalf("count densities: %v", err)
	}
	if count != 0 {
		t.Fatalf("base url change should clear densities, got %d rows", count)
	}
	if accepted, err := TokenDensityObserve(ctx, stale, 4000, 2000); err != nil || accepted {
		t.Fatalf("旧端点观测必须失效: accepted=%v err=%v", accepted, err)
	}
	oldEndpoint := requireTokenDensitySnapshot(t, channel, "density-model")
	if accepted, err := TokenDensityObserve(ctx, oldEndpoint, 4000, 2000); err != nil || accepted {
		t.Fatalf("旧配置应被忽略: accepted=%v err=%v", accepted, err)
	}
	channel.BaseURL = newBaseURL
	fresh := requireTokenDensitySnapshot(t, channel, "density-model")
	if fresh.Density != DefaultTokenDensity {
		t.Fatalf("新端点应从默认密度开始: %v", fresh.Density)
	}
	if accepted, err := TokenDensityObserve(ctx, fresh, 2000, 2000); err != nil || !accepted {
		t.Fatalf("新端点应正常校准: accepted=%v err=%v", accepted, err)
	}
}

func TestChannelModelRemovalResetsTokenDensity(t *testing.T) {
	ctx := context.Background()
	channel := createTokenDensityChannel(t)
	if err := TokenDensitySave(ctx, channel.ID, "density-model", 2, 13, 3, time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	stale := requireTokenDensitySnapshot(t, channel, "density-model")
	retained := requireTokenDensitySnapshot(t, channel, "retained-model")
	models := []model.ChannelModel{{Name: "retained-model"}}
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Models: &models}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := TokenDensityGet(ctx, channel.ID, "density-model"); err != nil || found {
		t.Fatalf("模型删除后密度仍存在: found=%v err=%v", found, err)
	}
	missing := requireTokenDensitySnapshot(t, channel, "density-model")
	if accepted, err := TokenDensityObserve(ctx, missing, 4000, 2000); err != nil || accepted {
		t.Fatalf("已删除的模型不得校准: accepted=%v err=%v", accepted, err)
	}
	models = append(models, model.ChannelModel{Name: "density-model"})
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Models: &models}, ctx); err != nil {
		t.Fatal(err)
	}
	fresh := requireTokenDensitySnapshot(t, channel, "density-model")
	if fresh.Density != DefaultTokenDensity {
		t.Fatalf("同名模型重建沿用了旧密度: %v", fresh.Density)
	}
	if accepted, err := TokenDensityObserve(ctx, stale, 4000, 2000); err != nil || accepted {
		t.Fatalf("同名重建不得恢复旧观测: accepted=%v err=%v", accepted, err)
	}
	for _, snapshot := range []TokenDensitySnapshot{fresh, retained} {
		if accepted, err := TokenDensityObserve(ctx, snapshot, 2000, 2000); err != nil || !accepted {
			t.Fatalf("有效模型应能校准: accepted=%v err=%v", accepted, err)
		}
	}
	row, found, err := TokenDensityGet(ctx, channel.ID, "density-model")
	if err != nil || !found || row.Density != 1 || row.SampleCount != 1 {
		t.Fatalf("重建模型未重新学习: row=%+v found=%v err=%v", row, found, err)
	}
}

func TestChannelDeleteDiscardsTokenDensityObservations(t *testing.T) {
	channel := createTokenDensityChannel(t)
	snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
	if err := ChannelDel(channel.ID, context.Background()); err != nil {
		t.Fatal(err)
	}
	if accepted, err := TokenDensityObserve(context.Background(), snapshot, 4000, 2000); err != nil || accepted {
		t.Fatalf("已删除渠道不得恢复密度: accepted=%v err=%v", accepted, err)
	}
}

func TestChannelModelUpdateFailureKeepsTokenDensity(t *testing.T) {
	channel := createTokenDensityChannel(t)
	snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
	models := []model.ChannelModel{{Name: ""}}
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Models: &models}, context.Background()); err == nil {
		t.Fatal("非法模型更新应失败")
	}
	if accepted, err := TokenDensityObserve(context.Background(), snapshot, 4000, 2000); err != nil || !accepted {
		t.Fatalf("事务回滚不应废弃有效观测: accepted=%v err=%v", accepted, err)
	}
}
