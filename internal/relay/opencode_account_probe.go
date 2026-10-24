package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/helper"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/sjson"
)

// 批量账号体检: 钉住账号并发打与官方客户端同构的完整请求, 健康入池/被拒冷却, 供免费档直接复用。
// 「账号」是渠道代理模板 {account} 占位符对应的 resin 出口会话, 不是登录凭据。
const (
	// ocProbeMaxAccounts 单次体检的账号数上限, 与台账容量对齐。
	ocProbeMaxAccounts = 2000
	// ocProbeMaxConcurrency 单次体检的并发上限。
	ocProbeMaxConcurrency = 20
	// ocProbeDefaultConcurrency 未指定时的并发数。
	ocProbeDefaultConcurrency = 10
	// ocProbeDefaultCount 未指定时的生成数量。
	ocProbeDefaultCount = 100
	// ocProbeMaxTimeoutMS 单账号探测超时上限。
	ocProbeMaxTimeoutMS = 120_000
	// ocProbeClientIP 是账号体检请求在日志流里的客户端标识。
	ocProbeClientIP = "账号体检"
	// ocProbeOutcome* 是体检结论枚举。
	ocProbeOutcomeHealthy  = "healthy"
	ocProbeOutcomeRejected = "rejected"
	ocProbeOutcomeError    = "error"
)

// ocProbeAccountPattern 限制账号字符集: 账号会被拼进代理 URL 的 userinfo,
// 只放行 userinfo 安全字符, 避免畸形账号破坏代理解析。
var ocProbeAccountPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// AccountProbeResult 是单个账号的体检结论。
type AccountProbeResult struct {
	Account   string `json:"account"`
	Outcome   string `json:"outcome"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

// AccountProbeSummary 是一次体检的汇总。
type AccountProbeSummary struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Rejected int `json:"rejected"`
	Failed   int `json:"failed"`
}

// AccountProbeReport 是一次批量体检的完整报告。
type AccountProbeReport struct {
	ChannelID int                  `json:"channel_id"`
	Model     string               `json:"model"`
	Results   []AccountProbeResult `json:"results"`
	Summary   AccountProbeSummary  `json:"summary"`
}

// ProbeOpencodeAccounts 并发钉住账号探测免费档渠道。
// accounts 非空时逐个探测(去重), 为空时生成 count 个随机账号; modelName 为空时
// 自动选渠道首个 chat 协议模型。健康与 403/429 的入池/冷却由 probeOnceWithAccount
// 的回填逻辑完成, 这里只汇总结论。
func ProbeOpencodeAccounts(ctx context.Context, channelID int, accounts []string, count, concurrency, timeoutMS int, modelName string) (*AccountProbeReport, error) {
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, err
	}
	if !proxyNeedsOpencodeAccount(channel) {
		return nil, fmt.Errorf("渠道不是含 {account} 代理的 OpenCode 免费档, 无需账号体检")
	}
	if modelName == "" {
		modelName, err = ocProbeModelName(channel)
		if err != nil {
			return nil, err
		}
	} else if !ocProbeChannelHasModel(channel, modelName) {
		return nil, fmt.Errorf("模型 %q 不在渠道模型列表中, 请先同步模型", modelName)
	}
	probing, err := ocProbeAccountList(accounts, count)
	if err != nil {
		return nil, err
	}
	// 生成即落账: 台账长期保存, 探测结论随后回填, 未探测项供页面筛选。
	if err := op.UpsertOpencodeGeneratedAccounts(channelID, probing); err != nil {
		return nil, err
	}
	// 展示名整批取一次目录, 供系统提示的 Name 行; 失败回退模型 ID。
	displayName := ocProbeDisplayName(ctx, channel, modelName)
	if concurrency <= 0 {
		concurrency = ocProbeDefaultConcurrency
	}
	if concurrency > ocProbeMaxConcurrency {
		concurrency = ocProbeMaxConcurrency
	}
	if timeoutMS <= 0 {
		timeoutMS = 30_000
	}
	if timeoutMS > ocProbeMaxTimeoutMS {
		timeoutMS = ocProbeMaxTimeoutMS
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond

	results := make([]AccountProbeResult, len(probing))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, account := range probing {
		wg.Add(1)
		go func(index int, account string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index] = ocProbeOne(ctx, channel, modelName, displayName, account, timeout)
		}(i, account)
	}
	wg.Wait()

	report := &AccountProbeReport{ChannelID: channelID, Model: modelName, Results: results}
	for _, result := range results {
		report.Summary.Total++
		switch result.Outcome {
		case ocProbeOutcomeHealthy:
			report.Summary.Healthy++
		case ocProbeOutcomeRejected:
			report.Summary.Rejected++
		default:
			report.Summary.Failed++
		}
	}
	return report, nil
}

// ocProbeChannelHasModel 报告模型名是否在该渠道的模型列表中。
func ocProbeChannelHasModel(channel model.Channel, modelName string) bool {
	for i := range channel.Models {
		if channel.Models[i].Name == modelName {
			return true
		}
	}
	return false
}

// ocProbeModelName 选探测用的模型: 优先 chat 协议的首个模型, 否则首个模型。
// 没有模型时要求先同步。
func ocProbeModelName(channel model.Channel) (string, error) {
	for i := range channel.Models {
		if channel.Models[i].UpstreamProtocol == "" || channel.Models[i].UpstreamProtocol == model.UpstreamProtocolChat {
			return channel.Models[i].Name, nil
		}
	}
	if len(channel.Models) > 0 {
		return channel.Models[0].Name, nil
	}
	return "", fmt.Errorf("渠道没有可用模型, 请先同步模型")
}

// ocProbeAccountList 归一待探测账号: 显式列表去重并校验字符集, 否则生成 count 个随机账号。
func ocProbeAccountList(accounts []string, count int) ([]string, error) {
	if len(accounts) == 0 {
		if count <= 0 {
			count = ocProbeDefaultCount
		}
		if count > ocProbeMaxAccounts {
			count = ocProbeMaxAccounts
		}
		generated := make([]string, 0, count)
		seen := make(map[string]bool, count)
		for len(generated) < count {
			account := newOpencodeProxyAccount()
			if seen[account] {
				continue
			}
			seen[account] = true
			generated = append(generated, account)
		}
		return generated, nil
	}
	if len(accounts) > ocProbeMaxAccounts {
		return nil, fmt.Errorf("单次最多探测 %d 个账号", ocProbeMaxAccounts)
	}
	seen := make(map[string]bool, len(accounts))
	probing := make([]string, 0, len(accounts))
	for _, account := range accounts {
		account = strings.TrimSpace(account)
		if account == "" {
			continue
		}
		if !ocProbeAccountPattern.MatchString(account) {
			return nil, fmt.Errorf("账号 %q 含非法字符, 只允许字母数字下划线连字符且不超过 64 位", account)
		}
		if seen[account] {
			continue
		}
		seen[account] = true
		probing = append(probing, account)
	}
	if len(probing) == 0 {
		return nil, fmt.Errorf("账号列表为空")
	}
	return probing, nil
}

// ocProbeRawRequest 构造与官方客户端逐字节同构的体检请求。免费档 chat 模型按实抓模板
// 发送完整 Agent 形态消息(系统提示+全量工具+流式参数), 免费档整形对其幂等; 其余(非
// chat 协议或非免费模型)回退面板构造器, 避免在付费档燃烧大系统提示的 token。
func ocProbeRawRequest(format llm.APIFormat, channel model.Channel, modelName, displayName string, maxOutput int) (*httpclient.Request, error) {
	if format != llm.APIFormatOpenAIChatCompletion || !isOpencodeFreeModel(channel, modelName) {
		return newTestRequest(format, modelName, defaultTestMessage, 1)
	}
	lane := "v2"
	if isOpencodeV1Lane(channel) {
		lane = "v1"
	}
	body, err := ocProbeClientBody(lane, modelName, displayName, maxOutput)
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if lane == "v2" {
		// 官方 v2 客户端恒发项目目录哈希, 从不发 global。
		headers.Set(opencodeProjectHeader, ocProbeProjectID)
	}
	return &httpclient.Request{
		Method:    http.MethodPost,
		Path:      "/v1" + upstreamPath(format),
		Headers:   headers,
		Body:      body,
		APIFormat: format.String(),
	}, nil
}

//go:embed opencode_probe_v1.json
var opencodeProbeV1JSON []byte

//go:embed opencode_probe_v2.json
var opencodeProbeV2JSON []byte

// ocProbeTemplate 是官方客户端实抓的体检模板: 系统提示(含动态标记)与全量工具定义。
// 2026-10-24 在本地抓包上游用 opencode 1.19.4(v1 档)与 2.1.4(v2 档)各发一条默认测试
// 消息捕获; 工具定义与抓包原文逐字节一致, 系统提示仅模型/展示名/日期三处动态化。
type ocProbeTemplate struct {
	System string          `json:"system"`
	Tools  json.RawMessage `json:"tools"`
}

var (
	ocProbeTemplatesOnce sync.Once
	ocProbeTemplates     map[string]*ocProbeTemplate
	ocProbeTemplatesErr  error
)

// loadOcProbeTemplates 解析两档体检模板, 解析失败在首次调用时固化为错误。
func loadOcProbeTemplates() (map[string]*ocProbeTemplate, error) {
	ocProbeTemplatesOnce.Do(func() {
		ocProbeTemplates = make(map[string]*ocProbeTemplate, 2)
		for lane, raw := range map[string][]byte{"v1": opencodeProbeV1JSON, "v2": opencodeProbeV2JSON} {
			var template ocProbeTemplate
			if ocProbeTemplatesErr = json.Unmarshal(raw, &template); ocProbeTemplatesErr != nil {
				return
			}
			ocProbeTemplates[lane] = &template
		}
	})
	return ocProbeTemplates, ocProbeTemplatesErr
}

// ocProbeProjectID 是体检 v2 档的项目号: 稳定 40-hex, 形态对齐官方客户端按项目目录
// 取哈希(同一网关恒定, 等价于官方客户端在固定项目目录下运行)。
var ocProbeProjectID = func() string {
	sum := sha256.Sum256([]byte("novaveil-opencode-account-probe"))
	return hex.EncodeToString(sum[:20])
}()

// ocProbeClientBody 按官方客户端实抓形态装配体检正文: 键序、参数、工具与抓包一致,
// 仅模型、展示名、日期与用户消息按当次探测填充。maxOutput 非正时回退 32000。
func ocProbeClientBody(lane, modelName, displayName string, maxOutput int) ([]byte, error) {
	templates, err := loadOcProbeTemplates()
	if err != nil {
		return nil, err
	}
	template := templates[lane]
	if template == nil {
		return nil, fmt.Errorf("未知体检模板档位: %s", lane)
	}
	if maxOutput <= 0 {
		maxOutput = opencodeV2CompletionLimit
	}
	system := template.System
	system = strings.ReplaceAll(system, "{{MODEL_NAME}}", displayName)
	system = strings.ReplaceAll(system, "{{MODEL_ID}}", modelName)
	system = strings.ReplaceAll(system, "{{DATE}}", ocProbeDate(time.Now()))
	if strings.Contains(system, "{{") {
		return nil, fmt.Errorf("体检系统提示存在未替换标记")
	}
	tools := template.Tools
	if runtime.GOOS == "windows" {
		// 与免费档整形的 shell 平台口径一致, 保证整形对本模板幂等。
		tools = bytes.ReplaceAll(tools, []byte("on Linux using bash"), []byte("on Windows using bash"))
	}
	systemJSON, err := marshalOpencodeJSON(system)
	if err != nil {
		return nil, err
	}
	userJSON, err := marshalOpencodeJSON(defaultTestMessage)
	if err != nil {
		return nil, err
	}
	messages := []byte(`[{"role":"system","content":` + string(systemJSON) + `},{"role":"user","content":` + string(userJSON) + `}]`)
	body := []byte("{}")
	set := func(path string, value any) error {
		next, err := sjson.SetBytes(body, path, value)
		if err != nil {
			return fmt.Errorf("装配体检字段 %q 失败: %w", path, err)
		}
		body = next
		return nil
	}
	setRaw := func(path string, value []byte) error {
		next, err := sjson.SetRawBytes(body, path, value)
		if err != nil {
			return fmt.Errorf("装配体检字段 %q 失败: %w", path, err)
		}
		body = next
		return nil
	}
	if lane == "v1" {
		// v1 档(1.19.4)键序: model, max_tokens, messages, tools, tool_choice, stream, stream_options。
		if err := set("model", modelName); err != nil {
			return nil, err
		}
		if err := set("max_tokens", maxOutput); err != nil {
			return nil, err
		}
		if err := setRaw("messages", messages); err != nil {
			return nil, err
		}
		if err := setRaw("tools", tools); err != nil {
			return nil, err
		}
		if err := set("tool_choice", "auto"); err != nil {
			return nil, err
		}
	} else {
		// v2 档(2.1.4)键序: model, messages, tools, stream, stream_options, max_completion_tokens。
		if err := set("model", modelName); err != nil {
			return nil, err
		}
		if err := setRaw("messages", messages); err != nil {
			return nil, err
		}
		if err := setRaw("tools", tools); err != nil {
			return nil, err
		}
	}
	if err := set("stream", true); err != nil {
		return nil, err
	}
	if err := setRaw("stream_options", []byte(`{"include_usage":true}`)); err != nil {
		return nil, err
	}
	if lane != "v1" {
		if err := set("max_completion_tokens", maxOutput); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// ocProbeDate 输出官方客户端的系统提示日期格式, 形如 Sat Oct 24 2026(日不补零)。
func ocProbeDate(now time.Time) string {
	return fmt.Sprintf("%s %s %d %d", now.Format("Mon"), now.Format("Jan"), now.Day(), now.Year())
}

// opencodeProbeCatalog 是体检取展示名的目录入口, 测试替换以免真实外呼。
var opencodeProbeCatalog = helper.FetchOpencodeCatalog

// ocProbeDisplayName 返回模型在官方目录的展示名(体检系统提示的 Name 行), 取不到回退模型 ID。
// 失败只影响一行文案, 不影响体检结论。
func ocProbeDisplayName(ctx context.Context, channel model.Channel, modelName string) string {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	catalog, err := opencodeProbeCatalog(fetchCtx, channel)
	if err != nil {
		return modelName
	}
	if name := catalog[modelName].Name; name != "" {
		return name
	}
	return modelName
}

// ocProbeChannelMaxOutput 返回渠道模型表配置的输出上限, 0 表示未配置。
func ocProbeChannelMaxOutput(channel model.Channel, modelName string) int {
	for i := range channel.Models {
		if channel.Models[i].Name == modelName {
			return channel.Models[i].MaxOutput
		}
	}
	return 0
}

// ocProbeOne 钉住单个账号探测一次并归类结论, 结论同步写回台账。
// 每次调用经 probeOnceWithAccount 铸全新的 opencode 会话号: 账号间、同账号复测间
// 的 x-opencode-session(及镜像头与 v1 档 msg_ id)互不相同, 避免上游按会话聚合识别。
func ocProbeOne(ctx context.Context, channel model.Channel, modelName, displayName string, account string, timeout time.Duration) AccountProbeResult {
	result := AccountProbeResult{Account: account, Outcome: ocProbeOutcomeError}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe, err := probeOnceWithAccount(probeCtx, channel, modelName, account, func(format llm.APIFormat) (*httpclient.Request, error) {
		return ocProbeRawRequest(format, channel, modelName, displayName, ocProbeChannelMaxOutput(channel, modelName))
	}, false)
	switch {
	case err != nil && probe == nil:
		result.Detail = err.Error()
	case err != nil:
		// 拿到了 HTTP 响应但未得出容量结论: 403/429 是账号健康度的直接结论,
		// 其余状态归为失败。
		result.Status = probe.Status
		result.Detail = err.Error()
		result.LatencyMS = probe.LatencyMS
		if probe.Status == 403 || probe.Status == 429 {
			result.Outcome = ocProbeOutcomeRejected
		}
	default:
		result.Outcome = ocProbeOutcomeHealthy
		result.Status = probe.Status
		result.Detail = probe.Detail
		result.LatencyMS = probe.LatencyMS
	}
	if saveErr := op.SaveOpencodeProbeResult(channel.ID, account, result.Outcome, result.Status, time.Now()); saveErr != nil {
		log.Warnf("账号体检结论落账失败: %v", saveErr)
	}
	return result
}

// ocAutoProbeInterval 自动体检周期: 每 24 小时一次。
const ocAutoProbeInterval = 24 * time.Hour

// ocAutoProbeMaxPerChannel 单轮单渠道最多体检的到期账号数, 与单次体检上限对齐。
const ocAutoProbeMaxPerChannel = ocProbeMaxAccounts

// OpencodeAccountLedgerItem 是台账中单个账号的展示条目。
type OpencodeAccountLedgerItem struct {
	Account         string    `json:"account"`
	State           string    `json:"state"`
	Outcome         string    `json:"outcome"`
	LastProbeStatus int       `json:"last_probe_status"`
	LastProbeAt     time.Time `json:"last_probe_at"`
	LastSuccessAt   time.Time `json:"last_success_at"`
	CooldownUntil   time.Time `json:"cooldown_until"`
	Source          string    `json:"source"`
	// SuccessCount/SuccessDays 是累计成功统计: 次数含真实转发与体检, 天数按自然日去重。
	SuccessCount int64 `json:"success_count"`
	SuccessDays  int   `json:"success_days"`
	// RealSuccessCount/RealFailureCount/ProbeSuccessCount 按来源拆分:
	// 下游真实转发的成功/失败数, 与体检探测的成功数。
	RealSuccessCount  int64 `json:"real_success_count"`
	RealFailureCount  int64 `json:"real_failure_count"`
	ProbeSuccessCount int64 `json:"probe_success_count"`
}

// OpencodeAccountLedger 是某渠道账号台账的筛选视图。
type OpencodeAccountLedger struct {
	ChannelID int                         `json:"channel_id"`
	Items     []OpencodeAccountLedgerItem `json:"items"`
	Total     int                         `json:"total"`
	Usable    int                         `json:"usable"`
	Untested  int                         `json:"untested"`
	Unusable  int                         `json:"unusable"`
	// ProbeError 是最近一次体检遇基础设施故障(网络/代理/上游 5xx)的账号数:
	// 不代表账号被封, 单独筛选便于复测。
	ProbeError int `json:"probe_error"`
	// Historic 是历史可用账号数: 曾经成功过(不论当前冷却或过期)。
	Historic int `json:"historic"`
}

// ListOpencodeAccountStates 返回渠道账号台账并按状态派生与筛选。
// filter: all(默认)/usable/untested/unusable/error/historic。派生口径:
// 可用 = 未冷却且(最近探测健康 或 7 天内有转发成功); 未测试 = 从未探测且从未成功;
// 不可用 = 冷却中或最近探测被上游拒绝; 网络错误 = 最近探测遇基础设施故障且无在效成功;
// 历史可用 = 曾经成功过(含当前冷却/过期的好号)。
func ListOpencodeAccountStates(channelID int, filter string) (*OpencodeAccountLedger, error) {
	channel, err := op.ChannelGet(channelID)
	if err != nil {
		return nil, err
	}
	if !proxyNeedsOpencodeAccount(channel) {
		return nil, fmt.Errorf("渠道不是含 {account} 代理的 OpenCode 免费档, 无账号台账")
	}
	rows, err := op.LoadOpencodeProxyAccounts(channelID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ledger := &OpencodeAccountLedger{ChannelID: channelID}
	for _, row := range rows {
		state := ocAccountLedgerState(row, now)
		hasSuccess := !row.LastSuccessAt.IsZero()
		switch state {
		case "usable":
			ledger.Usable++
		case "untested":
			ledger.Untested++
		case "error":
			ledger.ProbeError++
		default:
			ledger.Unusable++
		}
		if hasSuccess {
			ledger.Historic++
		}
		if filter == "historic" {
			if !hasSuccess {
				continue
			}
		} else if filter != "all" && filter != state {
			continue
		}
		ledger.Items = append(ledger.Items, OpencodeAccountLedgerItem{
			Account:           row.Account,
			State:             state,
			Outcome:           row.LastProbeOutcome,
			LastProbeStatus:   row.LastProbeStatus,
			LastProbeAt:       row.LastProbeAt,
			LastSuccessAt:     row.LastSuccessAt,
			CooldownUntil:     row.CooldownUntil,
			Source:            row.Source,
			SuccessCount:      row.SuccessCount,
			SuccessDays:       row.SuccessDays,
			RealSuccessCount:  row.RealSuccessCount,
			RealFailureCount:  row.RealFailureCount,
			ProbeSuccessCount: row.ProbeSuccessCount,
		})
	}
	ledger.Total = len(rows)
	return ledger, nil
}

// ocAccountLedgerState 派生台账条目的四态: usable/untested/unusable/error。
func ocAccountLedgerState(row model.OpencodeProxyAccount, now time.Time) string {
	if now.Before(row.CooldownUntil) {
		return "unusable"
	}
	usedInTTL := !row.LastSuccessAt.IsZero() && now.Sub(row.LastSuccessAt) <= ocProxySuccessTTL
	if row.LastProbeOutcome == model.OpencodeAccountOutcomeHealthy || usedInTTL {
		return "usable"
	}
	if row.LastProbeOutcome == "" && row.LastSuccessAt.IsZero() {
		return "untested"
	}
	// 网络/代理/上游 5xx 故障与「被上游拒绝」区分开: 前者不代表账号被封, 可复测。
	if row.LastProbeOutcome == model.OpencodeAccountOutcomeError {
		return "error"
	}
	return "unusable"
}

// DeleteOpencodeAccountsFromLedger 从台账删除指定账号。
func DeleteOpencodeAccountsFromLedger(channelID int, accounts []string) (int64, error) {
	if len(accounts) == 0 {
		return 0, nil
	}
	cleaned := make([]string, 0, len(accounts))
	for _, account := range accounts {
		account = strings.TrimSpace(account)
		if account != "" {
			cleaned = append(cleaned, account)
		}
	}
	deleted, err := op.DeleteOpencodeProxyAccounts(channelID, cleaned)
	if err != nil {
		return 0, err
	}
	// 同步清理进程内池, 避免被删账号在本轮继续被选中。
	if poolValue, ok := ocProxyPools.Load(channelID); ok {
		pool := poolValue.(*ocProxyPool)
		pool.mu.Lock()
		for _, account := range cleaned {
			delete(pool.accounts, account)
		}
		pool.mu.Unlock()
	}
	return deleted, nil
}

// AutoProbeOpencodeAccounts 对启用 {account} 代理的免费档渠道执行到期账号体检。
// 由后台任务每 24 小时调用; 开关(opencode_account_auto_probe)关闭时直接返回。
// 到期 = 距上次体检超过周期(未探测的生成账号立即到期), 单渠道单轮上限与单次体检对齐。
func AutoProbeOpencodeAccounts(ctx context.Context) error {
	enabled, err := op.SettingGetBool(model.SettingKeyOpencodeAccountAutoProbe)
	if err != nil {
		return fmt.Errorf("读取自动体检开关失败: %w", err)
	}
	if !enabled {
		return nil
	}
	for _, channel := range op.ChannelList() {
		if !channel.Enabled || !proxyNeedsOpencodeAccount(channel) {
			continue
		}
		rows, err := op.LoadOpencodeProxyAccounts(channel.ID)
		if err != nil {
			log.Warnf("渠道 %d 账号台账读取失败, 跳过自动体检: %v", channel.ID, err)
			continue
		}
		due := ocAutoProbeDueAccounts(rows, time.Now(), ocAutoProbeInterval)
		if len(due) == 0 {
			continue
		}
		if _, err := ProbeOpencodeAccounts(ctx, channel.ID, due, 0, ocProbeDefaultConcurrency, 0, ""); err != nil {
			log.Warnf("渠道 %d 自动体检失败: %v", channel.ID, err)
			continue
		}
		log.Infof("渠道 %d 自动体检完成: %d 个账号", channel.ID, len(due))
	}
	return nil
}

// ocAutoProbeDueAccounts 选出到期账号: 距上次体检超过周期即到期, 最早未体检的优先。
func ocAutoProbeDueAccounts(rows []model.OpencodeProxyAccount, now time.Time, interval time.Duration) []string {
	sorted := make([]model.OpencodeProxyAccount, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastProbeAt.Before(sorted[j].LastProbeAt)
	})
	due := make([]string, 0, len(sorted))
	for _, row := range sorted {
		if !row.LastProbeAt.IsZero() && now.Sub(row.LastProbeAt) < interval {
			break
		}
		due = append(due, row.Account)
		if len(due) >= ocAutoProbeMaxPerChannel {
			break
		}
	}
	return due
}
