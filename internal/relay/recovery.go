package relay

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// probeChannel 向渠道发送最小聊天合成请求验证其可用性, 取得任一有效响应即视为探测成功。
func probeChannel(ctx context.Context, channel model.Channel, modelName string) error {
	// 外部辅助调用也可能绕过 route/prober 直接进入 probeChannel, 因此这里再做一次幂等构造:
	// 若传入渠道尚未写入多 Key 的有效 Key, 固定选择第一把健康 Key 并解析对应代理账号;
	// 已由 route/prober 构造过的渠道则保持其 Key 与已解析代理不变。
	if len(channel.Keys) > 0 && channel.Key != channel.Keys[0].Key {
		var effectiveErr error
		channel, _, _, effectiveErr = effectiveProbeChannel(channel)
		if effectiveErr != nil {
			return effectiveErr
		}
	}
	body, err := json.Marshal(map[string]any{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	if err != nil {
		return err
	}
	outbound, passthrough, err := buildOutbound(channel, nil, llm.APIFormatOpenAIChatCompletion)
	if err != nil {
		return err
	}
	raw := &httpclient.Request{
		Method:  http.MethodPost,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body,
	}
	// 同协议渠道原样直通, 其余渠道经 pipeline 转换后请求。
	// 探测结论直接决定成员恢复或加重冷却, 瞬时网络抖动不应计入, 按网络错误重试策略
	// 容忍基础设施层错误后再下结论, 与分组路由对真实流量的容错口径一致。
	result, err := sendDiagnosticUpstream(ctx, func() (*upstreamResponse, error) {
		if passthrough {
			return sendPassthrough(ctx, llm.APIFormatOpenAIChatCompletion, raw, channel, outbound, false, "")
		}
		return sendConverted(ctx, llm.APIFormatOpenAIChatCompletion, raw, channel, outbound, false, "")
	})
	if result != nil {
		result.Close()
	}
	return err
}
