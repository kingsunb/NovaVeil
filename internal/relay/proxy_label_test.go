package relay

import (
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// assertProxyLabel 校验展示文案的契约: 保留主机端口、不回显密码段。
// 不锁定打码后的完整字面量: 非 http 系 scheme 的 userinfo 渲染由标准库决定, 与契约无关。
func assertProxyLabel(t *testing.T, label, host, secret, scene string) {
	t.Helper()
	if !strings.Contains(label, host) {
		t.Fatalf("%s 应保留代理主机端口 %s, 实际 %q", scene, host, label)
	}
	if strings.Contains(label, secret) {
		t.Fatalf("%s 不得回显代理密码 %s, 实际 %q", scene, secret, label)
	}
}

// TestRoundProxyLabel 验证三种出站形态在日志中的代理标注:
// 关闭代理不标注(直连), 渠道专属代理输出打码地址, 仅开启开关时标注系统代理地址。
func TestRoundProxyLabel(t *testing.T) {
	channelProxy := "socks5://user:secret@127.0.0.1:1080"
	channel := model.Channel{ID: 7, Name: "proxy-channel", Proxy: true, ChannelProxy: &channelProxy}
	assertProxyLabel(t, roundProxyLabel(channel, channel), "127.0.0.1:1080", "secret", "渠道专属代理")

	direct := model.Channel{ID: 8, Name: "direct-channel", Proxy: false, ChannelProxy: &channelProxy}
	if got := roundProxyLabel(direct, direct); got != "" {
		t.Fatalf("关闭代理的渠道不应标注代理信息, 实际 %q", got)
	}

	// 系统代理地址由配置注入, 这里替换查找函数避免依赖全局设置缓存的初始状态。
	originalLookup := systemProxyLookup
	t.Cleanup(func() { systemProxyLookup = originalLookup })
	systemProxy := "http://admin:pass@127.0.0.1:7890"
	systemProxyLookup = func() string { return systemProxy }

	systemProxyChannel := model.Channel{ID: 9, Name: "system-proxy-channel", Proxy: true}
	got := roundProxyLabel(systemProxyChannel, systemProxyChannel)
	assertProxyLabel(t, got, "127.0.0.1:7890", "pass", "系统代理")
	if !strings.Contains(got, "系统代理") {
		t.Fatalf("系统代理应与渠道专属代理可区分, 实际 %q", got)
	}

	// 空白的专属地址同样按系统代理处理, 避免误判为已配置。
	blank := "   "
	blankProxyChannel := model.Channel{ID: 10, Name: "blank-proxy-channel", Proxy: true, ChannelProxy: &blank}
	assertProxyLabel(t, roundProxyLabel(blankProxyChannel, blankProxyChannel), "127.0.0.1:7890", "pass", "空白专属代理地址")

	// 系统代理地址缺失时仍给出可读提示, 而不是什么都不显示。
	systemProxy = ""
	if got := roundProxyLabel(systemProxyChannel, systemProxyChannel); got != systemProxyUnsetLabel {
		t.Fatalf("系统代理未配置时应给出提示文案, 实际 %q", got)
	}
}

// TestRecordTestRequestProxyLabel 面板测试与真实转发走同一套 ChannelHttpClient 出站,
// 日志流条目必须带上同样的代理标注: 否则渠道测试明明经代理出去, 日志页却显示直连。
func TestRecordTestRequestProxyLabel(t *testing.T) {
	beforeID := idSeq.Load()
	defer cleanupProbeRequests(beforeID)
	// 替换系统代理查找, 避免依赖全局设置缓存的初始状态。
	originalLookup := systemProxyLookup
	t.Cleanup(func() { systemProxyLookup = originalLookup })
	systemProxy := "http://admin:pass@127.0.0.1:7890"
	systemProxyLookup = func() string { return systemProxy }

	record := func(t *testing.T, channel model.Channel) RequestState {
		t.Helper()
		_, _, target := testRequestTarget(channel, "proxy-label-model", "")
		probe := startTestRequest("proxy-label-model", []byte("{}"), target, nil)
		finishTestRequest(probe, "{}", nil, nil)
		id := probe.ID
		mu.Lock()
		defer mu.Unlock()
		request, ok := requests[id]
		if !ok {
			t.Fatalf("面板测试条目 %d 未登记", id)
		}
		return *request
	}
	cleanup := func(t *testing.T, id uint64) {
		t.Helper()
		mu.Lock()
		delete(requests, id)
		mu.Unlock()
	}

	// 渠道专属代理: 标注打码地址, 保留主机端口、不回显密码。
	channelProxy := "socks5://user:secret@127.0.0.1:1080"
	proxied := model.Channel{ID: 21, Name: "label-test-proxy", Proxy: true, ChannelProxy: &channelProxy}
	state := record(t, proxied)
	defer cleanup(t, state.ID)
	assertProxyLabel(t, state.ProxyAddr, "127.0.0.1:1080", "secret", "面板测试渠道专属代理")

	// 关闭代理: 与真实转发一致, 不标注(直连)。
	direct := model.Channel{ID: 22, Name: "label-test-direct", Proxy: false, ChannelProxy: &channelProxy}
	state = record(t, direct)
	defer cleanup(t, state.ID)
	if state.ProxyAddr != "" {
		t.Fatalf("关闭代理的渠道测试不应标注代理, 实际 %q", state.ProxyAddr)
	}

	// 仅开启开关未填专属地址: 标注系统代理地址。
	blank := model.Channel{ID: 23, Name: "label-test-system", Proxy: true}
	state = record(t, blank)
	defer cleanup(t, state.ID)
	assertProxyLabel(t, state.ProxyAddr, "127.0.0.1:7890", "pass", "面板测试系统代理")
	if !strings.Contains(state.ProxyAddr, "系统代理") {
		t.Fatalf("系统代理标注应带前缀, 实际 %q", state.ProxyAddr)
	}
}
