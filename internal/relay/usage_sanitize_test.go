package relay

// 可疑零输入用量修复的单元测试: sanitizeUsage 的豁免与估算规则表,
// 深拷贝边界, 以及 markSucceeded 终态链路上修复值进展示/统计并随快照暴露的集成验证。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm"
)

// nonTrivialSanitizeBody 字节数超过平凡阈值且带非空用户消息, 用于触发估算分支。
const nonTrivialSanitizeBody = `{"model":"gpt-test","messages":[{"role":"user","content":"please summarize the attached quarterly report"}]}`

// paddedNoMessagesBody 超过平凡阈值但解析不出 messages, 应按平凡请求豁免。
const paddedNoMessagesBody = `{"stream":false,"temperature":0.7,"metadata":{"client":"healthcheck","padding":"aaaaaaaaaaaaaaaaaaaa"}}`

// paddedEmptyTextBody 超过平凡阈值且带 messages 但文本全为空, 同样按平凡请求豁免。
const paddedEmptyTextBody = `{"model":"m","messages":[{"role":"user","content":""}],"metadata":{"client":"probe","padding":"bbbbbbbbbbbbbbbbbb"}}`

// TestSanitizeUsageRules 表驱动覆盖 sanitizeUsage 的全部判定分支:
// 短路放行、缓存豁免、平凡请求豁免与非平凡零输入的本地估算。
func TestSanitizeUsageRules(t *testing.T) {
	cases := []struct {
		name         string
		usage        *llm.Usage
		body         string
		repaired     bool                     // 是否期望打标并返回修复拷贝。
		wantEstimate int64                    // 修复时期望的输入估算值。
		wantDetail   *llm.PromptTokensDetails // 修复后应保留的输入明细, nil 表示不校验。
	}{
		{"nil 用量按请求体估算", nil, nonTrivialSanitizeBody, true, int64(len(nonTrivialSanitizeBody)) / 3, nil},
		{"正常正值不修复", &llm.Usage{PromptTokens: 12, CompletionTokens: 34, TotalTokens: 46}, nonTrivialSanitizeBody, false, 0, nil},
		{"缓存读命中豁免", &llm.Usage{PromptTokensDetails: &llm.PromptTokensDetails{CachedTokens: 100}}, nonTrivialSanitizeBody, false, 0, nil},
		{"缓存写命中豁免", &llm.Usage{PromptTokensDetails: &llm.PromptTokensDetails{WriteCachedTokens: 100}}, nonTrivialSanitizeBody, false, 0, nil},
		{"五分钟缓存写豁免", &llm.Usage{PromptTokensDetails: &llm.PromptTokensDetails{WriteCached5MinTokens: 100}}, nonTrivialSanitizeBody, false, 0, nil},
		{"一小时缓存写豁免", &llm.Usage{PromptTokensDetails: &llm.PromptTokensDetails{WriteCached1HourTokens: 100}}, nonTrivialSanitizeBody, false, 0, nil},
		{"短体空 ping 豁免", &llm.Usage{}, "{}", false, 0, nil},
		{"超阈值无消息豁免", &llm.Usage{}, paddedNoMessagesBody, false, 0, nil},
		{"消息文本全空豁免", &llm.Usage{}, paddedEmptyTextBody, false, 0, nil},
		{
			name:         "非平凡零输入估算",
			usage:        &llm.Usage{CompletionTokens: 9, TotalTokens: 9, PromptTokensDetails: &llm.PromptTokensDetails{AudioTokens: 3}},
			body:         nonTrivialSanitizeBody,
			repaired:     true,
			wantEstimate: int64(len(nonTrivialSanitizeBody)) / 3,
			wantDetail:   &llm.PromptTokensDetails{AudioTokens: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, estimated := sanitizeUsage(tc.usage, tc.body)
			if !tc.repaired {
				if estimated {
					t.Fatalf("%s 不应打标, 实际已打标", tc.name)
				}
				if got != tc.usage {
					t.Fatalf("%s 不修复时应原样透传入参指针", tc.name)
				}
				return
			}
			if !estimated {
				t.Fatalf("%s 应打标, 实际未打标", tc.name)
			}
			if got == tc.usage {
				t.Fatalf("%s 修复时应返回深拷贝而非原对象", tc.name)
			}
			if got.PromptTokens != tc.wantEstimate {
				t.Fatalf("输入 token 应估算为 %d, 实际 %d", tc.wantEstimate, got.PromptTokens)
			}
			// nil 用量用例没有原 CompletionTokens 可保留, 期望值为 0。
			wantCompletion := int64(0)
			if tc.usage != nil {
				wantCompletion = tc.usage.CompletionTokens
			}
			if want := tc.wantEstimate + wantCompletion; got.TotalTokens != want {
				t.Fatalf("总 token 应重算为 %d, 实际 %d", want, got.TotalTokens)
			}
			if got.CompletionTokens != wantCompletion {
				t.Fatalf("输出 token 应保留 %d, 实际 %d", wantCompletion, got.CompletionTokens)
			}
			if tc.wantDetail != nil && got.PromptTokensDetails == nil || tc.wantDetail != nil && *got.PromptTokensDetails != *tc.wantDetail {
				t.Fatalf("输入明细应保留 %+v, 实际 %+v", tc.wantDetail, got.PromptTokensDetails)
			}
		})
	}
}

// TestSanitizeUsageHugeBodyNotClamped 构造超大请求体验证估算不被上界夹击:
// 公式 est = len/3 远低于上界 len*2+8192, 结果必须精确等于 len/3。
func TestSanitizeUsageHugeBodyNotClamped(t *testing.T) {
	huge := `{"messages":[{"role":"user","content":"` + strings.Repeat("字", 1<<20) + `"]}]}`
	got, estimated := sanitizeUsage(&llm.Usage{}, huge)
	if !estimated {
		t.Fatal("超大非平凡零输入请求应打标")
	}
	if want := int64(len(huge)) / 3; got.PromptTokens != want {
		t.Fatalf("超大请求体的估算应等于 len/3 = %d, 实际 %d", want, got.PromptTokens)
	}
}

// TestSanitizeUsageDeepCopy 验证修复结果为深拷贝: 原对象与其指针字段完全不受影响。
func TestSanitizeUsageDeepCopy(t *testing.T) {
	details := &llm.PromptTokensDetails{AudioTokens: 2}
	original := &llm.Usage{PromptTokensDetails: details}
	fixed, estimated := sanitizeUsage(original, nonTrivialSanitizeBody)
	if !estimated {
		t.Fatal("非平凡零输入应打标")
	}
	if fixed == original || fixed.PromptTokensDetails == details {
		t.Fatal("修复结果及其明细指针都应是新分配的深拷贝")
	}
	if original.PromptTokens != 0 || original.TotalTokens != 0 || original.PromptTokensDetails.AudioTokens != 2 {
		t.Fatalf("调用方原对象不得被修改: %+v", original)
	}
	if fixed.PromptTokensDetails.AudioTokens != 2 {
		t.Fatalf("深拷贝应保留原有明细字段, 实际 %+v", fixed.PromptTokensDetails)
	}
}

// TestMarkSucceededSanitizesSuspiciousZeroInput 状态集成: 成功终态携带零输入用量时,
// 展示用量替换为本地估算, 快照 JSON 带 usage_estimated 标记;
// 正常正值用量则原样展示且不打标。
func TestMarkSucceededSanitizesSuspiciousZeroInput(t *testing.T) {
	setupFailoverTest(t)
	stubFailureRing(t)

	// 清空历史已结束记录, 避免本用例的请求在终态裁剪时被当作最旧条目淘汰。
	mu.Lock()
	for id, pending := range requests {
		if pending.Status != StatusRunning && pending.Status != StatusCommitted {
			delete(requests, id)
		}
	}
	mu.Unlock()

	request := newRequestState("sanitize-group", nonTrivialSanitizeBody, "", "", "")
	request.TargetModel = "model-sanitize"

	upstreamUsage := &llm.Usage{CompletionTokens: 7}
	request.markSucceeded("{}", upstreamUsage)

	estimate := int64(len(nonTrivialSanitizeBody)) / 3
	if !request.UsageEstimated {
		t.Fatal("非平凡零输入请求应打 usage_estimated 标记")
	}
	if request.Usage.PromptTokens != estimate {
		t.Fatalf("展示用量输入侧应为估算值 %d, 实际 %d", estimate, request.Usage.PromptTokens)
	}
	if request.Usage.TotalTokens != estimate+7 {
		t.Fatalf("展示用量总计应重算为 %d, 实际 %d", estimate+7, request.Usage.TotalTokens)
	}
	if upstreamUsage.PromptTokens != 0 || upstreamUsage.TotalTokens != 0 {
		t.Fatalf("上游上报的原用量对象不得被修改: %+v", upstreamUsage)
	}

	var snapshot RequestState
	states, _ := OpenRequestStream()
	for _, entry := range states {
		if entry.ID == request.ID {
			snapshot = entry
		}
	}
	if snapshot.ID != request.ID {
		t.Fatal("快照中应能找到本次请求")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化快照失败: %v", err)
	}
	if !strings.Contains(string(raw), `"usage_estimated":true`) {
		t.Fatalf("快照 JSON 应含 usage_estimated=true: %s", raw)
	}

	normal := newRequestState("sanitize-group", nonTrivialSanitizeBody, "", "", "")
	normal.markSucceeded("{}", &llm.Usage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17})
	if normal.UsageEstimated {
		t.Fatal("正常正值用量不应打标")
	}
	if normal.Usage.PromptTokens != 12 || normal.Usage.TotalTokens != 17 {
		t.Fatalf("正常用量应原样展示, 实际 %+v", normal.Usage)
	}
}
