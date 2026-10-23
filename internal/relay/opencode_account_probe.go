package relay

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/tidwall/sjson"
)

// 批量账号体检: 钉住账号并发打极小真实请求, 健康入池/被拒冷却, 供免费档直接复用。
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
			results[index] = ocProbeOne(ctx, channel, modelName, account, timeout)
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

// ocProbeRawRequest 构造与下游 Chat 请求同构的极小探测请求: 只带 model 与统一测试
// 消息, 不预设输出上限与流式——免费档整形会像真实下游一样注入 stream/tools/32000
// 输出上限, 出站字节形态与真实转发一致。非 chat 协议回退面板构造器(免费档实际只走 chat)。
func ocProbeRawRequest(format llm.APIFormat, modelName string) (*httpclient.Request, error) {
	if format != llm.APIFormatOpenAIChatCompletion {
		return newTestRequest(format, modelName, defaultTestMessage, 1)
	}
	body, err := sjson.SetBytes([]byte("{}"), "model", modelName)
	if err != nil {
		return nil, err
	}
	if body, err = sjson.SetBytes(body, "messages", []map[string]string{
		{"role": "user", "content": defaultTestMessage},
	}); err != nil {
		return nil, err
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	return &httpclient.Request{
		Method:    http.MethodPost,
		Path:      "/v1" + upstreamPath(format),
		Headers:   headers,
		Body:      body,
		APIFormat: format.String(),
	}, nil
}

// ocProbeOne 钉住单个账号探测一次并归类结论, 结论同步写回台账。
func ocProbeOne(ctx context.Context, channel model.Channel, modelName, account string, timeout time.Duration) AccountProbeResult {
	result := AccountProbeResult{Account: account, Outcome: ocProbeOutcomeError}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe, err := probeOnceWithAccount(probeCtx, channel, modelName, account, func(format llm.APIFormat) (*httpclient.Request, error) {
		return ocProbeRawRequest(format, modelName)
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
	Account       string    `json:"account"`
	State         string    `json:"state"`
	Outcome       string    `json:"outcome"`
	LastProbeStatus int     `json:"last_probe_status"`
	LastProbeAt   time.Time `json:"last_probe_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	CooldownUntil time.Time `json:"cooldown_until"`
	Source        string    `json:"source"`
	// SuccessCount/SuccessDays 是累计成功统计: 次数含真实转发与体检, 天数按自然日去重。
	SuccessCount int64 `json:"success_count"`
	SuccessDays  int   `json:"success_days"`
}

// OpencodeAccountLedger 是某渠道账号台账的筛选视图。
type OpencodeAccountLedger struct {
	ChannelID int                       `json:"channel_id"`
	Items     []OpencodeAccountLedgerItem `json:"items"`
	Total     int                       `json:"total"`
	Usable    int                       `json:"usable"`
	Untested  int                       `json:"untested"`
	Unusable  int                       `json:"unusable"`
	// ProbeError 是最近一次体检遇基础设施故障(网络/代理/上游 5xx)的账号数:
	// 不代表账号被封, 单独筛选便于复测。
	ProbeError int `json:"probe_error"`
}

// ListOpencodeAccountStates 返回渠道账号台账并按状态派生与筛选。
// filter: all(默认)/usable/untested/unusable/error。派生口径:
// 可用 = 未冷却且(最近探测健康 或 7 天内有转发成功); 未测试 = 从未探测且从未成功;
// 不可用 = 冷却中或最近探测被上游拒绝; 网络错误 = 最近探测遇基础设施故障且无在效成功。
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
		if filter != "all" && filter != state {
			continue
		}
		ledger.Items = append(ledger.Items, OpencodeAccountLedgerItem{
			Account:         row.Account,
			State:           state,
			Outcome:         row.LastProbeOutcome,
			LastProbeStatus: row.LastProbeStatus,
			LastProbeAt:     row.LastProbeAt,
			LastSuccessAt:   row.LastSuccessAt,
			CooldownUntil:   row.CooldownUntil,
			Source:          row.Source,
			SuccessCount:    row.SuccessCount,
			SuccessDays:     row.SuccessDays,
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
// 到期 = 距上次体检超过周期(未探测的生成账号立即到期), 单渠道单轮最多 200 个。
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
