package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// 客户端请求在转发过程中的当前状态。
type Status string

const (
	StatusRunning   Status = "running"   // 循环中: 正在选目标, 等待或请求上游。
	StatusCommitted Status = "committed" // 首字节已写出客户端, 此后不可再重试。
	StatusSuccess   Status = "success"   // 响应已完整交付客户端。
	StatusFailed    Status = "failed"    // 请求以错误结束。
	StatusCanceled  Status = "canceled"  // 客户端提前断开或取消。
)

// ErrClass 失败原因的结构化分类, 用于面板按类过滤与失败环形缓冲检索。
type ErrClass string

const (
	ErrClassZeroOutput      ErrClass = "zero_output"      // 上游明确上报输出 token 为 0, 本轮判无效。
	ErrClassNoAnswer        ErrClass = "no_answer"        // 上游按 stop 正常收尾但整轮只有推理内容、没有最终回答(思考耗尽输出预算)。
	ErrClassEarlyEof        ErrClass = "early_eof"        // 上游流在产出任何内容与终止事件之前提前关闭(提前 EOF)。
	ErrClassStreamIdle      ErrClass = "stream_idle"      // 流式转发期相邻事件间隔超过配置的空闲超时。
	ErrClassStreamBudget    ErrClass = "stream_budget"    // 流式累计事件数/字节数超过配置或安全预算。
	ErrClassTimeout         ErrClass = "timeout"          // 成员级响应超时(首事件或完整响应)。
	ErrClassClientCancel    ErrClass = "client_cancel"    // 客户端提前断开或主动取消。
	ErrClassAdminAbort      ErrClass = "admin_abort"      // 管理端人工中止本轮上游调用。
	ErrClassUpstream4xx     ErrClass = "upstream_4xx"     // 上游返回 4xx 状态码。
	ErrClassUpstream5xx     ErrClass = "upstream_5xx"     // 上游返回 5xx 状态码。
	ErrClassUpstreamNetwork ErrClass = "upstream_network" // 网络/代理/DNS/TLS 等基础设施层错误, 按 MemberInfraMaxRetries 独立计数, 达到后走正常冷却通道。
	ErrClassUpstream        ErrClass = "upstream_error"   // 其余上游侧错误。
	ErrClassChannelBusy     ErrClass = "channel_busy"     // 渠道并发槽位满载的本地准入拒绝: 未发起上游调用, 非上游故障, 不计入成员失败与冷却。
	ErrClassRoundsExhausted ErrClass = "rounds_exhausted" // 路由层耗尽（轮次/时长超限），非渠道错误，不落库持久化。
)

// AttemptOutcome 单轮尝试的结束形态。
type AttemptOutcome string

const (
	AttemptSuccess  AttemptOutcome = "success"  // 本轮取得可提交响应。
	AttemptFailed   AttemptOutcome = "failed"   // 本轮真实失败, 计入渠道故障并重试。
	AttemptCanceled AttemptOutcome = "canceled" // 客户端取消或人工中止, 不计为渠道故障。
)

// MaskMatch 命中明细的单条记录, 供日志详情展示"哪个占位符来自哪条规则及其命中原文"。
//
// 决策变更(文档 07): 已批准在日志命中明细中下发并展示命中原文 original,
// 供管理员在日志详情定位被脱敏的原文。该字段随状态流 SSE 下发到所有订阅者,
// 可见性边界与日志详情/错误日志原有管理员可见性一致。
type MaskMatch struct {
	Label       string `json:"label"`       // 规则标签, 如 PHONE / EMAIL / SECRET / TERM
	Original    string `json:"original"`    // 命中原文(被脱敏前的敏感片段)
	Placeholder string `json:"placeholder"` // 替换占位符, 如 {{PHONE_bcdfgh}}
}

// 客户端请求的完整进程内状态, 同时作为状态流的消息形状; 上半部分在请求到达时写入并在结束时定稿, 下半部分每轮循环覆盖。
type RequestState struct {
	ID        uint64    `json:"id"`         // 请求在当前进程内的唯一标识。
	Status    Status    `json:"status"`     // 请求当前状态。
	StartedAt time.Time `json:"started_at"` // 请求到达时间。
	// 首字时点：首个已交付客户端的事件/整响应提交的时刻，用于展示首字耗时（TTFT）。
	FirstTokenAt time.Time     `json:"first_token_at,omitempty"`
	// OutputChars 累计已转发给客户端的输出字符数(按已转发事件 payload 的 UTF-8 字符数近似, 含结构化帧开销)。
	// 流式进行中由 noteOutputChars 持续累加并节流发布, 前端据此实时折算输出速度(c/s); 终态为最终累计值。
	OutputChars  int64         `json:"output_chars,omitempty"`
	Duration     time.Duration `json:"-"`                  // 内部保存纳秒精度；JSON 通过 MarshalJSON 输出 duration 与 duration_ms。
	Model        string        `json:"model"`              // 客户端请求的模型名称, 即分组名称。
	ClientIP     string        `json:"client_ip"`          // 发起请求的客户端 IP 地址。
	APIKey       string        `json:"-"`                  // 内部可能暂存调用密钥；JSON 只输出脱敏尾缀。
	KeyName      string        `json:"key_name,omitempty"` // 调用密钥的名称(面板可读)。
	Usage        llm.Usage     `json:"usage"`              // 请求结束时写入的展示用量。

	UsageEstimated bool `json:"usage_estimated,omitempty"` // 终态用量是否因上游上报可疑零输入而经本地估算修复, 面板据此区分真实上报与估算值。

	Round         int         `json:"round"`                    // 最新一轮循环的递增序号, 人工中止按此匹配以免误杀下一轮。
	TargetChannel string      `json:"target_channel"`           // 最新一轮选中的渠道名称。
	TargetModel   string      `json:"target_model"`             // 最新一轮实际请求上游的模型名称。
	KeyLabel      string      `json:"key_label,omitempty"`      // 最新一轮使用的渠道 Key 标签: "#序号(备注)", 多 Key 渠道用于定位具体凭据, 旧式单 Key 为空。
	ThinkingLevel string      `json:"thinking_level,omitempty"` // 最新一轮实际注入上游的思考等级, 未配置为空。
	ClientFormat  string      `json:"client_format"`            // 下游(客户端)请求协议。
	UpstreamType  string      `json:"upstream_type"`            // 最新一轮上游渠道的协议类型。
	RelayMode     string      `json:"relay_mode"`               // 最新一轮转发方式: passthrough 同协议透传, converted 跨协议转换。
	ProxyAddr     string      `json:"proxy_addr,omitempty"`     // 最新一轮使用的渠道代理完整地址(密码打码, 含 {account} 解析出的别名); 未走渠道代理为空。
	Masked        bool        `json:"masked,omitempty"`         // 本次请求是否执行了脱敏(请求体占位符替换), 面板据此展示脱敏标记。
	MaskMatches   []MaskMatch `json:"mask_matches,omitempty"`   // 脱敏命中明细, 仅在脱敏发生时有值; 旧版本/开关关闭/未命中时为空, 前端据此决定是否渲染命中区(文档 07 §3.1)。
	// MaskMatchesTruncated 命中明细是否因条数/字节上限被裁剪, true 表示当前为摘要而非全量(文档 07 §3.1 第 5 点、design §2.1.3)。
	MaskMatchesTruncated bool            `json:"mask_matches_truncated,omitempty"`
	Sending              bool            `json:"sending"`            // 最新一轮是否仍在等待上游响应。
	Error                string          `json:"error,omitempty"`    // 最新一轮的失败原因, 请求结束后即为最终错误。
	Class                ErrClass        `json:"class,omitempty"`    // 终态错误分类, 请求结束后写入。
	Attempts             []AttemptRecord `json:"attempts,omitempty"` // 每轮尝试轨迹, 按轮次递增追加。

	body           string             // 客户端原始请求体, 体积大故不进状态流, 由独立接口按需拉取。
	responseBody   string             // 聚合后的完整最终响应体, 同样按需拉取。
	cancel         context.CancelFunc // 中止最新一轮上游请求, 仅在该轮等待响应期间非空。
	stopRequested  bool               // 管理端请求整体终止标记: 置位后转发循环在最近的安全点退出, 不再发起任何新尝试。
	stopCh         chan struct{}      // 管理端整体终止信号: StopRequest 中 close 一次, wait/select 据此立即唤醒而非等定时器到期。
	roundStartedAt time.Time          // 最新一轮的开始时刻, 用于计算该轮尝试耗时。
	lastOutputPublish time.Time        // 最近一次输出字符数的节流发布时刻, 把逐块累加收敛为至多 2Hz 的状态推送。
}

// AttemptRecord 一轮上游尝试的轨迹记录, 面板据此渲染请求的时间线。
type AttemptRecord struct {
	Seq          int            `json:"seq"`                      // 轮次序号, 与 Round 一致。
	ChannelID    int            `json:"channel_id"`               // 本轮选中的渠道 ID。
	ChannelName  string         `json:"channel_name"`             // 本轮选中的渠道名称。
	MemberID     int            `json:"member_id"`                // 本轮使用的分组成员 ID。
	Model        string         `json:"model"`                    // 本轮实际请求上游的模型名称。
	KeyLabel     string         `json:"key_label,omitempty"`      // 本轮使用的渠道 Key 标签: "#序号(别名)", 旧式单 Key 为空。
	ProxyAddr    string         `json:"proxy_addr,omitempty"`     // 本轮出口代理地址(密码打码); 空为直连。
	FirstTokenMS int64          `json:"first_token_ms,omitempty"` // 本轮首字耗时毫秒(TTFT), 首字未到为 0。
	LatencyMS    int64          `json:"latency_ms"`               // 本轮从发起到结束的耗时毫秒。
	Outcome      AttemptOutcome `json:"outcome"`                  // 结束形态: 成功/失败/取消。
	ErrClass     ErrClass       `json:"err_class,omitempty"`      // 失败分类, 成功时为空。
	ErrBrief     string         `json:"err_brief,omitempty"`      // 失败摘要, 超长按字节截断。
}

const streamBuffer = 16 // 单个状态流连接的非阻塞消息缓冲容量。
const maxFinished = 200 // 进程内最多保留的已结束请求数量, 需覆盖错误页要展示的最近二十次错误。

// outputCharsPublishInterval 是流式进行中输出字符数的节流发布间隔, 与前端日志卡片的
// 500ms 刷新周期对齐: 逐块累加只做一次 cheap 的自增与时间比较, 状态发布按此间隔收敛。
// 每次发布都要把整个 RequestState 克隆并序列化后推给全部状态流订阅者, 若逐块触发会把
// 高吞吐流式的块频(可达每秒上百块)放大成等量的全量快照推送, 却不对应任何观测增量——
// 前端 500ms 才读一次 now, 2Hz 的发布已覆盖「实时」观感。
const outputCharsPublishInterval = 500 * time.Millisecond

const errBriefLimit = 256     // 错误摘要的最大保留字节数, 超长按 UTF-8 边界截断。
const maxAttempts = 200       // 单个请求最多保留的尝试轨迹条数, 超出后丢弃最旧记录。
const maxFailureRecords = 200 // 失败环形缓冲容量, 仅保留摘要元数据, 不含请求体。

// errAdminStopped 管理端对单个请求发起整体终止时的终态原因。
var errAdminStopped = errors.New("管理端已停止请求")

// maxBodyPreview 已结束请求常驻内存的请求体/响应体预览上限(字节)。
// 完整报文仅在请求进行期间存在, 终态定稿后即截断, 避免大上下文负载下数百 MB 的长期驻留。
const maxBodyPreview = 64 * 1024

var (
	idSeq                atomic.Uint64 // 进程内严格递增的请求 ID。
	mu                   sync.Mutex    // 全部共享状态的互斥锁。
	requests             = make(map[uint64]*RequestState)
	finishedRequestQueue = make([]uint64, 0, maxFinished)       // 按请求 ID 保存的全部请求状态。
	watchers             = make(map[chan RequestState]struct{}) // 全部状态流 SSE 连接。

	failureRing  = make([]FailureSummary, maxFailureRecords) // 失败摘要环形缓冲, 覆盖最旧记录。
	failureCount int                                         // 环内有效记录数, 不超过 maxFailureRecords。
	failureNext  int                                         // 下一条摘要的写入位置。
)

// newRequestState 分配请求 ID 并登记初始运行状态; 返回的记录是本请求后续全部状态写入的入口。
// apiKeyRaw 只在此函数调用栈内存在；RequestState 永远只保留脱敏尾缀，避免原始
// API Key 进入进程内状态、SSE 快照或错误日志。
func newRequestState(model, body, clientIP, apiKeyRaw, keyName string) *RequestState {
	mu.Lock()
	request := &RequestState{
		ID:        idSeq.Add(1),
		Status:    StatusRunning,
		StartedAt: time.Now(),
		Model:     model,
		ClientIP:  clientIP,
		APIKey:    maskAPIKey(apiKeyRaw),
		KeyName:   keyName,
		body:      body,
		stopCh:    make(chan struct{}),
	}
	requests[request.ID] = request
	publishRequestLocked(request)
	mu.Unlock()
	// 客户端 IP 统计独立加锁, 移出全局状态临界区: 登记热路径不为第二把锁付出嵌套开销。
	trackClientRequest(request.ClientIP)
	return request
}

// applyMaskResult 用脱敏后的请求体替换状态中的原始明文, 同时记录命中明细并发布一次
// 请求状态, 使已打开的日志详情实时收到命中信息(文档 07 §3.1)。
// truncated 为命中明细是否因条数/字节上限被裁剪的标记(来自 truncateMaskMatches),
// 与 matches 一并写入, 供前端在截断时提示「仅展示部分命中」。
// 命中明细只属于当前请求这一次 Apply 的结果; 不把原始请求体写回 body(凭据安全红线, 文档 04 §五)。
func (r *RequestState) applyMaskResult(masked string, matches []MaskMatch, truncated bool) {
	mu.Lock()
	r.body = masked
	r.MaskMatches = matches
	r.MaskMatchesTruncated = truncated
	publishRequestLocked(r)
	mu.Unlock()
}

// maskAPIKey 仅保留最后 4 个 rune 并加固定掩码；空值不输出。无论传入的是原始
// key 还是历史调用点传入的 ...ABCD，都统一产出 ...ABCD。
func maskAPIKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, "...")
	runes := []rune(raw)
	if len(runes) > 4 {
		runes = runes[len(runes)-4:]
	}
	return "..." + string(runes)
}

// MarshalJSON 对外发送状态快照时同时提供 legacy duration（纳秒）和明确的
// duration_ms（毫秒），并确保 API Key 已脱敏。前端优先读取 duration_ms。
func (r RequestState) MarshalJSON() ([]byte, error) {
	type requestStateJSON struct {
		ID                   uint64          `json:"id"`
		Status               Status          `json:"status"`
		StartedAt            time.Time       `json:"started_at"`
		FirstTokenAt         time.Time       `json:"first_token_at,omitempty"`
		OutputChars          int64           `json:"output_chars,omitempty"`
		Duration             int64           `json:"duration"` // legacy: nanoseconds
		DurationMS           int64           `json:"duration_ms"`
		Model                string          `json:"model"`
		ClientIP             string          `json:"client_ip"`
		APIKey               string          `json:"api_key,omitempty"`
		KeyName              string          `json:"key_name,omitempty"`
		Usage                llm.Usage       `json:"usage"`
		UsageEstimated       bool            `json:"usage_estimated,omitempty"`
		Round                int             `json:"round"`
		TargetChannel        string          `json:"target_channel"`
		TargetModel          string          `json:"target_model"`
		KeyLabel             string          `json:"key_label,omitempty"`
		ThinkingLevel        string          `json:"thinking_level,omitempty"`
		ClientFormat         string          `json:"client_format"`
		UpstreamType         string          `json:"upstream_type"`
		RelayMode            string          `json:"relay_mode"`
		ProxyAddr            string          `json:"proxy_addr,omitempty"`
		Masked               bool            `json:"masked,omitempty"`
		MaskMatches          []MaskMatch     `json:"mask_matches,omitempty"`
		MaskMatchesTruncated bool            `json:"mask_matches_truncated,omitempty"`
		Sending              bool            `json:"sending"`
		Error                string          `json:"error,omitempty"`
		Class                ErrClass        `json:"class,omitempty"`
		Attempts             []AttemptRecord `json:"attempts"`
	}
	nanoseconds := int64(r.Duration)
	milliseconds := r.Duration.Milliseconds()
	// 归一化 attempts: nil 与空切片统一序列化为 [], 保证 null/[] 契约一致。
	attempts := r.Attempts
	if attempts == nil {
		attempts = make([]AttemptRecord, 0)
	}
	return json.Marshal(requestStateJSON{
		ID: r.ID, Status: r.Status, StartedAt: r.StartedAt,
		FirstTokenAt: r.FirstTokenAt, OutputChars: r.OutputChars, Duration: nanoseconds,
		DurationMS: milliseconds, Model: r.Model, ClientIP: r.ClientIP, APIKey: maskAPIKey(r.APIKey),
		KeyName: r.KeyName, Usage: r.Usage, UsageEstimated: r.UsageEstimated, Round: r.Round,
		TargetChannel: r.TargetChannel, TargetModel: r.TargetModel, KeyLabel: r.KeyLabel,
		ThinkingLevel: r.ThinkingLevel, ClientFormat: r.ClientFormat, UpstreamType: r.UpstreamType,
		RelayMode: r.RelayMode, ProxyAddr: r.ProxyAddr, Masked: r.Masked,
		MaskMatches: r.MaskMatches, MaskMatchesTruncated: r.MaskMatchesTruncated,
		Sending: r.Sending, Error: r.Error, Class: r.Class, Attempts: attempts,
	})
}

// roundLifecycle 把单轮上下文取消与上游响应资源绑定在同一个可并发控制的生命周期里。
// StopRequest/Interrupt 在任何阶段调用 Stop: 响应尚未建立时先取消 roundCtx; 响应随后 Attach
// 会立即关闭; 响应已建立时同时取消 roundCtx 并显式 Close 上游事件流及 MaxConcurrent 槽位。
type roundLifecycle struct {
	cancel   context.CancelCauseFunc
	mu       sync.Mutex
	response *upstreamResponse
	stopped  bool
}

func newRoundLifecycle(cancel context.CancelCauseFunc) *roundLifecycle {
	return &roundLifecycle{cancel: cancel}
}

// Stop 幂等中止本轮。context.CancelCauseFunc 与 upstreamResponse.Close 均可重复调用;
// response Close 会立即关闭底层 SSE body, 使阻塞中的 Next 及时返回。
func (l *roundLifecycle) Stop() {
	if l == nil {
		return
	}
	l.cancel(nil)
	l.mu.Lock()
	l.stopped = true
	response := l.response
	l.mu.Unlock()
	if response != nil {
		response.Close()
	}
}

// Attach 把已取得首个有效事件的响应绑定到本轮。若 Stop 已在 send 返回竞态窗口内发生,
// 立即关闭刚建立的响应, 不让上游流或并发槽位逃逸到下一阶段。
func (l *roundLifecycle) Attach(response *upstreamResponse) {
	if l == nil || response == nil {
		return
	}
	l.mu.Lock()
	l.response = response
	stopped := l.stopped
	l.mu.Unlock()
	if stopped {
		response.Close()
	}
}

// RoundTarget 描述一轮上游调用的目标与协议信息。
type RoundTarget struct {
	MemberID      int    // 本轮使用的分组成员 ID。
	ChannelID     int    // 本轮选中的渠道 ID。
	ChannelName   string // 本轮选中的渠道名称。
	Model         string // 实际请求上游的模型名称。
	KeyLabel      string // 本轮使用的渠道 Key 标签: "#序号(别名)", 旧式单 Key 为空。
	ThinkingLevel string // 注入的思考等级, 未配置为空。
	ClientFormat  string // 下游(客户端)协议。
	UpstreamType  string // 上游渠道的协议类型。
	Passthrough   bool   // 是否同协议透传。
	ProxyAddr     string // 本轮使用的渠道代理完整地址(密码已打码); 未走渠道代理为空。
}

// StopRequest 请求整体终止: 置位终止标记并立即中止当前等待中的轮次。
// 转发循环在最近的循环顶部检查该标记后以取消终态收尾, 不再发起新尝试。
// 取消函数不从此处清零, 由 releaseRoundLifecycle 在定稿时统一释放, 避免在首帧后失去取消能力。
// stopCh 同步关闭: 正阻塞在轮间 wait/RPM 等待的 goroutine 据此立即唤醒, 不必等定时器到期。
func (r *RequestState) StopRequest() {
	mu.Lock()
	if r.stopRequested {
		mu.Unlock()
		return
	}
	r.stopRequested = true
	cancel := r.cancel
	ch := r.stopCh
	mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ch != nil {
		close(ch)
	}
}

// IsStopRequested 返回是否已被管理端要求整体终止。
func (r *RequestState) IsStopRequested() bool {
	mu.Lock()
	defer mu.Unlock()
	return r.stopRequested
}

// startRound 记录本轮选中的目标并进入上游请求, cancel 供人工中止本轮, 返回递增的轮次序号。
// 同时追加一条尝试轨迹, 结束形态由 finishRound 回填。
func (r *RequestState) startRound(cancel context.CancelFunc, target RoundTarget) int {
	mu.Lock()
	defer mu.Unlock()

	r.Round++
	r.TargetChannel = target.ChannelName
	r.TargetModel = target.Model
	r.KeyLabel = target.KeyLabel
	r.ThinkingLevel = target.ThinkingLevel
	r.ClientFormat = target.ClientFormat
	r.UpstreamType = target.UpstreamType
	r.ProxyAddr = target.ProxyAddr
	if target.Passthrough {
		r.RelayMode = "passthrough"
	} else {
		r.RelayMode = "converted"
	}
	r.Sending = true
	r.Error = ""
	r.cancel = cancel
	r.roundStartedAt = time.Now()
	if len(r.Attempts) >= maxAttempts {
		r.Attempts = r.Attempts[len(r.Attempts)-maxAttempts+1:]
	}
	r.Attempts = append(r.Attempts, AttemptRecord{
		Seq:         r.Round,
		ChannelID:   target.ChannelID,
		ChannelName: target.ChannelName,
		MemberID:    target.MemberID,
		Model:       target.Model,
		KeyLabel:    target.KeyLabel,
		ProxyAddr:   target.ProxyAddr,
	})
	publishRequestLocked(r)
	return r.Round
}

// finishRound 记录本轮上游结果, errText 非空表示本轮失败或取消的原因; 空串表示已取得可提交响应。
// 该调用只把本轮写入终态轨迹, 不会撤销本轮的 cancel 函数或上游响应流: 首事件到达后仍可能因客户端断开
// 或管理端中止需要立即触发中止, 真正的资源释放统一由 releaseRoundLifecycle 在本轮真正终结时调用。
func (r *RequestState) finishRound(outcome AttemptOutcome, class ErrClass, errText string) {
	mu.Lock()
	defer mu.Unlock()

	r.Sending = false
	r.Error = errText
	r.closeAttemptLocked(outcome, class, errText)
	publishRequestLocked(r)
}

// releaseRoundLifecycle 释放本轮的取消函数并发布最新状态, 幂等且仅在该轮真正终结后调用一次。
// 流式响应在首事件已取得后, finishRound 会先记 AttemptSuccess 但保留 cancel, 供 StopRequestByID/
// StopAll 仍然能在长流中触发 roundCtx 取消并经由 ctx 中止长转发; 最终的释放由本函数统一执行。
func (r *RequestState) releaseRoundLifecycle() {
	mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// closeAttemptLocked 回填最新一条尝试轨迹的结束形态与耗时; 没有进行中的记录时静默跳过。
// 调用方必须持有锁。
func (r *RequestState) closeAttemptLocked(outcome AttemptOutcome, class ErrClass, errText string) {
	if len(r.Attempts) == 0 || r.Attempts[len(r.Attempts)-1].Seq != r.Round {
		return
	}
	attempt := &r.Attempts[len(r.Attempts)-1]
	attempt.Outcome = outcome
	attempt.LatencyMS = time.Since(r.roundStartedAt).Milliseconds()
	switch {
	case class != "":
		attempt.ErrClass = class
	case outcome == AttemptFailed:
		attempt.ErrClass = ErrClassUpstream
	default:
		attempt.ErrClass = ""
	}
	switch {
	case errText != "" && outcome != AttemptSuccess:
		attempt.ErrBrief = truncateErrBrief(errText)
	case outcome == AttemptCanceled && class == ErrClassAdminAbort:
		attempt.ErrBrief = "人工中止"
	case outcome == AttemptCanceled && class == ErrClassClientCancel:
		attempt.ErrBrief = "客户端断开"
	}
}

// Interrupt 中止指定请求仍在等待响应且轮次匹配的上游请求; 轮次不匹配说明该轮已结束, 不影响后续轮次。
// 与 StopRequest 对齐: 取出 cancel 后调用, 由 releaseRoundLifecycle 在终态路径负责清空字段,
// 保留 cancel 字段在长流期间仍可被 StopRequestByID/StopAll 触发, 实现同一请求多个中止入口的复用。
// 返回是否真触发了 cancel, 方便 handler/前端区分「已中止」与「目标轮次已结束」两种情况。
func Interrupt(id uint64, round int) bool {
	mu.Lock()
	request := requests[id]
	if request == nil || request.Round != round || request.cancel == nil {
		mu.Unlock()
		return false
	}
	cancel := request.cancel
	mu.Unlock()

	cancel()
	return true
}

// wait 在重新选择目标之前退避 seconds 秒; 客户端在退避期间断开或管理端终止请求时以取消终态定稿并返回 false。
func (r *RequestState) wait(ctx context.Context, seconds int) bool {
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		r.markCanceled(ctx.Err(), "", nil)
		return false
	case <-r.stopCh:
		r.markCanceled(errAdminStopped, "", nil)
		return false
	case <-timer.C:
		return true
	}
}

// markCommitted 标记响应已提交; 流式响应在此之后仍会持续转发, 故必须先于提交动作调用。
// 首字时点在此统一记录：流式首帧写出、非流式整响应提交均落在该位置，便于展示首字耗时（TTFT）。
// 同时回填当前轮次尝试轨迹的首字耗时，供时间线展示「首字 X · 总耗时 Y」。
func (r *RequestState) markCommitted() {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()
	r.FirstTokenAt = now
	r.Status = StatusCommitted
	// 回填最新一条尝试轨迹的首字耗时（本轮即成功轮，roundStartedAt 已在 startRound 设置）。
	if len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1].Seq == r.Round && !r.roundStartedAt.IsZero() {
		r.Attempts[len(r.Attempts)-1].FirstTokenMS = now.Sub(r.roundStartedAt).Milliseconds()
	}
	publishRequestLocked(r)
}

// noteOutputChars 累计本请求已转发给客户端的输出字符数, 并按 outputCharsPublishInterval 节流
// 发布状态, 使流式进行中的日志详情能实时展示输出速度(c/s)。逐块调用只做一次 cheap 的
// 自增与时间比较, 真正把状态推给订阅者的动作至多每 500ms 一次, 避免逐块发布风暴。
// n 为本次转发事件 payload 的 UTF-8 字符数(近似, 含结构化帧的 JSON 开销); 调用方不得持有全局锁。
func (r *RequestState) noteOutputChars(n int) {
	if n <= 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	r.OutputChars += int64(n)
	if time.Since(r.lastOutputPublish) >= outputCharsPublishInterval {
		r.lastOutputPublish = time.Now()
		publishRequestLocked(r)
	}
}

// markSucceeded 以成功终态定稿请求。
func (r *RequestState) markSucceeded(responseBody string, usage *llm.Usage) {
	mu.Lock()
	r.Status = StatusSuccess
	r.Error = ""
	r.responseBody = responseBody
	r.Sending = false
	r.cancel = nil
	body := r.body
	status := r.Status
	class := r.Class
	errText := r.Error
	mu.Unlock()
	r.finish(body, responseBody, status, class, errText, usage)
}

// abandonUnfinishedRequest 在 panic 等绕过正常 return 的路径上定稿请求。
// 已经是终态时不再改写, 避免把成功覆盖成失败; 仍在运行或已提交时走 markFailed,
// 从而截断常驻全文。调用方不得在持有 mu 时进入。
func abandonUnfinishedRequest(request *RequestState, err error) {
	if request == nil || err == nil {
		return
	}
	mu.Lock()
	switch request.Status {
	case StatusSuccess, StatusFailed, StatusCanceled:
		mu.Unlock()
		return
	}
	mu.Unlock()
	request.markFailed(err, "", nil)
}

// markFailed 以失败终态定稿请求, 最终错误取自本次失败原因, 并按哨兵错误与 HTTP 状态码归类。
func (r *RequestState) markFailed(err error, responseBody string, usage *llm.Usage) {
	mu.Lock()
	r.Status = StatusFailed
	r.Error = err.Error()
	r.Class = ClassifyError(err)
	r.responseBody = responseBody
	r.Sending = false
	r.cancel = nil
	body := r.body
	status := r.Status
	class := r.Class
	errText := r.Error
	mu.Unlock()
	r.finish(body, responseBody, status, class, errText, usage)
}

// markCanceled 以取消终态定稿请求, 用于客户端提前断开或主动取消。
func (r *RequestState) markCanceled(err error, responseBody string, usage *llm.Usage) {
	mu.Lock()
	r.Status = StatusCanceled
	r.Error = err.Error()
	r.Class = ErrClassClientCancel
	r.responseBody = responseBody
	r.Sending = false
	r.cancel = nil
	body := r.body
	status := r.Status
	class := r.Class
	errText := r.Error
	mu.Unlock()
	r.finish(body, responseBody, status, class, errText, usage)
}

// finish 完成终态定稿的收尾工作, 分两个阶段执行:
// 阶段一(锁外): 用量修复(sanitizeUsage 需按完整请求体估算)与对话留存移交, 两者都可能
// 触及数 MB 的完整报文(gjson 解析/写协程入队); 置于锁外避免单个大请求的定稿把全进程的
// 登记轮次/状态发布/人工中止全部卡在全局锁上。字符串不可变, 锁外读取锁内快照的引用安全。
// 阶段二(锁内): 回写展示字段, 截断常驻报文为预览, 发布终态并裁剪历史。
// 期间终态已在阶段一之前置位, StopRequest/stopAllRunningRequests 按状态跳过本请求,
// 阶段间窗口内面板可经由 RequestBody/ResponseBody 拉取完整报文。
// 与零输出重试(errZeroOutput)的整轮无效判定互不干扰: 那是轮次级判定, 这里是请求级定稿。
func (r *RequestState) finish(body, responseBody string, status Status, class ErrClass, errText string, usage *llm.Usage) {
	usage, estimated := sanitizeUsage(usage, body)
	// 对话留存: 截断前零拷贝移交完整报文引用(字符串不可变), 编码与落盘全部异步。
	record := model.ConversationRecord{
		RequestID:    r.ID,
		CreatedAt:    time.Now(),
		Model:        r.Model,
		ChannelName:  r.TargetChannel,
		TargetModel:  r.TargetModel,
		ClientIP:     r.ClientIP,
		KeyName:      r.KeyName,
		ClientFormat: r.ClientFormat,
		UpstreamType: r.UpstreamType,
		RelayMode:    r.RelayMode,
		Outcome:      conversationOutcome(status),
		ErrClass:     string(class),
		ErrBrief:     truncateErrBrief(errText),
		RawRequest:   body,
		Response:     responseBody,
	}
	if usage != nil {
		record.Usage = model.UsageStat{
			PromptTokens:     int(usage.PromptTokens),
			CompletionTokens: int(usage.CompletionTokens),
		}
	}
	op.CaptureConversation(record)

	// 用量分桶落库: sanitize 修正后的用量 UPSERT 累加到 (小时 × 目标模型) 桶,
	// 失败/取消终态同样计入(上游已真实消耗), 单行同步写不阻塞其他请求的转发;
	// 落库错误仅告警, 不影响定稿路径。
	// 提取 reasoning/cache token 与 cost 从 llm.Usage 的明细字段, duration 从请求耗时。
	if usage != nil {
		var reasoning, cached int64
		var cost float64
		if usage.CompletionTokensDetails != nil {
			reasoning = usage.CompletionTokensDetails.ReasoningTokens
		}
		if usage.PromptTokensDetails != nil {
			cached = usage.PromptTokensDetails.CachedTokens
		}
		if usage.Cost != nil {
			cost = *usage.Cost
		}
		durationMs := time.Since(r.StartedAt).Milliseconds()
		op.RecordUsageBucket(r.TargetModel, usage.PromptTokens, usage.CompletionTokens, reasoning, cached, cost, durationMs)
	}

	mu.Lock()
	defer mu.Unlock()
	// 说明: 管理端 Clear 可能在两阶段之间删除本记录, 此处仍会落失败摘要并发布终态——
	// 这是既有契约(observability 测试特意在离册状态下验证摘要落账), 竞态后果仅是
	// "清空瞬间恰好定稿的失败会多留一条摘要", 有界且无害, 不为它引入代计数。
	r.UsageEstimated = estimated
	if usage != nil {
		r.Usage = *usage
	}
	r.body = truncatePreview(body)
	r.responseBody = truncatePreview(responseBody)
	r.Duration = time.Since(r.StartedAt)
	if status == StatusFailed {
		appendFailureLocked(FailureSummary{
			ID:            r.ID,
			FinishedAt:    time.Now(),
			Model:         r.Model,
			TargetChannel: r.TargetChannel,
			TargetModel:   r.TargetModel,
			ErrClass:      class,
			ErrBrief:      truncateErrBrief(errText),
		})
	}
	publishRequestLocked(r)
	// OLD-11: 终态队列按完成顺序记录请求 ID, 修剪只做队首出队, 不随 requests
	// 总量线性增长; 队列容量与 maxFinished 对齐, map 大小恒有界。
	finishedRequestQueue = append(finishedRequestQueue, r.ID)
	trimFinishedRequestsLocked()
}

// trimFinishedRequestsLocked 裁剪进程内终态历史: 完成队列按序保存终态 ID,
// 超出 maxFinished 即从队首删除最旧终态, 摊还 O(1), 避免每次定稿全表扫描。须持有 mu。
func trimFinishedRequestsLocked() {
	for len(finishedRequestQueue) > maxFinished {
		oldest := finishedRequestQueue[0]
		finishedRequestQueue = finishedRequestQueue[1:]
		delete(requests, oldest)
	}
}

// ClassifyError 按哨兵错误与 HTTP 状态码归类终态失败原因; 无法细分时归入 upstream_error。
func ClassifyError(err error) ErrClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, errZeroOutput) {
		return ErrClassZeroOutput
	}
	if errors.Is(err, errNoAnswerStop) {
		return ErrClassNoAnswer
	}
	if errors.Is(err, errStreamEarlyEof) {
		return ErrClassEarlyEof
	}
	if errors.Is(err, errStreamIdleTimeout) {
		return ErrClassStreamIdle
	}
	if errors.Is(err, errStreamBudgetExceeded) || errors.Is(err, errStreamWindowBudgetExceeded) {
		return ErrClassStreamBudget
	}
	if errors.Is(err, errMemberResponseTimeout) {
		return ErrClassTimeout
	}
	if errors.Is(err, errRoundsExceeded) || errors.Is(err, errDeadlineExceeded) {
		return ErrClassRoundsExhausted
	}
	if isInfrastructureError(err) {
		return ErrClassUpstreamNetwork
	}
	return classifyUpstreamStatus(err)
}

// classifyRound 归类一轮尝试的结束原因, 判定顺序: 客户端取消 > 哨兵错误 > 人工中止 > 成员超时(上下文原因) > 基础设施错误 > HTTP 状态码 > 兜底。
// 客户端取消优先于一切: 父上下文结束时本轮上下文必然同时取消, 不能误判为人工中止。
// 基础设施错误(SOCKS/DNS/TLS/连接重置/连接拒绝)优先于 HTTP 状态码归类, 让 recordRouteFailure 跳过成员冷却。
func classifyRound(err error, parentCtx, roundCtx context.Context) ErrClass {
	if parentCtx != nil && parentCtx.Err() != nil {
		return ErrClassClientCancel
	}
	if err != nil {
		if errors.Is(err, errZeroOutput) {
			return ErrClassZeroOutput
		}
		if errors.Is(err, errNoAnswerStop) {
			return ErrClassNoAnswer
		}
		if errors.Is(err, errChannelConcurrencyFull) {
			return ErrClassChannelBusy
		}
		if errors.Is(err, errStreamEarlyEof) {
			return ErrClassEarlyEof
		}
		if errors.Is(err, errStreamIdleTimeout) {
			return ErrClassStreamIdle
		}
		if errors.Is(err, errStreamBudgetExceeded) || errors.Is(err, errStreamWindowBudgetExceeded) {
			return ErrClassStreamBudget
		}
		if errors.Is(err, errMemberResponseTimeout) {
			return ErrClassTimeout
		}
	}
	if roundCtx != nil {
		switch cause := context.Cause(roundCtx); {
		case errors.Is(cause, errMemberResponseTimeout):
			return ErrClassTimeout
		case errors.Is(cause, errStreamIdleTimeout):
			return ErrClassStreamIdle
		case errors.Is(cause, context.Canceled):
			return ErrClassAdminAbort
		}
	}
	if isInfrastructureError(err) {
		return ErrClassUpstreamNetwork
	}
	return classifyUpstreamStatus(err)
}

// infrastructureErrorKeywords 兜底识别的网络层错误关键词, 覆盖 socks5 / TLS / DNS / 连接重置 / 拒绝等
// 不会被 *httpclient.Error.StatusCode 捕获的常见封装格式。
var infrastructureErrorKeywords = []string{
	"socks", // socks5 proxy dialer: "socks connect tcp ... general SOCKS server failure"
	"connection refused",
	"connection reset",
	"no such host",   // DNS 解析失败
	"tls handshake",  // TLS 握手失败: "net/http: TLS handshake error"
	"x509: ",         // 证书校验失败
	"unexpected eof", // 网络层提前 EOF: "unexpected EOF" (非提前结束的业务流)
	"broken pipe",
	"network is unreachable",
	"i/o timeout", // 部分 dialer 包装的 timeout
}

// isInfrastructureError 判定错误是否属于网络/代理/DNS/TLS 等基础设施层失败, 与 HTTP 业务错误区分。
// 判定条件(满足任一即为 true):
//  1. *httpclient.Error 的 StatusCode == 0: 表示连接建立后或建立前失败, 没有 HTTP 响应;
//  2. *net.OpError: 标准库网络操作错误(拨号/读写/握手等);
//  3. err 文本含 infrastructureErrorKeywords 中的关键词(覆盖三方库的字符串化错误)。
//
// 401/403/429/5xx 等业务错误(具有非零 StatusCode 或"with status NNN"包装)不会落入此分支。
func isInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *httpclient.Error
	if errors.As(err, &httpErr) && httpErr.StatusCode == 0 {
		return true
	}
	// net.OpError / net.Error 在三方库深度 wrap 时 errors.As 链路可能断, 同时用文本兜底
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, kw := range infrastructureErrorKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// statusTextPattern 从错误文本中尽力还原上游 HTTP 状态码, 覆盖 "with status NNN" 与
// "upstream responded NNN" 两种既有包装格式; 不做无锚点的数字扫描以免误匹配正文。
var statusTextPattern = regexp.MustCompile(`(?:with status|upstream responded)\s+(\d{3})`)

// classifyUpstreamStatus 依次从结构化错误(httpclient.Error / llm.ResponseError)和错误文本中解析状态码并归类。
func classifyUpstreamStatus(err error) ErrClass {
	if code, ok := UpstreamStatusCode(err); ok {
		return classifyStatusCode(code)
	}
	return ErrClassUpstream
}

// UpstreamStatusCode 尽力从结构化错误或既有包装格式的文本中还原上游 HTTP 状态码。
func UpstreamStatusCode(err error) (int, bool) {
	var httpErr *httpclient.Error
	if errors.As(err, &httpErr) && httpErr.StatusCode != 0 {
		return httpErr.StatusCode, true
	}
	var responseErr llm.ResponseError
	if errors.As(err, &responseErr) && responseErr.StatusCode != 0 {
		return responseErr.StatusCode, true
	}
	if err != nil {
		if match := statusTextPattern.FindStringSubmatch(err.Error()); match != nil {
			if code, convErr := strconv.Atoi(match[1]); convErr == nil {
				return code, true
			}
		}
		// 部分转换路径的错误只保留原因短语(如 "Request failed: Bad Request, ..."),
		// 按标准短语反查状态码, 让这类错误也能进入确定性快速失败判定。
		lower := strings.ToLower(err.Error())
		for phrase, code := range statusPhraseMap {
			if strings.Contains(lower, "request failed: "+phrase) {
				return code, true
			}
		}
	}
	return 0, false
}

// statusPhraseMap 标准原因短语到状态码的反查表, 配合 "Request failed: <短语>" 包装格式使用。
var statusPhraseMap = map[string]int{
	"bad request":           http.StatusBadRequest,
	"unauthorized":          http.StatusUnauthorized,
	"payment required":      http.StatusPaymentRequired,
	"forbidden":             http.StatusForbidden,
	"not found":             http.StatusNotFound,
	"method not allowed":    http.StatusMethodNotAllowed,
	"conflict":              http.StatusConflict,
	"unprocessable entity":  http.StatusUnprocessableEntity,
	"too many requests":     http.StatusTooManyRequests,
	"internal server error": http.StatusInternalServerError,
	"bad gateway":           http.StatusBadGateway,
	"service unavailable":   http.StatusServiceUnavailable,
}

// classifyStatusCode 按 HTTP 状态码段位归类; 非 4xx/5xx 的异常取值保守归入 upstream_error。
func classifyStatusCode(code int) ErrClass {
	switch {
	case code >= 500:
		return ErrClassUpstream5xx
	case code >= 400:
		return ErrClassUpstream4xx
	default:
		return ErrClassUpstream
	}
}

// truncateErrBrief 把错误摘要按字节截断到上限内, 并回退到完整的 UTF-8 边界避免出现残缺字符。
func truncateErrBrief(text string) string {
	return truncateUTF8Bytes(text, errBriefLimit)
}

// truncatePreview 把常驻内存的请求体/响应体截断到预览上限(UTF-8 边界安全)。
func truncatePreview(text string) string {
	return truncateUTF8Bytes(text, maxBodyPreview)
}

// truncateUTF8Bytes 按字节上限截断文本并保证不切断多字节字符。
// 真正发生截断时用 strings.Clone 复制小结果, 释放对原大字符串底层存储的引用,
// 避免预览字段钉住完整请求/响应报文内存; 未超限的短串路径直接返回原串, 不做无意义复制。
func truncateUTF8Bytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return strings.Clone(cut)
}

// FailureSummary 失败请求的摘要元数据, 不含请求体与响应体。
type FailureSummary struct {
	ID            uint64    `json:"id"`             // 对应的请求 ID, 可据此拉取原始请求体。
	FinishedAt    time.Time `json:"finished_at"`    // 失败定稿时间。
	Model         string    `json:"model"`          // 客户端请求的模型名称, 即分组名称。
	TargetChannel string    `json:"target_channel"` // 最后一次尝试的渠道名称。
	TargetModel   string    `json:"target_model"`   // 最后一次尝试的模型名称。
	ErrClass      ErrClass  `json:"err_class"`      // 失败分类。
	ErrBrief      string    `json:"err_brief"`      // 失败原因摘要, 已按字节截断。
}

// appendFailureLocked 把一条失败摘要写入环形缓冲, 写满后覆盖最旧记录; 调用方必须持有锁。
func appendFailureLocked(summary FailureSummary) {
	failureRing[failureNext] = summary
	failureNext = (failureNext + 1) % maxFailureRecords
	if failureCount < maxFailureRecords {
		failureCount++
	}
}

// FailureSummaries 按新到旧返回最近失败摘要; limit 非正或超出容量时返回全部保留记录, class 非空时按类过滤。
func FailureSummaries(limit int, class ErrClass) []FailureSummary {
	mu.Lock()
	defer mu.Unlock()

	if limit <= 0 || limit > maxFailureRecords {
		limit = maxFailureRecords
	}
	result := make([]FailureSummary, 0, min(limit, failureCount))
	for i := 0; i < failureCount && len(result) < limit; i++ {
		summary := failureRing[(failureNext-1-i+maxFailureRecords)%maxFailureRecords]
		if class != "" && summary.ErrClass != class {
			continue
		}
		result = append(result, summary)
	}
	return result
}

// publishRequestLocked 非阻塞发布最新请求状态, 连接拥塞时关闭它并交给客户端重连获取全量快照; 调用方必须持有锁。
// 尝试轨迹在锁内克隆后才随值拷贝发出, 消费者在锁外序列化时不会与后续轮次写入构成数据竞争。
// 发布副本清空 body/responseBody 引用: SSE 消费者只做 JSON 序列化(两个未导出字段不参与),
// 不清空会让缓冲通道里最多 16 条快照各自钉住完整请求/响应报文, 白白放大常驻内存。
func publishRequestLocked(request *RequestState) {
	outgoing := *request
	outgoing.body = ""
	outgoing.responseBody = ""
	if len(request.Attempts) > 0 {
		outgoing.Attempts = slices.Clone(request.Attempts)
	}
	if len(request.MaskMatches) > 0 {
		outgoing.MaskMatches = slices.Clone(request.MaskMatches)
	}
	for stream := range watchers {
		select {
		case stream <- outgoing:
		default:
			delete(watchers, stream)
			close(stream)
		}
	}
}

// OpenRequestStream 注册请求状态流连接, 返回按请求 ID 倒序的全部快照和后续增量通道。
func OpenRequestStream() ([]RequestState, chan RequestState) {
	mu.Lock()
	defer mu.Unlock()

	stream := make(chan RequestState, streamBuffer)
	watchers[stream] = struct{}{}

	snapshot := make([]RequestState, 0, len(requests))
	for _, request := range requests {
		entry := *request
		entry.body = ""
		entry.responseBody = ""
		if len(request.Attempts) > 0 {
			entry.Attempts = slices.Clone(request.Attempts)
		}
		if len(request.MaskMatches) > 0 {
			entry.MaskMatches = slices.Clone(request.MaskMatches)
		}
		snapshot = append(snapshot, entry)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].ID > snapshot[j].ID })
	return snapshot, stream
}

// CloseRequestStream 注销并关闭指定请求状态流连接。
func CloseRequestStream(stream chan RequestState) {
	mu.Lock()
	defer mu.Unlock()

	if _, exists := watchers[stream]; exists {
		delete(watchers, stream)
		close(stream)
	}
}

// RequestBody 返回指定请求保存的原始请求体, 记录不存在时返回空串。
func RequestBody(id uint64) string {
	mu.Lock()
	defer mu.Unlock()

	if request := requests[id]; request != nil {
		return request.body
	}
	return ""
}

// ResponseBody 返回指定请求当前保存的响应体, 记录不存在或响应未完成时返回空串。
func ResponseBody(id uint64) string {
	mu.Lock()
	defer mu.Unlock()

	if request := requests[id]; request != nil {
		return request.responseBody
	}
	return ""
}

// allStopped 在「日志页一键停止所有请求」开关置位期间, 阻止新请求进入转发循环并终止全部在途请求。
// 使用 atomic.Bool 而非 mutex, 入口热路径仅需读操作, 不必为它付出锁开销。
var allStopped atomic.Bool

// StopAllRequests 立即置位全局停止标志, 并对所有进行中请求调用整体终止; 返回被终止的在途请求数量。
// 标志位的清理交给 ClearStopAll, 由单独接口或自动恢复路径负责, 避免与单次操作耦合。
func StopAllRequests() int {
	allStopped.Store(true)
	return stopAllRunningRequests()
}

// ClearStopAll 清空全局停止标志; 后续新请求恢复进入转发循环。
// 已经在「已停止」期间被拒的新请求不会自动重试, 客户端需要重新发起。
func ClearStopAll() {
	allStopped.Store(false)
}

// IsAllStopped 返回是否处于全局停止状态; 入口与各轮循环均需调用。
func IsAllStopped() bool {
	return allStopped.Load()
}

// stopAllRunningRequests 在持锁快照后逐个调用整体终止; 终止操作本身不带锁可重入。
func stopAllRunningRequests() int {
	mu.Lock()
	snapshot := make([]*RequestState, 0, len(requests))
	for _, r := range requests {
		if r.Status == StatusRunning || r.Status == StatusCommitted {
			snapshot = append(snapshot, r)
		}
	}
	mu.Unlock()
	for _, r := range snapshot {
		r.StopRequest()
	}
	return len(snapshot)
}

// Clear 删除全部已结束的请求记录并清空失败环形缓冲。
func Clear() {
	mu.Lock()
	defer mu.Unlock()

	for id, request := range requests {
		if request.Status != StatusRunning && request.Status != StatusCommitted {
			delete(requests, id)
		}
	}
	failureCount = 0
	failureNext = 0
	finishedRequestQueue = finishedRequestQueue[:0]
}

// StopRequestByID 按 ID 终止指定请求, 返回是否存在该请求。
func StopRequestByID(id uint64) bool {
	mu.Lock()
	request, ok := requests[id]
	mu.Unlock()
	if !ok {
		return false
	}
	request.StopRequest()
	return true
}

// clientIPSet 记录所有调用过的客户端 IP(去重), totalRequestsCount 为总请求数,
// totalErrorCount 为终态失败请求数。三者均进程内累积、重启清零, 仅作面板展示用途。
// totalErrorCount 在 recordErrorLog 中递增, 不依赖错误日志的留存/按类去重/队满丢弃,
// 因此是"实际失败事件总量"的近似(自启动累计), 与受日志保留上限影响的 error_count_24h 互补。
// IP 集合设上限: 采信 ClientIP 的部署下伪造/扫段流量可制造海量唯一 IP, 无上限集合是
// 不可控的内存增长点, 触顶即整体重置(展示口径牺牲准确性换取内存有界)。
var (
	clientIPsMu        sync.Mutex
	clientIPSet        = make(map[string]time.Time) // IP -> 最近访问时间
	totalRequestsCount atomic.Uint64
	totalErrorCount    atomic.Uint64
)

// maxClientIPs 客户端 IP 去重集合的容量上限。
const maxClientIPs = 4096

// trackClientRequest 在新请求登记时记录来源 IP 并递增计数。
// 触顶时淘汰半数最久未访问的 IP(审计 OLD-10), 而不是整体重置, 避免统计口径
// 在达到 4096 个不同 IP 后骤降为 0 的展示跳变。
func trackClientRequest(clientIP string) {
	totalRequestsCount.Add(1)
	if clientIP == "" {
		return
	}
	clientIPsMu.Lock()
	defer clientIPsMu.Unlock()
	now := time.Now()
	if _, exists := clientIPSet[clientIP]; !exists && len(clientIPSet) >= maxClientIPs {
		evictOldestClientIPsLocked(len(clientIPSet) / 2)
	}
	clientIPSet[clientIP] = now
}

// evictOldestClientIPsLocked 按最近访问时间删除 n 个最久未见的 IP。须持有 clientIPsMu。
func evictOldestClientIPsLocked(n int) {
	if n <= 0 {
		return
	}
	type ipSeen struct {
		ip   string
		seen time.Time
	}
	entries := make([]ipSeen, 0, len(clientIPSet))
	for ip, seen := range clientIPSet {
		entries = append(entries, ipSeen{ip: ip, seen: seen})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seen.Before(entries[j].seen) })
	if n > len(entries) {
		n = len(entries)
	}
	for _, e := range entries[:n] {
		delete(clientIPSet, e.ip)
	}
}

// ClientIPCount 返回去重后的客户端 IP 数量。
func ClientIPCount() int {
	clientIPsMu.Lock()
	defer clientIPsMu.Unlock()
	return len(clientIPSet)
}

// TotalRequestCount 返回进程启动以来的总请求数。
func TotalRequestCount() uint64 {
	return totalRequestsCount.Load()
}

// TotalErrorCount 返回进程启动以来的终态失败请求数(业务错误计数)。
// 该计数在 recordErrorLog 中递增, 不受错误日志保留上限/按类去重/队满丢弃影响,
// 重启清零; 供面板作为"实际失败事件总量"的近似, 与 error_count_24h 互补。
func TotalErrorCount() uint64 {
	return totalErrorCount.Load()
}

// conversationOutcome 把终态状态映射为留存记录的 outcome 字段。
func conversationOutcome(status Status) string {
	switch status {
	case StatusSuccess:
		return "success"
	case StatusFailed:
		return "failed"
	default:
		return "canceled"
	}
}
