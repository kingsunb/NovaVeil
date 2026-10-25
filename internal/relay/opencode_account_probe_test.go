package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
)

func probeChatOKBody() []byte {
	return []byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}}`)
}

// TestProbeOpencodeAccountsClassifiesAndSeedsPool 验证批量体检的三类结论与池回填:
// 健康→verified 入池优先复用; 403→24h 冷却; 其余非 2xx 归为失败不冷却。

// ocProbePinQuestion 测试内锁定单一体检问题, 保持端到端断言确定性。
func ocProbePinQuestion(t *testing.T) {
	t.Helper()
	questions := ocProbeQuestions
	ocProbeQuestions = []string{defaultTestMessage}
	t.Cleanup(func() { ocProbeQuestions = questions })
}

func TestProbeOpencodeAccountsClassifiesAndSeedsPool(t *testing.T) {
	ocProbePinQuestion(t)
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if strings.HasSuffix(account, "bad") {
			return http.StatusForbidden, []byte(`{"error":{"message":"FreeTierError"}}`)
		}
		if strings.HasSuffix(account, "down") {
			return http.StatusBadGateway, []byte(`{"error":{"message":"upstream down"}}`)
		}
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)

	report, err := ProbeOpencodeAccounts(context.Background(), channel.ID,
		[]string{"user_good", "user_good", "user_bad", "user_down"}, 0, 4, 0, "")
	if err != nil {
		t.Fatalf("批量体检失败: %v", err)
	}
	if report.Summary.Total != 3 || report.Summary.Healthy != 1 ||
		report.Summary.Rejected != 1 || report.Summary.Failed != 1 {
		t.Fatalf("汇总异常: %+v", report.Summary)
	}
	byAccount := make(map[string]AccountProbeResult, len(report.Results))
	for _, result := range report.Results {
		byAccount[result.Account] = result
	}
	good, ok := byAccount["user_good"]
	if !ok || good.Outcome != ocProbeOutcomeHealthy || good.Status != http.StatusOK {
		t.Fatalf("user_good 应为 healthy/200: %+v", good)
	}
	bad := byAccount["user_bad"]
	if bad.Outcome != ocProbeOutcomeRejected || bad.Status != http.StatusForbidden {
		t.Fatalf("user_bad 应为 rejected/403: %+v", bad)
	}
	down := byAccount["user_down"]
	if down.Outcome != ocProbeOutcomeError || down.Status != http.StatusBadGateway {
		t.Fatalf("user_down 应为 error/502: %+v", down)
	}

	snapshot := opencodeProxyPoolSnapshot(t, channel.ID)
	goodState, ok := snapshot["user_good"]
	if !ok || !goodState.verified || !goodState.cooldownUntil.IsZero() {
		t.Fatalf("user_good 应 verified 且无冷却: %+v", goodState)
	}
	badState, ok := snapshot["user_bad"]
	if !ok || !time.Now().Before(badState.cooldownUntil) {
		t.Fatalf("user_bad 应进入冷却: %+v", badState)
	}
	if downState, exists := snapshot["user_down"]; exists {
		// 生成账号落账后是池的普通候选: 基础设施失败只影响授信与冷却, 不应被标记 verified。
		if downState.verified || !downState.cooldownUntil.IsZero() {
			t.Fatalf("user_down 不应被授信或冷却: %+v", downState)
		}
	}
}

// TestProbeOpencodeAccountsGenerateAndValidate 验证随机生成模式与入参校验。
func TestProbeOpencodeAccountsGenerateAndValidate(t *testing.T) {
	ocProbePinQuestion(t)
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)

	report, err := ProbeOpencodeAccounts(context.Background(), channel.ID, nil, 3, 3, 0, "")
	if err != nil {
		t.Fatalf("生成模式体检失败: %v", err)
	}
	if report.Summary.Total != 3 || report.Summary.Healthy != 3 {
		t.Fatalf("生成 3 个账号应全部健康: %+v", report.Summary)
	}
	pattern := regexp.MustCompile(`^opencode[0-9a-f]{8}$`)
	seen := make(map[string]bool, 3)
	for _, result := range report.Results {
		if !pattern.MatchString(result.Account) {
			t.Fatalf("生成账号格式异常: %q", result.Account)
		}
		if seen[result.Account] {
			t.Fatalf("生成账号重复: %q", result.Account)
		}
		seen[result.Account] = true
	}

	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID, []string{"bad account!"}, 0, 0, 0, ""); err == nil {
		t.Fatal("含空格账号应被拒绝")
	}
	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID, make([]string, ocProbeMaxAccounts+1), 0, 0, 0, ""); err == nil {
		t.Fatal("超过单次上限应被拒绝")
	}

	bare := createIntegrationChannel(t, "account-probe-no-proxy", model.ChannelProviderOpenAI, "http://opencode.test/v1", "plain-model")
	if _, err := ProbeOpencodeAccounts(context.Background(), bare.ID, nil, 0, 0, 0, ""); err == nil {
		t.Fatal("非 {account} 代理渠道应被拒绝")
	}
}

// TestOpencodeAccountLedgerStates 验证台账状态派生、筛选与删除:
// 可用=健康探测; 未测试=仅录入; 不可用=冷却中或被上游拒绝; 网络错误=基础设施故障可复测。
func TestOpencodeAccountLedgerStates(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	now := time.Now()

	if err := op.UpsertOpencodeGeneratedAccounts(channel.ID, []string{"acct_ok", "acct_untested"}); err != nil {
		t.Fatalf("落账失败: %v", err)
	}
	if err := op.SaveOpencodeProbeResult(channel.ID, "acct_ok", model.OpencodeAccountOutcomeHealthy, 200, now); err != nil {
		t.Fatalf("写探测结论失败: %v", err)
	}
	if err := op.SaveOpencodeProxyCooldown(channel.ID, "acct_cooling", now.Add(time.Hour)); err != nil {
		t.Fatalf("写冷却失败: %v", err)
	}
	if err := op.SaveOpencodeProbeResult(channel.ID, "acct_rejected", model.OpencodeAccountOutcomeRejected, 403, now); err != nil {
		t.Fatalf("写探测结论失败: %v", err)
	}
	if err := op.SaveOpencodeProbeResult(channel.ID, "acct_neterr", model.OpencodeAccountOutcomeError, 502, now); err != nil {
		t.Fatalf("写探测结论失败: %v", err)
	}

	ledger, err := ListOpencodeAccountStates(channel.ID, "all")
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if ledger.Usable != 1 || ledger.Unusable != 2 || ledger.Untested != 1 || ledger.ProbeError != 1 {
		t.Fatalf("状态计数异常: %+v", ledger)
	}
	states := make(map[string]string, len(ledger.Items))
	for _, item := range ledger.Items {
		states[item.Account] = item.State
	}
	if states["acct_ok"] != "usable" || states["acct_cooling"] != "unusable" ||
		states["acct_untested"] != "untested" || states["acct_rejected"] != "unusable" ||
		states["acct_neterr"] != "error" {
		t.Fatalf("状态派生异常: %+v", states)
	}

	filtered, err := ListOpencodeAccountStates(channel.ID, "error")
	if err != nil {
		t.Fatalf("筛选失败: %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Account != "acct_neterr" {
		t.Fatalf("error 筛选异常: %+v", filtered.Items)
	}

	deleted, err := DeleteOpencodeAccountsFromLedger(channel.ID, []string{"acct_untested"})
	if err != nil || deleted != 1 {
		t.Fatalf("删除应移除 1 行: deleted=%d err=%v", deleted, err)
	}
	if ledger2, _ := ListOpencodeAccountStates(channel.ID, "all"); ledger2.Total != 4 {
		t.Fatalf("删除后台账应剩 4 行: %d", ledger2.Total)
	}

	// 成功统计: 同一自然日多次成功只累计次数不增天数, 跨自然日天数 +1。
	if err := op.SaveOpencodeProxySuccess(channel.ID, "acct_ok", now); err != nil {
		t.Fatalf("写成功记录失败: %v", err)
	}
	if err := op.SaveOpencodeProxySuccess(channel.ID, "acct_ok", now.Add(time.Minute)); err != nil {
		t.Fatalf("写成功记录失败: %v", err)
	}
	if err := op.SaveOpencodeProxySuccess(channel.ID, "acct_ok", now.Add(25*time.Hour)); err != nil {
		t.Fatalf("写跨天成功记录失败: %v", err)
	}
	rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	for _, row := range rows {
		if row.Account != "acct_ok" {
			continue
		}
		if row.SuccessCount != 3 || row.SuccessDays != 2 {
			t.Fatalf("成功统计异常: count=%d days=%d", row.SuccessCount, row.SuccessDays)
		}
	}
}

// ocProbeExpectedV1Tools / ocProbeExpectedV2Tools 是官方客户端实抓的工具名与顺序。
var (
	ocProbeExpectedV1Tools = []string{"bash", "edit", "glob", "grep", "read", "skill", "task", "todowrite", "webfetch", "websearch", "write"}
	ocProbeExpectedV2Tools = []string{"edit", "glob", "grep", "question", "read", "shell", "skill", "subagent", "webfetch", "websearch", "write", "execute"}
)

// topLevelJSONKeys 按出现顺序返回 JSON 对象的顶层键。
func topLevelJSONKeys(t *testing.T, body []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("解析 JSON 起始符失败: %v", err)
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("解析 JSON 键失败: %v", err)
		}
		keys = append(keys, token.(string))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("解析 JSON 值失败: %v", err)
		}
	}
	return keys
}

// assertClientFormBody 断言体检正文与官方客户端实抓形态一致: 键序、系统提示动态段、
// 全量工具、流式参数与测试消息。
func assertClientFormBody(t *testing.T, body []byte, lane string, modelName, displayName string) {
	t.Helper()
	wantKeys := []string{"model", "messages", "tools", "stream", "stream_options", "max_completion_tokens"}
	if lane == "v1" {
		wantKeys = []string{"model", "max_tokens", "messages", "tools", "tool_choice", "stream", "stream_options"}
	}
	if keys := topLevelJSONKeys(t, body); strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("%s 档键序异常: %v", lane, keys)
	}
	if got := gjson.GetBytes(body, "model").String(); got != modelName {
		t.Fatalf("model 异常: %q", got)
	}
	system := gjson.GetBytes(body, "messages.0.content").String()
	if lane == "v1" {
		if !strings.Contains(system, "You are opencode") ||
			!strings.Contains(system, "the model named "+modelName+".") ||
			!strings.Contains(system, "opencode/"+modelName) {
			t.Fatalf("v1 系统提示缺少模型动态段")
		}
	} else {
		if !strings.Contains(system, "You are an AI agent running in OpenCode") ||
			!strings.Contains(system, "- Name: "+displayName) ||
			!strings.Contains(system, "- Model ID: "+modelName) {
			t.Fatalf("v2 系统提示缺少模型动态段")
		}
	}
	if !regexp.MustCompile(`Today's date: (Sun|Mon|Tue|Wed|Thu|Fri|Sat) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{1,2} \d{4}`).MatchString(system) {
		t.Fatalf("系统提示缺少当日日期: %q", system[len(system)-300:])
	}
	if strings.Contains(system, "{{") {
		t.Fatalf("系统提示存在未替换标记")
	}
	if role := gjson.GetBytes(body, "messages.0.role").String(); role != "system" {
		t.Fatalf("messages.0.role 异常: %q", role)
	}
	if content := gjson.GetBytes(body, "messages.1.content").String(); content != defaultTestMessage {
		t.Fatalf("测试消息异常: %q", content)
	}
	if role := gjson.GetBytes(body, "messages.1.role").String(); role != "user" {
		t.Fatalf("messages.1.role 异常: %q", role)
	}
	wantTools := ocProbeExpectedV2Tools
	if lane == "v1" {
		wantTools = ocProbeExpectedV1Tools
	}
	var gotTools []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		gotTools = append(gotTools, tool.Get("function.name").String())
	}
	if strings.Join(gotTools, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("%s 档工具集异常: %v", lane, gotTools)
	}
	if !gjson.GetBytes(body, "stream").Bool() || !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("流式参数异常")
	}
	limitField := "max_completion_tokens"
	if lane == "v1" {
		limitField = "max_tokens"
		if got := gjson.GetBytes(body, "tool_choice").String(); got != "auto" {
			t.Fatalf("v1 档 tool_choice 异常: %q", got)
		}
	}
	if got := gjson.GetBytes(body, limitField).Int(); got != 32000 {
		t.Fatalf("%s 异常: %d", limitField, got)
	}
	phrase := "on Linux using bash"
	if runtime.GOOS == "windows" {
		phrase = "on Windows using bash"
	}
	if !strings.Contains(string(body), phrase) && lane == "v2" {
		t.Fatalf("shell 工具描述平台口径异常")
	}
}

// TestOcProbeRawRequestMatchesRealClient 验证免费档体检请求与官方客户端实抓形态一致。
func TestOcProbeRawRequestMatchesRealClient(t *testing.T) {
	cases := []struct {
		lane    string
		baseURL string
	}{
		{"v1", "http://opencode.test/zen"},
		{"v2", "http://opencode.test/inference"},
	}
	for _, c := range cases {
		channel := model.Channel{
			Type:           model.ChannelProviderOpenAI,
			BaseURL:        c.baseURL,
			IsFree:         true,
			OpencodeCompat: true,
			Models:         []model.ChannelModel{{Name: "probe-free-model"}},
		}
		raw, err := ocProbeRawRequest(llm.APIFormatOpenAIChatCompletion, channel, "probe-free-model", "Probe Free Model", defaultTestMessage, 0)
		if err != nil {
			t.Fatalf("%s 档构造失败: %v", c.lane, err)
		}
		if raw.Path != "/v1/chat/completions" {
			t.Fatalf("%s 档路径异常: %q", c.lane, raw.Path)
		}
		if c.lane == "v2" {
			if got := raw.Headers.Get(opencodeProjectHeader); !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(got) {
				t.Fatalf("v2 档项目号形态异常: %q", got)
			}
		} else if raw.Headers.Get(opencodeProjectHeader) != "" {
			t.Fatal("v1 档不应携带项目号头")
		}
		assertClientFormBody(t, raw.Body, c.lane, "probe-free-model", "Probe Free Model")
	}

	// 非免费模型回退极小请求, 模型级输出上限在免费档透传。
	channel := model.Channel{Type: model.ChannelProviderOpenAI, BaseURL: "http://opencode.test/inference", Models: []model.ChannelModel{{Name: "paid-model", MaxOutput: 12345}}}
	raw, err := ocProbeRawRequest(llm.APIFormatOpenAIChatCompletion, channel, "paid-model", "", defaultTestMessage, 12345)
	if err != nil {
		t.Fatalf("付费模型构造失败: %v", err)
	}
	if got := gjson.GetBytes(raw.Body, "messages.#").Int(); got != 1 {
		t.Fatalf("非免费模型应回退极小请求: %d 条消息", got)
	}
	freeChannel := model.Channel{Type: model.ChannelProviderOpenAI, BaseURL: "http://opencode.test/inference", IsFree: true, Models: []model.ChannelModel{{Name: "probe-free-model", MaxOutput: 9999}}}
	raw, err = ocProbeRawRequest(llm.APIFormatOpenAIChatCompletion, freeChannel, "probe-free-model", "", defaultTestMessage, 9999)
	if err != nil {
		t.Fatalf("免费模型构造失败: %v", err)
	}
	if got := gjson.GetBytes(raw.Body, "max_completion_tokens").Int(); got != 9999 {
		t.Fatalf("模型级输出上限应透传: %d", got)
	}
}

// TestOcProbeClientBodyShapingIdempotent 验证免费档整形对实抓模板幂等:
// 出站正文不会被整形层二次改写, 与上游逐字节门禁的口径锁定。
func TestOcProbeClientBodyShapingIdempotent(t *testing.T) {
	v1Body, err := ocProbeClientBody("v1", "probe-free-model", "", defaultTestMessage, 0)
	if err != nil {
		t.Fatalf("v1 模板构造失败: %v", err)
	}
	if shaped, _, _ := shapeOpencodeFreeBody(v1Body, true, model.UpstreamProtocolChat); string(shaped) != string(v1Body) {
		t.Fatal("v1 档整形不应改写体检模板")
	}
	v2Body, err := ocProbeClientBody("v2", "probe-free-model", "", defaultTestMessage, 0)
	if err != nil {
		t.Fatalf("v2 模板构造失败: %v", err)
	}
	if shaped, _, _ := shapeOpencodeFreeBody(v2Body, false, model.UpstreamProtocolChat); string(shaped) != string(v2Body) {
		t.Fatal("v2 档整形不应改写体检模板")
	}
}

// TestProbeOpencodeAccountsSendsClientFormAndUniqueSessions 端到端验证 v2 免费档体检:
// 出站正文为官方客户端完整形态, 且每个账号的会话号互不相同。
func TestProbeOpencodeAccountsSendsClientFormAndUniqueSessions(t *testing.T) {
	ocProbePinQuestion(t)
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", channel.ID).
		Updates(map[string]any{"base_url": "http://opencode.test/inference", "is_free": true}).Error; err != nil {
		t.Fatalf("切换 v2 免费档失败: %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("刷新渠道缓存失败: %v", err)
	}
	channel, err := op.ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	modelName := channel.Models[0].Name
	if !isOpencodeFreeModel(channel, modelName) {
		t.Fatalf("测试渠道应为免费档: %+v", channel)
	}
	originalCatalog := opencodeProbeCatalog
	opencodeProbeCatalog = func(context.Context, model.Channel) (map[string]helper.OpencodeModelInfo, error) {
		return map[string]helper.OpencodeModelInfo{modelName: {Name: "Probe Free Model"}}, nil
	}
	t.Cleanup(func() { opencodeProbeCatalog = originalCatalog })

	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID,
		[]string{"user_a", "user_b", "user_c"}, 0, 3, 0, modelName); err != nil {
		t.Fatalf("批量体检失败: %v", err)
	}
	requests := probe.proxiedRequests()
	if len(requests) != 3 {
		t.Fatalf("应有 3 条出站请求: %d", len(requests))
	}
	sessions := make(map[string]bool, 3)
	for _, request := range requests {
		if request.Method != http.MethodPost || request.Path != "/inference/openai/v1/chat/completions" {
			t.Fatalf("出站请求行异常: %s %s", request.Method, request.Path)
		}
		session := request.Header.Get("x-opencode-session")
		if !regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(session) {
			t.Fatalf("会话号形态异常: %q", session)
		}
		if sessions[session] {
			t.Fatalf("会话号重复: %q", session)
		}
		sessions[session] = true
		if request.Header.Get("x-opencode-session-id") != session ||
			request.Header.Get("x-session-affinity") != session ||
			request.Header.Get("X-Session-Id") != session {
			t.Fatalf("会话镜像头异常: %q", session)
		}
		if !strings.HasPrefix(request.Header.Get("User-Agent"), "opencode/") {
			t.Fatalf("User-Agent 异常: %q", request.Header.Get("User-Agent"))
		}
		if request.Header.Get("x-opencode-client") != "cli" {
			t.Fatalf("x-opencode-client 异常: %q", request.Header.Get("x-opencode-client"))
		}
		if request.Header.Get("x-opencode-project") != ocProbeProjectID {
			t.Fatalf("项目号异常: %q", request.Header.Get("x-opencode-project"))
		}
		if request.Header.Get("traceparent") == "" || request.Header.Get("b3") == "" {
			t.Fatal("v2 免费档应携带追踪头")
		}
		assertClientFormBody(t, request.Body, "v2", modelName, "Probe Free Model")
	}
}

// waitForOpencodeAccountCounters 轮询等待账号行落库且三类拆分计数达到期望值。
func waitForOpencodeAccountCounters(t *testing.T, channelID int, account string, realSuccess, realFailure, probeSuccess int64) model.OpencodeProxyAccount {
	t.Helper()
	var persisted model.OpencodeProxyAccount
	waitFor(t, 2*time.Second, func() bool {
		rows, err := op.LoadOpencodeProxyAccounts(channelID)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if row.Account != account {
				continue
			}
			persisted = row
			return row.RealSuccessCount == realSuccess && row.RealFailureCount == realFailure && row.ProbeSuccessCount == probeSuccess
		}
		return false
	})
	return persisted
}

// TestProbeOpencodeAccountsRecordsProbeSuccessCounter 验证体检成功只计体检计数,
// 不与真实转发成功混账。
func TestProbeOpencodeAccountsRecordsProbeSuccessCounter(t *testing.T) {
	ocProbePinQuestion(t)
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(account string) (int, []byte) {
		if account == "user_bad" {
			return http.StatusForbidden, []byte(`{"error":{"message":"FreeTierError"}}`)
		}
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)

	if _, err := ProbeOpencodeAccounts(context.Background(), channel.ID,
		[]string{"user_good", "user_bad"}, 0, 2, 0, ""); err != nil {
		t.Fatalf("批量体检失败: %v", err)
	}
	row := waitForOpencodeAccountCounters(t, channel.ID, "user_good", 0, 0, 1)
	if row.SuccessCount != 1 {
		t.Fatalf("总成功应为 1: %+v", row)
	}
	waitForOpencodeAccountCounters(t, channel.ID, "user_bad", 0, 0, 0)
}

// TestOpencodeLedgerHistoricFilterAndSplitCounters 验证「历史可用」筛选口径
// (累计成功过且不受伪造最近成功时间干扰)、默认排序与成功清冷却。
func TestOpencodeLedgerHistoricFilterAndSplitCounters(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, probeChatOKBody()
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	now := time.Now()

	// good_then_cool: 先真实成功再冷却, 是「历史可用」的冷却好号。
	if err := op.SaveOpencodeProxySuccess(channel.ID, "good_then_cool", now); err != nil {
		t.Fatalf("写成功记录失败: %v", err)
	}
	if err := op.SaveOpencodeProxyProbeSuccess(channel.ID, "good_then_cool", now); err != nil {
		t.Fatalf("写体检成功失败: %v", err)
	}
	if err := op.SaveOpencodeProxyRealFailure(channel.ID, "good_then_cool"); err != nil {
		t.Fatalf("写失败记录失败: %v", err)
	}
	if err := op.SaveOpencodeProxyCooldown(channel.ID, "good_then_cool", now.Add(time.Hour)); err != nil {
		t.Fatalf("写冷却失败: %v", err)
	}
	// never_success: 从未成功, 不属于历史可用。
	if err := op.UpsertOpencodeGeneratedAccounts(channel.ID, []string{"never_success"}); err != nil {
		t.Fatalf("落账失败: %v", err)
	}
	// aaa_fabricated: 旧版冷却落库伪造的最近成功时间(计数为 0), 不得计入历史可用。
	if err := db.GetDB().Create(&model.OpencodeProxyAccount{
		ChannelID:     channel.ID,
		Account:       "aaa_fabricated",
		LastSuccessAt: now.Add(-time.Hour),
		CooldownUntil: now.Add(6 * 24 * time.Hour),
		Source:        model.OpencodeAccountSourceTraffic,
	}).Error; err != nil {
		t.Fatalf("写伪造成功行失败: %v", err)
	}
	// hot_account: 成功数最高的好号, 默认排序应置顶。
	if err := db.GetDB().Create(&model.OpencodeProxyAccount{
		ChannelID:         channel.ID,
		Account:           "hot_account",
		LastSuccessAt:     now,
		Source:            model.OpencodeAccountSourceTraffic,
		SuccessCount:      7,
		RealSuccessCount:  5,
		ProbeSuccessCount: 2,
		LastSuccessDay:    now.Format("2006-01-02"),
	}).Error; err != nil {
		t.Fatalf("写高频好号失败: %v", err)
	}

	ledger, err := ListOpencodeAccountStates(channel.ID, "historic")
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if ledger.Historic != 2 || len(ledger.Items) != 2 {
		t.Fatalf("历史可用应为两个真实成功过账号: %+v", ledger)
	}
	if ledger.Items[0].Account != "hot_account" || ledger.Items[1].Account != "good_then_cool" {
		t.Fatalf("历史可用列表应按成功数降序: %v", ledger.Items)
	}
	item := ledger.Items[1]
	if item.State != "unusable" {
		t.Fatalf("冷却中的历史可用账号当前态应为不可用: %+v", item)
	}
	if item.RealSuccessCount != 1 || item.RealFailureCount != 1 || item.ProbeSuccessCount != 1 || item.SuccessCount != 2 {
		t.Fatalf("拆分计数异常: %+v", item)
	}

	all, err := ListOpencodeAccountStates(channel.ID, "all")
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if all.Historic != 2 || all.Total != 4 {
		t.Fatalf("汇总计数异常: historic=%d total=%d", all.Historic, all.Total)
	}
	// 默认排序: 真实+体检成功降序, 同数按账号名升序。
	order := make([]string, 0, len(all.Items))
	for _, row := range all.Items {
		order = append(order, row.Account)
	}
	if strings.Join(order, ",") != "hot_account,good_then_cool,aaa_fabricated,never_success" {
		t.Fatalf("默认排序异常: %v", order)
	}

	// 成功落库清除冷却: 冷却中的好号再次成功后, 库内冷却归零、状态回到可用。
	if err := op.SaveOpencodeProxySuccess(channel.ID, "good_then_cool", now); err != nil {
		t.Fatalf("写成功记录失败: %v", err)
	}
	rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	for _, row := range rows {
		if row.Account != "good_then_cool" {
			continue
		}
		if !row.CooldownUntil.IsZero() {
			t.Fatalf("成功后应清除库内冷却: %+v", row)
		}
		if row.RealSuccessCount != 2 || row.SuccessCount != 3 {
			t.Fatalf("成功计数异常: %+v", row)
		}
	}
	after, err := ListOpencodeAccountStates(channel.ID, "all")
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	for _, row := range after.Items {
		if row.Account == "good_then_cool" && row.State != "usable" {
			t.Fatalf("清冷却后应为可用: %+v", row)
		}
	}
}

// TestOpencodeRelayPrefersVerifiedAccountAndCountsRealTraffic 端到端确认下游真实
// 请求优先选已验证(健康)账号, 且成功按真实转发口径计数。
func TestOpencodeRelayPrefersVerifiedAccountAndCountsRealTraffic(t *testing.T) {
	setupFailoverTest(t)
	probe := newOpencodeProxyProbe(t, func(string) (int, []byte) {
		return http.StatusOK, chatCompletionBody("chatcmpl-verified-first", "ok-verified-first", 3, 5)
	})
	channel := createOpencodeProxyTestChannel(t, probe.template)
	pool := &ocProxyPool{loaded: true, accounts: map[string]*ocProxyAccount{
		"verified-good":    {account: "verified-good", lastSuccessAt: time.Now(), verified: true},
		"unverified-fresh": {account: "unverified-fresh"},
	}}
	ocProxyPools.Store(channel.ID, pool)
	t.Cleanup(func() { ocProxyPools.Delete(channel.ID) })
	group := createIntegrationGroup(t, "opencode-verified-first", opencodeProxyTestConfig(), integrationLeafItem(t, channel, channel.Models[0].Name))

	recorder := postOpencodeProxyRequest(t, group, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("请求应成功, 实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if seen := probe.seenAccounts(); len(seen) != 1 || seen[0] != "verified-good" {
		t.Fatalf("下游请求应先选已验证健康账号, 实际 %v", seen)
	}
	waitForOpencodeAccountCounters(t, channel.ID, "verified-good", 1, 0, 0)
}

// TestOpencodeAutoProbeDueAccounts 验证到期选择: 未探测立即到期, 周期内跳过, 上限截断。
func TestOpencodeAutoProbeDueAccounts(t *testing.T) {
	now := time.Now()
	rows := []model.OpencodeProxyAccount{
		{Account: "fresh", LastProbeAt: now.Add(-time.Hour)},
		{Account: "untested"},
		{Account: "stale", LastProbeAt: now.Add(-25 * time.Hour)},
	}
	due := ocAutoProbeDueAccounts(rows, now, 24*time.Hour)
	if len(due) != 2 || due[0] != "untested" || due[1] != "stale" {
		t.Fatalf("到期选择异常: %v", due)
	}
	many := make([]model.OpencodeProxyAccount, 0, ocAutoProbeMaxPerChannel+5)
	for i := 0; i < ocAutoProbeMaxPerChannel+5; i++ {
		many = append(many, model.OpencodeProxyAccount{Account: fmt.Sprintf("acct_%03d", i)})
	}
	if got := ocAutoProbeDueAccounts(many, now, 24*time.Hour); len(got) != ocAutoProbeMaxPerChannel {
		t.Fatalf("应按上限截断: %d", len(got))
	}
}
