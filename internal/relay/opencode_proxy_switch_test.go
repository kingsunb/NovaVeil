package relay

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
)

type opencodeProxyProbe struct {
	server   *httptest.Server
	template string
	respond  func(account string) (int, []byte)

	mu       sync.Mutex
	accounts []string
}

func newOpencodeProxyProbe(t *testing.T, respond func(account string) (int, []byte)) *opencodeProxyProbe {
	t.Helper()
	probe := &opencodeProxyProbe{respond: respond}
	probe.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		account := proxyAccountFromHeader(r.Header.Get("Proxy-Authorization"))
		probe.mu.Lock()
		probe.accounts = append(probe.accounts, account)
		probe.mu.Unlock()
		status, body := probe.respond(account)
		if body == nil {
			body = []byte(`{"error":{"message":"forbidden"}}`)
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
		if request, err := http.ReadRequest(rw.Reader); err == nil {
			_ = request.Body.Close()
		}
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
			status, http.StatusText(status), len(body))
		_, _ = conn.Write(body)
	}))
	t.Cleanup(probe.server.Close)
	probe.template = "http://{account}:pass@" + strings.TrimPrefix(probe.server.URL, "http://")
	return probe
}

func proxyAccountFromHeader(value string) string {
	encoded, ok := strings.CutPrefix(value, "Basic ")
	if !ok {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	account, _, _ := strings.Cut(string(decoded), ":")
	return account
}

func (p *opencodeProxyProbe) seenAccounts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.accounts...)
}

func createOpencodeProxyTestChannel(t *testing.T, proxyTemplate string, keys ...model.ChannelKey) model.Channel {
	t.Helper()
	channel := model.Channel{
		Name:         integrationUniqueName("opencode-proxy-channel"),
		Type:         model.ChannelProviderOpenAI,
		Enabled:      true,
		BaseURL:      "http://opencode.test/zen",
		Key:          "public",
		Keys:         keys,
		Proxy:        true,
		ChannelProxy: &proxyTemplate,
		Models:       []model.ChannelModel{{Name: integrationUniqueName("opencode-proxy-model"), Source: model.ChannelModelSourceManual}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("创建 OpenCode 代理渠道失败: %v", err)
	}
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).Update("opencode_compat", true).Error; err != nil {
		t.Fatalf("标记 OpenCode 渠道失败: %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("刷新渠道缓存失败: %v", err)
	}
	channel, err := op.ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("读取 OpenCode 代理渠道失败: %v", err)
	}
	if !proxyNeedsOpencodeAccount(channel) {
		t.Fatalf("测试渠道应启用代理账号池: %+v", channel)
	}
	t.Cleanup(func() {
		_ = op.ChannelDel(channel.ID, context.Background())
	})
	return channel
}

func seedOpencodeProxyAccounts(t *testing.T, channelID int, accounts map[string]time.Time) {
	t.Helper()
	pool := &ocProxyPool{loaded: true, accounts: make(map[string]*ocProxyAccount, len(accounts))}
	for account, successAt := range accounts {
		pool.accounts[account] = &ocProxyAccount{account: account, lastSuccessAt: successAt, verified: true}
	}
	ocProxyPools.Store(channelID, pool)
	t.Cleanup(func() { ocProxyPools.Delete(channelID) })
}

func opencodeProxyPoolSnapshot(t *testing.T, channelID int) map[string]ocProxyAccount {
	t.Helper()
	value, ok := ocProxyPools.Load(channelID)
	if !ok {
		return nil
	}
	pool := value.(*ocProxyPool)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	snapshot := make(map[string]ocProxyAccount, len(pool.accounts))
	for account, state := range pool.accounts {
		snapshot[account] = *state
	}
	return snapshot
}

func assertOpencodeProxyKeysNotCooling(t *testing.T, channel model.Channel) {
	t.Helper()
	if len(channel.Keys) == 0 {
		t.Fatal("测试渠道应配置 Key")
	}
	for _, key := range channel.Keys {
		if channelKeyCooling(channel.ID, key.ID) {
			t.Fatalf("代理账号拒绝不应冷却 Key %q", key.Remark)
		}
	}
}

func assertOpencodeProxyCooldownDuration(t *testing.T, until time.Time, want time.Duration) {
	t.Helper()
	remaining := time.Until(until)
	if remaining < want-time.Minute || remaining > want+time.Minute {
		t.Fatalf("账号冷却剩余时长应约为 %v, 实际 %v", want, remaining)
	}
}

func assertOpencodeProxyPersistedCooldown(t *testing.T, channelID int, account string, want time.Duration) {
	t.Helper()
	var persisted model.OpencodeProxyAccount
	waitFor(t, 2*time.Second, func() bool {
		rows, err := op.LoadOpencodeProxyAccounts(channelID)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if row.Account == account {
				persisted = row
				return !row.CooldownUntil.IsZero()
			}
		}
		return false
	})
	assertOpencodeProxyCooldownDuration(t, persisted.CooldownUntil, want)
}

func opencodeProxyTestConfig() model.GroupRelayConfig {
	return model.GroupRelayConfig{
		MemberMaxAttempts:                     1,
		MemberRetryIntervalSeconds:            0.05,
		MemberNonStreamResponseTimeoutSeconds: 5,
		MemberStreamFirstEventTimeoutSeconds:  5,
		MemberCooldownSeconds:                 60,
		CooldownBackoffMultiplier:             2,
		CooldownMaxSeconds:                    120,
		AllCooldownRetryBaseSeconds:           0,
	}
}

func postOpencodeProxyRequest(t *testing.T, group model.Group, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	engine, path := newIntegrationEngine(llm.APIFormatOpenAIChatCompletion)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, group.Name)
	return postRelayJSON(t, engine, path, body, "", ctx)
}

func TestOpencodeProxy403RetriesAnotherAccount(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "bad-account" {
			return http.StatusForbidden, []byte("abusive traffic")
		}
		return http.StatusOK, chatCompletionBody("chatcmpl-opencode-retry", "ok-from-good-account", 3, 5)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	seedOpencodeProxyAccounts(t, channel.ID, map[string]time.Time{
		"bad-account":  time.Now(),
		"good-account": time.Now().Add(-time.Hour),
	})
	group := createIntegrationGroup(t, "opencode-proxy-retry", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	recorder := postOpencodeProxyRequest(t, group, nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-from-good-account") {
		t.Fatalf("换号后应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := probe.seenAccounts(); len(got) != 2 || got[0] != "bad-account" || got[1] != "good-account" {
		t.Fatalf("应先用坏账号再换好账号, 实际 %v", got)
	}

	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	cooldown := 0
	for account, state := range pool {
		if !state.cooldownUntil.IsZero() {
			cooldown++
			if account != "bad-account" {
				t.Fatalf("只有坏账号应冷却, 实际账号 %s", account)
			}
		}
	}
	if cooldown != 1 {
		t.Fatalf("账号池应恰有 1 个冷却账号, 实际 %d: %+v", cooldown, pool)
	}
	assertOpencodeProxyCooldownDuration(t, pool["bad-account"].cooldownUntil, ocProxyAbuseCooldown)

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; cooling {
		t.Fatalf("换号重试不应冷却成员, 实际 %v", route.Cooldowns)
	}
	if route.Levels[itemID] != 0 || route.HalfOpens[itemID] != 0 {
		t.Fatalf("换号重试不应改变成员健康度, 实际 levels=%v half_opens=%v", route.Levels, route.HalfOpens)
	}
	if route.CurrentItemID != itemID {
		t.Fatalf("成功成员应保持当前路由, 实际 %d/%d", route.CurrentItemID, itemID)
	}
	state := requestStateOf(t, nextRequestID()-1)
	if state.Status != StatusSuccess || len(state.Attempts) != 2 || state.Attempts[0].Outcome != AttemptFailed || state.Attempts[1].Outcome != AttemptSuccess {
		t.Fatalf("应记录失败后成功两轮, 实际 %s/%+v", state.Status, state.Attempts)
	}
}

func TestOpencodeProxy429CoolsAccount24hAndRetriesAnother(t *testing.T) {
	setupFailoverTest(t)
	resetChannelKeyHealth()
	t.Cleanup(resetChannelKeyHealth)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "bad-account" {
			return http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limited"}}`)
		}
		return http.StatusOK, chatCompletionBody("chatcmpl-opencode-429-retry", "ok-from-next-account", 3, 5)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template,
		model.ChannelKey{Key: "single-alpha", Remark: "alpha"},
	)
	seedOpencodeProxyAccounts(t, channel.ID, map[string]time.Time{
		"bad-account":  time.Now(),
		"good-account": time.Now().Add(-time.Hour),
	})
	group := createIntegrationGroup(t, "opencode-proxy-429-retry", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	expectedID := nextRequestID()
	recorder := postOpencodeProxyRequest(t, group, nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-from-next-account") {
		t.Fatalf("429 换号后应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := probe.seenAccounts(); len(got) != 2 || got[0] != "bad-account" || got[1] != "good-account" {
		t.Fatalf("应先用被限速账号再换好账号, 实际 %v", got)
	}

	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	bad, ok := pool["bad-account"]
	if !ok {
		t.Fatalf("被限速账号应留在池内, 实际 %+v", pool)
	}
	assertOpencodeProxyCooldownDuration(t, bad.cooldownUntil, ocProxyRateLimitedCooldown)
	if state, ok := pool["good-account"]; !ok || !state.cooldownUntil.IsZero() {
		t.Fatalf("成功账号不应冷却, 实际 %+v", state)
	}
	assertOpencodeProxyKeysNotCooling(t, channel)
	assertOpencodeProxyPersistedCooldown(t, channel.ID, "bad-account", ocProxyRateLimitedCooldown)

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; cooling {
		t.Fatalf("429 换号不应冷却成员, 实际 %v", route.Cooldowns)
	}
	if route.Levels[itemID] != 0 || route.HalfOpens[itemID] != 0 {
		t.Fatalf("429 换号不应改变成员健康度, 实际 levels=%v half_opens=%v", route.Levels, route.HalfOpens)
	}
	if route.CurrentItemID != itemID {
		t.Fatalf("成功成员应保持当前路由, 实际 %d/%d", route.CurrentItemID, itemID)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess || len(state.Attempts) != 2 || state.Attempts[0].Outcome != AttemptFailed || state.Attempts[1].Outcome != AttemptSuccess {
		t.Fatalf("应记录失败后成功两轮, 实际 %s/%+v", state.Status, state.Attempts)
	}
}

func TestOpencodeProxy429StopsAtRetryLimit(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limited"}}`)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	t.Cleanup(func() { ocProxyPools.Delete(channel.ID) })
	config := opencodeProxyTestConfig()
	config.MemberMaxAttempts = 3
	config.MemberRetryIntervalSeconds = 0
	group := createIntegrationGroup(t, "opencode-proxy-429-limit", config, integrationLeafItem(t, channel, channel.Models[0].Name))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	expectedID := nextRequestID()
	recorder := postOpencodeProxyRequest(t, group, ctx)
	if recorder.Body.Len() != 0 {
		t.Fatalf("持续 429 不应泄漏响应体, 实际 %s", recorder.Body.String())
	}
	seen := probe.seenAccounts()
	if len(seen) < ocProxyMaxRetries+1 {
		t.Fatalf("应至少完成 %d 次换号尝试, 实际 %d: %v", ocProxyMaxRetries+1, len(seen), seen)
	}
	unique := make(map[string]struct{}, len(seen))
	for _, account := range seen {
		unique[account] = struct{}{}
	}
	if len(unique) != len(seen) {
		t.Fatalf("429 冷却账号不应被复用, 实际 %v", seen)
	}
	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	if len(pool) != len(unique) {
		t.Fatalf("尝试过的账号应全部入冷却槽, 实际 %+v", pool)
	}
	for account, state := range pool {
		if !state.cooldownUntil.After(time.Now()) {
			t.Fatalf("账号 %s 应处于冷却中, 实际 %+v", account, state)
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
		return err == nil && len(rows) == len(pool)
	})

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; !cooling {
		t.Fatalf("超过换号上限后应回落成员失败路径, 实际 %v", route.Cooldowns)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusCanceled && state.Status != StatusFailed {
		t.Fatalf("请求应以可预期失败终态结束, 实际 %s(%s)", state.Status, state.Error)
	}
}

func TestOpencodeProxy429MultiKeySwitchesAccountWithoutKeyCooldown(t *testing.T) {
	setupFailoverTest(t)
	resetChannelKeyHealth()
	t.Cleanup(resetChannelKeyHealth)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "bad-account" {
			return http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limited"}}`)
		}
		return http.StatusOK, chatCompletionBody("chatcmpl-opencode-429-multikey", "ok-429-multikey-account-switch", 3, 5)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template,
		model.ChannelKey{Key: "multikey-alpha", Remark: "alpha"},
		model.ChannelKey{Key: "multikey-beta", Remark: "beta"},
	)
	seedOpencodeProxyAccounts(t, channel.ID, map[string]time.Time{
		"bad-account":  time.Now(),
		"good-account": time.Now().Add(-time.Hour),
	})
	group := createIntegrationGroup(t, "opencode-proxy-429-multikey-retry", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	expectedID := nextRequestID()
	recorder := postOpencodeProxyRequest(t, group, nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-429-multikey-account-switch") {
		t.Fatalf("多 Key 渠道 429 换号后应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := probe.seenAccounts(); len(got) != 2 || got[0] != "bad-account" || got[1] != "good-account" {
		t.Fatalf("应先用被限速账号再换好账号, 实际 %v", got)
	}

	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	bad, ok := pool["bad-account"]
	if !ok {
		t.Fatalf("被限速账号应留在池内, 实际 %+v", pool)
	}
	assertOpencodeProxyCooldownDuration(t, bad.cooldownUntil, ocProxyRateLimitedCooldown)
	assertOpencodeProxyKeysNotCooling(t, channel)

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; cooling {
		t.Fatalf("429 换号不应冷却成员, 实际 %v", route.Cooldowns)
	}
	if route.Levels[itemID] != 0 || route.HalfOpens[itemID] != 0 {
		t.Fatalf("429 换号不应改变成员健康度, 实际 levels=%v half_opens=%v", route.Levels, route.HalfOpens)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess || len(state.Attempts) != 2 {
		t.Fatalf("应记录一次换号后成功, 实际 %s/%+v", state.Status, state.Attempts)
	}
}

func TestOpencodeProxy403MultiKeySwitchesAccountWithoutKeyCooldown(t *testing.T) {
	setupFailoverTest(t)
	resetChannelKeyHealth()
	t.Cleanup(resetChannelKeyHealth)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "bad-account" {
			return http.StatusForbidden, []byte("abusive traffic")
		}
		return http.StatusOK, chatCompletionBody("chatcmpl-opencode-multikey", "ok-multikey-account-switch", 3, 5)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template,
		model.ChannelKey{Key: "multikey-alpha", Remark: "alpha"},
		model.ChannelKey{Key: "multikey-beta", Remark: "beta"},
	)
	seedOpencodeProxyAccounts(t, channel.ID, map[string]time.Time{
		"bad-account":  time.Now(),
		"good-account": time.Now().Add(-time.Hour),
	})
	group := createIntegrationGroup(t, "opencode-proxy-multikey-retry", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	expectedID := nextRequestID()
	recorder := postOpencodeProxyRequest(t, group, nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-multikey-account-switch") {
		t.Fatalf("多 Key 渠道换号后应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := probe.seenAccounts(); len(got) != 2 || got[0] != "bad-account" || got[1] != "good-account" {
		t.Fatalf("应先用坏账号再换好账号, 实际 %v", got)
	}

	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	cooldown := 0
	for account, state := range pool {
		if !state.cooldownUntil.IsZero() {
			cooldown++
			if account != "bad-account" {
				t.Fatalf("只有坏账号应冷却, 实际账号 %s", account)
			}
		}
	}
	if cooldown != 1 {
		t.Fatalf("账号池应恰有 1 个冷却账号, 实际 %d: %+v", cooldown, pool)
	}
	assertOpencodeProxyKeysNotCooling(t, channel)

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; cooling {
		t.Fatalf("换号重试不应冷却成员, 实际 %v", route.Cooldowns)
	}
	if route.Levels[itemID] != 0 || route.HalfOpens[itemID] != 0 {
		t.Fatalf("换号重试不应改变成员健康度, 实际 levels=%v half_opens=%v", route.Levels, route.HalfOpens)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusSuccess || len(state.Attempts) != 2 {
		t.Fatalf("应记录一次换号后成功, 实际 %s/%+v", state.Status, state.Attempts)
	}
	if state.Attempts[0].Outcome != AttemptFailed || state.Attempts[1].Outcome != AttemptSuccess {
		t.Fatalf("尝试轨迹应为失败后成功, 实际 %+v", state.Attempts)
	}
	if state.Attempts[0].MemberID != itemID || state.Attempts[1].MemberID != itemID {
		t.Fatalf("换号应保持同一成员, 实际 %d -> %d", state.Attempts[0].MemberID, state.Attempts[1].MemberID)
	}
}

func TestOpencodeProxy403StopsAtRetryLimit(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusForbidden, []byte("abusive traffic")
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	t.Cleanup(func() { ocProxyPools.Delete(channel.ID) })
	config := opencodeProxyTestConfig()
	config.MemberMaxAttempts = 3
	config.MemberRetryIntervalSeconds = 0
	group := createIntegrationGroup(t, "opencode-proxy-limit", config, integrationLeafItem(t, channel, channel.Models[0].Name))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	recorder := postOpencodeProxyRequest(t, group, ctx)
	if recorder.Body.Len() != 0 {
		t.Fatalf("持续 403 不应泄漏响应体, 实际 %s", recorder.Body.String())
	}
	seen := probe.seenAccounts()
	if len(seen) < ocProxyMaxRetries+1 {
		t.Fatalf("应至少完成 %d 次换号尝试, 实际 %d: %v", ocProxyMaxRetries+1, len(seen), seen)
	}
	unique := make(map[string]struct{}, len(seen))
	for _, account := range seen {
		unique[account] = struct{}{}
	}
	if len(unique) != len(seen) {
		t.Fatalf("403 冷却账号不应被复用, 实际 %v", seen)
	}
	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	if len(pool) != len(unique) {
		t.Fatalf("尝试过的账号应全部入冷却槽, 实际 %+v", pool)
	}
	for account, state := range pool {
		if !state.cooldownUntil.After(time.Now()) {
			t.Fatalf("账号 %s 应处于冷却中, 实际 %+v", account, state)
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
		return err == nil && len(rows) == len(pool)
	})

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; !cooling {
		t.Fatalf("超过换号上限后应回落成员失败路径, 实际 %v", route.Cooldowns)
	}
	state := requestStateOf(t, nextRequestID()-1)
	if state.Status != StatusCanceled && state.Status != StatusFailed {
		t.Fatalf("请求应以可预期失败终态结束, 实际 %s(%s)", state.Status, state.Error)
	}
}

func TestOpencodeProxy403MultiKeyStopsAtRetryLimitWithoutKeyCooldown(t *testing.T) {
	setupFailoverTest(t)
	resetChannelKeyHealth()
	t.Cleanup(resetChannelKeyHealth)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusForbidden, []byte("abusive traffic")
	})
	channel := createOpencodeProxyTestChannel(t, probe.template,
		model.ChannelKey{Key: "multikey-alpha", Remark: "alpha"},
		model.ChannelKey{Key: "multikey-beta", Remark: "beta"},
	)
	config := opencodeProxyTestConfig()
	config.MemberMaxAttempts = 3
	config.MemberRetryIntervalSeconds = 0
	group := createIntegrationGroup(t, "opencode-proxy-multikey-limit", config, integrationLeafItem(t, channel, channel.Models[0].Name))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	expectedID := nextRequestID()
	recorder := postOpencodeProxyRequest(t, group, ctx)
	if recorder.Body.Len() != 0 {
		t.Fatalf("持续 403 不应泄漏响应体, 实际 %s", recorder.Body.String())
	}
	seen := probe.seenAccounts()
	if len(seen) < ocProxyMaxRetries+1 {
		t.Fatalf("应至少完成 %d 次换号尝试, 实际 %d: %v", ocProxyMaxRetries+1, len(seen), seen)
	}
	unique := make(map[string]struct{}, len(seen))
	for _, account := range seen {
		unique[account] = struct{}{}
	}
	if len(unique) != len(seen) {
		t.Fatalf("403 冷却账号不应被复用, 实际 %v", seen)
	}
	pool := opencodeProxyPoolSnapshot(t, channel.ID)
	if len(pool) != len(unique) {
		t.Fatalf("尝试过的账号应全部入冷却槽, 实际 %+v", pool)
	}
	for account, state := range pool {
		if !state.cooldownUntil.After(time.Now()) {
			t.Fatalf("账号 %s 应处于冷却中, 实际 %+v", account, state)
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
		return err == nil && len(rows) == len(pool)
	})
	assertOpencodeProxyKeysNotCooling(t, channel)

	itemID := group.Items[0].ID
	route := routeSnapshot(t, group.ID)
	if _, cooling := route.Cooldowns[itemID]; !cooling {
		t.Fatalf("超过换号上限后应回落成员失败路径, 实际 %v", route.Cooldowns)
	}
	state := requestStateOf(t, expectedID)
	if state.Status != StatusCanceled && state.Status != StatusFailed {
		t.Fatalf("请求应以可预期失败终态结束, 实际 %s(%s)", state.Status, state.Error)
	}
}

func TestOpencodeProxy403ConcurrentRetry(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "bad-account" {
			return http.StatusForbidden, []byte("abusive traffic")
		}
		return http.StatusOK, chatCompletionBody("chatcmpl-opencode-concurrent", "ok-concurrent", 2, 3)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	seedOpencodeProxyAccounts(t, channel.ID, map[string]time.Time{
		"bad-account":  time.Now(),
		"good-account": time.Now().Add(-time.Hour),
	})
	group := createIntegrationGroup(t, "opencode-proxy-concurrent", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	const requests = 4
	var wg sync.WaitGroup
	errors := make(chan string, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := postOpencodeProxyRequest(t, group, nil)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok-concurrent") {
				errors <- fmt.Sprintf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("并发请求应全部成功: %s", err)
	}
}
