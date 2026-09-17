package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestStateJSONMasksAPIKeyAndAddsDurationMS(t *testing.T) {
	secret := "sk-live-very-secret-ABCD"
	state := RequestState{
		ID:        7,
		Status:    StatusSuccess,
		StartedAt: time.Unix(100, 0).UTC(),
		Duration:  1500 * time.Millisecond,
		Model:     "demo",
		ClientIP:  "203.0.113.10",
		APIKey:    secret,
		Attempts:  []AttemptRecord{},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal RequestState: %v", err)
	}
	body := string(encoded)
	if strings.Contains(body, secret) {
		t.Fatalf("serialized state leaked full API key: %s", body)
	}

	var got struct {
		APIKey     string `json:"api_key"`
		Duration   int64  `json:"duration"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal serialized state: %v", err)
	}
	if got.APIKey != "...ABCD" {
		t.Fatalf("api_key = %q, want masked tail ...ABCD", got.APIKey)
	}
	if got.Duration != int64(1500*time.Millisecond) {
		t.Fatalf("duration = %d, want legacy nanoseconds %d", got.Duration, int64(1500*time.Millisecond))
	}
	if got.DurationMS != 1500 {
		t.Fatalf("duration_ms = %d, want 1500", got.DurationMS)
	}
}

func TestRequestStateJSONMasksRunningAPIKey(t *testing.T) {
	secret := "sk-live-running-secret-WXYZ"
	state := RequestState{
		ID:        8,
		Status:    StatusRunning,
		StartedAt: time.Now().UTC(),
		Duration:  0,
		Model:     "demo-running",
		ClientIP:  "203.0.113.99",
		APIKey:    secret,
		Attempts:  []AttemptRecord{},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal running RequestState: %v", err)
	}
	body := string(encoded)
	if strings.Contains(body, secret) {
		t.Fatalf("running serialized state leaked full API key: %s", body)
	}

	var got struct {
		APIKey     string `json:"api_key"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal running serialized state: %v", err)
	}
	if got.APIKey != "...WXYZ" {
		t.Fatalf("running api_key = %q, want masked tail ...WXYZ", got.APIKey)
	}
	if got.DurationMS != 0 {
		t.Fatalf("running duration_ms = %d, want 0", got.DurationMS)
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"...ABCD":                  "...ABCD",
		"sk-short":                 "...hort",
		"sk-live-very-secret-ABCD": "...ABCD",
	}
	for input, want := range cases {
		if got := maskAPIKey(input); got != want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestRequestStateJSONIncludesProxyAddrAndKeyLabel 验证 MarshalJSON 输出 proxy_addr 和 key_label，
// 修复前这两个字段在 requestStateJSON 中缺失，导致前端列表始终显示「直连」且无法看到密钥标签。
func TestRequestStateJSONIncludesProxyAddrAndKeyLabel(t *testing.T) {
	state := RequestState{
		ID:        100,
		Status:    StatusSuccess,
		StartedAt: time.Unix(200, 0).UTC(),
		Duration:  500 * time.Millisecond,
		Model:     "demo",
		ClientIP:  "203.0.113.50",
		ProxyAddr: "socks5://***@10.0.0.9:1080",
		KeyLabel:  "#1(my-key)",
		Attempts:  []AttemptRecord{},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		ProxyAddr string `json:"proxy_addr"`
		KeyLabel  string `json:"key_label"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ProxyAddr != "socks5://***@10.0.0.9:1080" {
		t.Fatalf("proxy_addr = %q, want socks5://***@10.0.0.9:1080", got.ProxyAddr)
	}
	if got.KeyLabel != "#1(my-key)" {
		t.Fatalf("key_label = %q, want #1(my-key)", got.KeyLabel)
	}
}

// TestRequestStateJSONOmitsEmptyProxyAddr 验证无代理时 proxy_addr 为空且 omitempty 生效。
func TestRequestStateJSONOmitsEmptyProxyAddr(t *testing.T) {
	state := RequestState{
		ID:        101,
		Status:    StatusSuccess,
		StartedAt: time.Unix(300, 0).UTC(),
		Model:     "demo",
		ClientIP:  "203.0.113.51",
		Attempts:  []AttemptRecord{},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		ProxyAddr string `json:"proxy_addr"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ProxyAddr != "" {
		t.Fatalf("proxy_addr = %q, want empty for direct connection", got.ProxyAddr)
	}
}

// TestRequestStateJSONMaskMatches 验证命中明细序列化(文档 07 §3.4):
//   - 有命中时 JSON 含正确的 label/original/placeholder(决策变更: 已批准下发原文);
//   - 无命中(nil)时 mask_matches 字段因 omitempty 不出现。
func TestRequestStateJSONMaskMatches(t *testing.T) {
	t.Run("populated matches serialize correctly", func(t *testing.T) {
		state := RequestState{
			ID:        200,
			Status:    StatusSuccess,
			StartedAt: time.Unix(100, 0).UTC(),
			Model:     "demo",
			ClientIP:  "203.0.113.60",
			Attempts:  []AttemptRecord{},
			MaskMatches: []MaskMatch{
				{Label: "PHONE", Original: "13800138000", Placeholder: "{{PHONE_abcd12}}"},
				{Label: "TERM", Original: "内部代号A", Placeholder: "{{TERM_ef3456}}"},
			},
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got struct {
			MaskMatches []MaskMatch `json:"mask_matches"`
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.MaskMatches) != 2 {
			t.Fatalf("mask_matches len = %d, want 2", len(got.MaskMatches))
		}
		if got.MaskMatches[0].Label != "PHONE" || got.MaskMatches[0].Original != "13800138000" || got.MaskMatches[0].Placeholder != "{{PHONE_abcd12}}" {
			t.Fatalf("mask_matches[0] = %+v, want PHONE match", got.MaskMatches[0])
		}
		if got.MaskMatches[1].Label != "TERM" || got.MaskMatches[1].Original != "内部代号A" || got.MaskMatches[1].Placeholder != "{{TERM_ef3456}}" {
			t.Fatalf("mask_matches[1] = %+v, want TERM match", got.MaskMatches[1])
		}
		// 决策变更(文档 07): 已批准下发原文, JSON 必须包含 original 字段。
		if !strings.Contains(string(encoded), `"original"`) {
			t.Fatalf("mask_matches JSON 应包含 original: %s", encoded)
		}
	})

	t.Run("nil matches omitted by omitempty", func(t *testing.T) {
		state := RequestState{
			ID:        201,
			Status:    StatusSuccess,
			StartedAt: time.Unix(100, 0).UTC(),
			Model:     "demo",
			ClientIP:  "203.0.113.61",
			Attempts:  []AttemptRecord{},
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), `"mask_matches"`) {
			t.Fatalf("nil mask_matches 应因 omitempty 不出现在 JSON 中: %s", encoded)
		}
	})
}

// TestRequestStateJSONMaskMatchesTruncated 验证截断标记序列化(文档 07 §3.1 第 5 点):
//   - MaskMatchesTruncated=true 时 JSON 含 "mask_matches_truncated":true;
//   - 缺省(false)时因 omitempty 不出现。
func TestRequestStateJSONMaskMatchesTruncated(t *testing.T) {
	t.Run("truncated true emits mask_matches_truncated", func(t *testing.T) {
		state := RequestState{
			ID:                   300,
			Status:               StatusSuccess,
			StartedAt:            time.Unix(100, 0).UTC(),
			Model:                "demo",
			ClientIP:             "203.0.113.70",
			Attempts:             []AttemptRecord{},
			MaskMatches:          []MaskMatch{{Label: "PHONE", Original: "13800138000", Placeholder: "{{PHONE_x}}"}},
			MaskMatchesTruncated: true,
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(encoded), `"mask_matches_truncated":true`) {
			t.Fatalf("truncated=true 时 JSON 应含 mask_matches_truncated:true: %s", encoded)
		}
	})

	t.Run("truncated false omitted by omitempty", func(t *testing.T) {
		state := RequestState{
			ID:          301,
			Status:      StatusSuccess,
			StartedAt:   time.Unix(100, 0).UTC(),
			Model:       "demo",
			ClientIP:    "203.0.113.71",
			Attempts:    []AttemptRecord{},
			MaskMatches: []MaskMatch{{Label: "PHONE", Original: "13800138000", Placeholder: "{{PHONE_x}}"}},
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), `mask_matches_truncated`) {
			t.Fatalf("truncated=false 时 mask_matches_truncated 应因 omitempty 缺席: %s", encoded)
		}
	})
}

// TestAttemptRecordJSONIncludesFirstTokenMS 验证 AttemptRecord 序列化包含 first_token_ms 字段。
func TestAttemptRecordJSONIncludesFirstTokenMS(t *testing.T) {
	attempt := AttemptRecord{
		Seq:          1,
		ChannelID:    7,
		ChannelName:  "ch",
		Model:        "m",
		FirstTokenMS: 120,
		LatencyMS:    500,
		Outcome:      AttemptSuccess,
	}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	var got struct {
		FirstTokenMS int64 `json:"first_token_ms"`
		LatencyMS    int64 `json:"latency_ms"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal attempt: %v", err)
	}
	if got.FirstTokenMS != 120 {
		t.Fatalf("first_token_ms = %d, want 120", got.FirstTokenMS)
	}
	if got.LatencyMS != 500 {
		t.Fatalf("latency_ms = %d, want 500", got.LatencyMS)
	}
}

// TestMarkCommittedSetsAttemptFirstTokenMS 验证 markCommitted 回填当前轮次尝试轨迹的首字耗时。
func TestMarkCommittedSetsAttemptFirstTokenMS(t *testing.T) {
	Clear()
	state := newRequestState("demo", "{}", "127.0.0.1", "sk-test-ABCD", "test-key")
	state.startRound(func() {}, RoundTarget{
		MemberID:    1,
		ChannelID:   7,
		ChannelName: "ch",
		Model:       "m",
	})
	// 确保首字时点与轮次起始有可测量的差值。
	time.Sleep(2 * time.Millisecond)
	// finishRound 先记录本轮成功并设置 LatencyMS，markCommitted 随后回填 FirstTokenMS。
	state.finishRound(AttemptSuccess, "", "")
	state.markCommitted()
	state.markSucceeded("resp", nil)

	if len(state.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(state.Attempts))
	}
	if state.Attempts[0].FirstTokenMS <= 0 {
		t.Fatalf("first_token_ms = %d, want > 0 after markCommitted", state.Attempts[0].FirstTokenMS)
	}
	if state.Attempts[0].LatencyMS < state.Attempts[0].FirstTokenMS {
		t.Fatalf("latency_ms (%d) should be >= first_token_ms (%d)", state.Attempts[0].LatencyMS, state.Attempts[0].FirstTokenMS)
	}
}

// TestRequestStateJSONAttemptsNeverNull 技术债回归: attempts 契约一致。
// 修复前 MarshalJSON 构造了非 nil 的 attempts 局部值, 却仍序列化 r.Attempts(可能 nil),
// 导致 nil 时输出 "attempts":null 而非 "attempts":[]。前端按数组迭代时 null 会报错。
func TestRequestStateJSONAttemptsNeverNull(t *testing.T) {
	// nil attempts 必须序列化为 []。
	emptyState := RequestState{
		ID:        42,
		Status:    StatusSuccess,
		StartedAt: time.Unix(100, 0).UTC(),
		Model:     "demo",
		ClientIP:  "203.0.113.20",
		Attempts:  nil,
	}
	encoded, err := json.Marshal(emptyState)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"attempts":null`) {
		t.Fatalf("nil attempts 不应序列化为 null: %s", encoded)
	}
	var got struct {
		Attempts []AttemptRecord `json:"attempts"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Attempts == nil {
		t.Fatalf("解码后 attempts 不应为 nil, 应为空切片: %s", encoded)
	}
	if len(got.Attempts) != 0 {
		t.Fatalf("空 attempts 应解码为长度 0 切片, 实际 %d: %s", len(got.Attempts), encoded)
	}

	// 非 nil attempts 正常序列化。
	populatedState := RequestState{
		ID:        43,
		Status:    StatusFailed,
		StartedAt: time.Unix(200, 0).UTC(),
		Model:     "demo2",
		ClientIP:  "203.0.113.21",
		Attempts: []AttemptRecord{
			{Seq: 1, ChannelID: 7, ChannelName: "ch", Model: "m", Outcome: AttemptFailed, ErrClass: ErrClassTimeout, ErrBrief: "boom"},
		},
	}
	encoded, err = json.Marshal(populatedState)
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal populated: %v", err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Seq != 1 || got.Attempts[0].Outcome != AttemptFailed {
		t.Fatalf("非 nil attempts 序列化/反序列化不符: %+v", got.Attempts)
	}
}
