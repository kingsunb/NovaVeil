package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// TestChannelKeyFailoverLimitsAttempts 验证模型评估的密钥探测上限:
// 渠道密钥多于 evalKeyAttemptLimit 时, 评估按配置顺序只尝试前 N 把,
// 全部失败后即以「已达单次评估尝试上限」报错, 不再逐把烧计费请求。
func TestChannelKeyFailoverLimitsAttempts(t *testing.T) {
	setupFailoverTest(t)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer upstream.Close()

	keys := make([]model.ChannelKey, 0, evalKeyAttemptLimit+2)
	for i := 0; i < evalKeyAttemptLimit+2; i++ {
		keys = append(keys, model.ChannelKey{Key: fmt.Sprintf("bad-key-%d", i)})
	}
	channel := model.Channel{
		Name:    integrationUniqueName("it-eval-key-limit"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys:    keys,
		Models:  []model.ChannelModel{{Name: "it-eval-key-limit-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	result, err := TestChannelKeyFailover(context.Background(), channel.ID, "it-eval-key-limit-model", "ping")
	if err == nil {
		t.Fatalf("全部密钥失效应返回聚合错误, 实际成功: %+v", result)
	}
	if got := hits.Load(); got != evalKeyAttemptLimit {
		t.Fatalf("应只尝试前 %d 把密钥, 实际上游被请求 %d 次", evalKeyAttemptLimit, got)
	}
	if !strings.Contains(err.Error(), "已达单次评估尝试上限") {
		t.Fatalf("超限错误应提示单次尝试上限, 实际: %v", err)
	}
}

// TestChannelKeyFailoverShortCircuitsWithinLimit 验证密钥数不超过上限时行为不变:
// 前把全坏、后把可用, 评估仍按顺序短路到可用密钥, 不会被误截断。
func TestChannelKeyFailoverShortCircuitsWithinLimit(t *testing.T) {
	setupFailoverTest(t)
	upstream := newKeyTestUpstream(t)
	defer upstream.Close()

	keys := make([]model.ChannelKey, 0, evalKeyAttemptLimit)
	for i := 0; i < evalKeyAttemptLimit-1; i++ {
		keys = append(keys, model.ChannelKey{Key: fmt.Sprintf("bad-key-%d", i)})
	}
	keys = append(keys, model.ChannelKey{Key: "good-key", Remark: "可用"})
	channel := model.Channel{
		Name:    integrationUniqueName("it-eval-key-shortcut"),
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: upstream.URL,
		Keys:    keys,
		Models:  []model.ChannelModel{{Name: "it-eval-key-shortcut-model", Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	result, err := TestChannelKeyFailover(context.Background(), channel.ID, "it-eval-key-shortcut-model", "ping")
	if err != nil {
		t.Fatalf("末尾可用密钥应被短路命中: %v", err)
	}
	if result.Content != "走路" {
		t.Fatalf("应返回可用密钥的回复摘要, 实际: %+v", result)
	}
}