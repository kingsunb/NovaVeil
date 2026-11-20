package op

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestTokenDensitySaveGetDelete(t *testing.T) {
	ctx := context.Background()
	const channelID = 9901
	const modelName = "density-persist-test"
	cleanup := func() {
		_ = db.GetDB().Where("channel_id = ? AND model = ?", channelID, modelName).Delete(&model.TokenDensity{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := TokenDensitySave(ctx, channelID, modelName, 0.87, 3, 2, "2026-11-19"); err != nil {
		t.Fatalf("save density: %v", err)
	}
	row, found, err := TokenDensityGet(ctx, channelID, modelName)
	if err != nil || !found {
		t.Fatalf("get density: found=%v err=%v", found, err)
	}
	if row.Density != 0.87 || row.SampleCount != 3 || row.DailySamples != 2 || row.SampleDay != "2026-11-19" {
		t.Fatalf("density row = %+v", row)
	}

	if err := TokenDensitySave(ctx, channelID, modelName, 0.9, 4, 3, "2026-11-20"); err != nil {
		t.Fatalf("update density: %v", err)
	}
	row, found, err = TokenDensityGet(ctx, channelID, modelName)
	if err != nil || !found || row.Density != 0.9 || row.SampleCount != 4 || row.DailySamples != 3 || row.SampleDay != "2026-11-20" {
		t.Fatalf("updated density row = %+v, found=%v err=%v", row, found, err)
	}

	if err := TokenDensityDelete(ctx, channelID, modelName); err != nil {
		t.Fatalf("delete density: %v", err)
	}
	if _, found, err := TokenDensityGet(ctx, channelID, modelName); err != nil || found {
		t.Fatalf("deleted density still present: found=%v err=%v", found, err)
	}
}

func TestTokenDensityListIncludesChannelName(t *testing.T) {
	ctx := context.Background()
	channel := model.Channel{
		Name:    "density-list-test",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://density-list.invalid",
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() {
		_ = db.GetDB().Where("channel_id = ?", channel.ID).Delete(&model.TokenDensity{}).Error
		_ = db.GetDB().Delete(&model.Channel{}, channel.ID).Error
	})

	if err := TokenDensitySave(ctx, channel.ID, "density-list-model", 0.5, 1, 1, "2026-11-19"); err != nil {
		t.Fatalf("save density: %v", err)
	}
	rows, err := TokenDensityList(ctx)
	if err != nil {
		t.Fatalf("list density: %v", err)
	}
	for _, row := range rows {
		if row.ChannelID == channel.ID && row.Model == "density-list-model" {
			if row.ChannelName != channel.Name {
				t.Fatalf("channel name = %q, want %q", row.ChannelName, channel.Name)
			}
			return
		}
	}
	t.Fatal("density row not found in list")
}

func createTokenDensityChannel(t *testing.T) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name: fmt.Sprintf("density-lifecycle-%d", time.Now().UnixNano()),
		Type: model.ChannelProviderOpenAI, BaseURL: "https://density.example",
		Models: []model.ChannelModel{{Name: "density-model"}, {Name: "retained-model"}},
	}
	if err := ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() {
		if _, err := ChannelGet(channel.ID); err == nil {
			if err := ChannelDel(channel.ID, context.Background()); err != nil {
				t.Errorf("delete channel: %v", err)
			}
		}
	})
	return channel
}

func requireTokenDensitySnapshot(t *testing.T, channel model.Channel, modelName string) TokenDensitySnapshot {
	t.Helper()
	snapshot, err := TokenDensitySnapshotGet(context.Background(), channel.ID, channel.BaseURL, modelName)
	if err != nil {
		t.Fatalf("get density snapshot: %v", err)
	}
	return snapshot
}

func TestTokenDensityResetDiscardsInFlightObservations(t *testing.T) {
	for _, resetAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("all=%v", resetAll), func(t *testing.T) {
			ctx := context.Background()
			channel := createTokenDensityChannel(t)
			stale := requireTokenDensitySnapshot(t, channel, "density-model")
			if accepted, err := TokenDensityObserve(ctx, stale, 4000, 2000); err != nil || !accepted {
				t.Fatalf("initialize density: accepted=%v err=%v", accepted, err)
			}
			var err error
			if resetAll {
				err = TokenDensityClear(ctx)
			} else {
				err = TokenDensityDelete(ctx, channel.ID, "density-model")
			}
			if err != nil {
				t.Fatal(err)
			}
			fresh := requireTokenDensitySnapshot(t, channel, "density-model")
			if fresh.Density != DefaultTokenDensity {
				t.Fatalf("清除后仍使用旧密度: %v", fresh.Density)
			}
			if accepted, err := TokenDensityObserve(ctx, stale, 6000, 2000); err != nil || accepted {
				t.Fatalf("旧观测不得复活: accepted=%v err=%v", accepted, err)
			}
			if _, found, err := TokenDensityGet(ctx, channel.ID, "density-model"); err != nil || found {
				t.Fatalf("清除后旧行复活: found=%v err=%v", found, err)
			}
			if accepted, err := TokenDensityObserve(ctx, fresh, 2000, 2000); err != nil || !accepted {
				t.Fatalf("新观测应重新学习: accepted=%v err=%v", accepted, err)
			}
			row, found, err := TokenDensityGet(ctx, channel.ID, "density-model")
			if err != nil || !found || row.SampleCount != 1 || row.Density != 1 {
				t.Fatalf("重新学习结果=%+v found=%v err=%v", row, found, err)
			}
		})
	}
}

func TestTokenDensityResetSerializesConcurrentObservations(t *testing.T) {
	ctx := context.Background()
	channel := createTokenDensityChannel(t)
	for _, resetAll := range []bool{false, true} {
		for i := 0; i < 16; i++ {
			snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
			gate := make(chan struct{})
			done := make(chan error, 2)
			go func() {
				<-gate
				_, err := TokenDensityObserve(ctx, snapshot, 4000, 2000)
				done <- err
			}()
			go func() {
				<-gate
				if resetAll {
					done <- TokenDensityClear(ctx)
				} else {
					done <- TokenDensityDelete(ctx, channel.ID, "density-model")
				}
			}()
			close(gate)
			for j := 0; j < 2; j++ {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("密度清除与写入发生死锁")
				}
			}
			if _, found, err := TokenDensityGet(ctx, channel.ID, "density-model"); err != nil || found {
				t.Fatalf("并发清除后旧行复活: found=%v err=%v", found, err)
			}
		}
	}
}

func TestTokenDensityFailedResetKeepsObservationValid(t *testing.T) {
	channel := createTokenDensityChannel(t)
	snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := TokenDensityDelete(ctx, channel.ID, "density-model"); err == nil {
		t.Fatal("已取消的删除必须报错")
	}
	if accepted, err := TokenDensityObserve(context.Background(), snapshot, 4000, 2000); err != nil || !accepted {
		t.Fatalf("删除失败不应废弃观测: accepted=%v err=%v", accepted, err)
	}
}

func TestTokenDensityConcurrentObservationsHonorDailyLimit(t *testing.T) {
	ctx := context.Background()
	channel := createTokenDensityChannel(t)
	snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
	type result struct {
		accepted bool
		err      error
	}
	const workers = 32
	gate := make(chan struct{})
	results := make(chan result, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-gate
			accepted, err := TokenDensityObserve(ctx, snapshot, 4000, 2000)
			results <- result{accepted: accepted, err: err}
		}()
	}
	close(gate)
	accepted := 0
	for i := 0; i < workers; i++ {
		select {
		case observed := <-results:
			if observed.err != nil {
				t.Fatal(observed.err)
			}
			if observed.accepted {
				accepted++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("并发密度校准阻塞")
		}
	}
	row, found, err := TokenDensityGet(ctx, channel.ID, "density-model")
	if err != nil || !found || accepted != 13 || row.SampleCount != 13 || row.DailySamples != 3 || row.Density != 2 {
		t.Fatalf("并发采样越过预算: row=%+v accepted=%d found=%v err=%v", row, accepted, found, err)
	}
}

func TestTokenDensityFailedObservationDoesNotConsumeSample(t *testing.T) {
	channel := createTokenDensityChannel(t)
	snapshot := requireTokenDensitySnapshot(t, channel, "density-model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if accepted, err := TokenDensityObserve(ctx, snapshot, 4000, 2000); err == nil || accepted {
		t.Fatalf("写入失败必须报错: accepted=%v err=%v", accepted, err)
	}
	if accepted, err := TokenDensityObserve(context.Background(), snapshot, 2000, 2000); err != nil || !accepted {
		t.Fatalf("有效请求应能重试学习: accepted=%v err=%v", accepted, err)
	}
	row, found, err := TokenDensityGet(context.Background(), channel.ID, "density-model")
	if err != nil || !found || row.SampleCount != 1 || row.Density != 1 {
		t.Fatalf("失败写入污染了采样状态: row=%+v found=%v err=%v", row, found, err)
	}
}
