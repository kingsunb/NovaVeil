package mask

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cat 拼接多段 []byte(测试辅助)。
func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func TestStream_SingleChunk(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	r := NewStreamRestorer(m)
	out := r.Push([]byte("call " + ph + " now"))
	assert.Equal(t, "call 13800138000 now", string(out))
}

func TestStream_SplitAcross2Chunks(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	n := len(ph)
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte("call " + ph[:n/2]))
	out2 := r.Push([]byte(ph[n/2:] + " now"))
	assert.Equal(t, "call 13800138000 now", string(cat(out1, out2)))
}

func TestStream_SplitAcross3Chunks(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	n := len(ph)
	p1, p2, p3 := ph[:n/3], ph[n/3:2*n/3], ph[2*n/3:]
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte("pre " + p1))
	out2 := r.Push([]byte(p2))
	out3 := r.Push([]byte(p3 + " post"))
	assert.Equal(t, "pre 13800138000 post", string(cat(out1, out2, out3)))
}

func TestStream_TemplateSyntaxFlushed(t *testing.T) {
	// {{ 后无 }} 且超 64 字节: 视为模板语法, 刷出而非无限缓冲。
	m := newMapping()
	r := NewStreamRestorer(m)
	big := "{{" + strings.Repeat("a", 70) // 72 字节, 无 }}
	out := r.Push([]byte(big))
	assert.Equal(t, big, string(out), "超长未闭合应刷出")
	// 之后无 pending 残留。
	assert.Empty(t, r.Flush())
}

func TestStream_ShortIncompleteHeldUntilFlush(t *testing.T) {
	// 短未闭合前缀(<64)暂存, 不立即吐出; Flush 时吐出。
	m := newMapping()
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte("text {{incomplete"))
	assert.Equal(t, "text ", string(out1), "未闭合前缀前的明文应输出")
	flush := r.Flush()
	assert.Equal(t, "{{incomplete", string(flush), "残留 pending 原样吐出")
}

func TestStream_PendingOver64Flushed(t *testing.T) {
	m := newMapping()
	// 65 字节未闭合 → 超过 64 上限, 刷出。
	r1 := NewStreamRestorer(m)
	big65 := "{{" + strings.Repeat("a", 63) // 65 字节
	out := r1.Push([]byte(big65))
	assert.Equal(t, big65, string(out), "65 字节未闭合应刷出")

	// 64 字节未闭合 → 未超上限, 暂存到 Flush。
	r2 := NewStreamRestorer(m)
	big64 := "{{" + strings.Repeat("a", 62) // 64 字节
	out2 := r2.Push([]byte(big64))
	assert.Empty(t, out2, "64 字节未闭合应暂存")
	assert.Equal(t, big64, string(r2.Flush()), "Flush 吐出暂存")
}

func TestStream_MultiplePlaceholdersMixedWithText(t *testing.T) {
	m := newMapping()
	ph1 := m.Recall("13800138000", "PHONE")
	ph2 := m.Recall("alice@example.com", "EMAIL")
	body := "a " + ph1 + " b " + ph2 + " c"
	r := NewStreamRestorer(m)
	out := r.Push([]byte(body))
	assert.Equal(t, "a 13800138000 b alice@example.com c", string(out))
}

func TestStream_FlushAtEnd_NoLoss(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	n := len(ph)
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte("x " + ph[:n/2]))
	out2 := r.Push([]byte(ph[n/2:]))
	flush := r.Flush()
	// 占位符已完整还原, Flush 无残留。
	assert.Equal(t, "x 13800138000", string(cat(out1, out2)))
	assert.Empty(t, flush)
}

func TestStream_UnresolvedPlaceholderLeftAsIs(t *testing.T) {
	// 未登记占位符: 还原查不到, 原样保留, 绝不猜。
	m := newMapping()
	r := NewStreamRestorer(m)
	out := r.Push([]byte("see {{PHONE_bcdfgh}} here"))
	assert.Equal(t, "see {{PHONE_bcdfgh}} here", string(out))
}

func TestStream_EmptyChunk(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte("pre " + ph[:4])) // 不完整前缀, 部分暂存
	out2 := r.Push(nil)                     // 空 event: 不写出、不断连
	out3 := r.Push([]byte(ph[4:] + " post"))
	assert.Equal(t, "pre 13800138000 post", string(cat(out1, out2, out3)))
}

func TestStream_NilMappingPassthrough(t *testing.T) {
	r := NewStreamRestorer(nil)
	out := r.Push([]byte("plain text no placeholders"))
	assert.Equal(t, "plain text no placeholders", string(out))
}

func TestStream_RoundTripWithEngine(t *testing.T) {
	// 端到端: 引擎脱敏 → 流式分块还原 → 原文。
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("call 13800138000 and 13900139000", "s1", enable("PHONE"), nil)
	require.NoError(t, err)
	ph := PlaceholderRe.FindString(res.Masked)
	require.NotEmpty(t, ph)

	r := NewStreamRestorer(res.Mapping)
	// 把脱敏文本拆成 3 段(占位符跨段)。
	masked := res.Masked
	n := len(masked)
	out := cat(
		r.Push([]byte(masked[:n/3])),
		r.Push([]byte(masked[n/3:2*n/3])),
		r.Push([]byte(masked[2*n/3:])),
		r.Flush(),
	)
	assert.Equal(t, "call 13800138000 and 13900139000", string(out))
}

func TestStream_DelimiterSplitAcrossChunks(t *testing.T) {
	// {{ 被拆分到两个 chunk: 第一个 chunk 末尾是单个 {, 第二个 chunk 以 { 开头。
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	// ph = "{{PHONE_xxxxxx}}", 在 {{ 之间拆开
	openIdx := strings.Index(ph, "{{")
	part1 := "pre " + ph[:openIdx+1] // "pre {"  (单个 { 在末尾)
	part2 := ph[openIdx+1:]          // "{PHONE_xxxxxx}}"
	part3 := " post"
	r := NewStreamRestorer(m)
	out1 := r.Push([]byte(part1))
	out2 := r.Push([]byte(part2))
	out3 := r.Push([]byte(part3))
	flush := r.Flush()
	assert.Equal(t, "pre 13800138000 post", string(cat(out1, out2, out3, flush)))
}

func TestStream_AdjacentPlaceholders(t *testing.T) {
	// 相邻占位符无间隔文本: {{PH1}}{{PH2}}
	m := newMapping()
	ph1 := m.Recall("13800138000", "PHONE")
	ph2 := m.Recall("13900139000", "PHONE")
	body := ph1 + ph2
	r := NewStreamRestorer(m)
	out := r.Push([]byte(body))
	assert.Equal(t, "1380013800013900139000", string(out))
}

func TestStream_PushChannelIsolation(t *testing.T) {
	// 各通道 pending 独立: reasoning/tool 通道的占位符前缀不能污染正文, 反之亦然(审计 REL-04)。
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	ph2 := m.Recall("13900139000", "PHONE")
	r := NewStreamRestorer(m)

	n := len(ph)
	outDefault := r.PushChannel(DefaultChannel, []byte("call "+ph[:n/3]))
	outReason := r.PushChannel(ReasoningChannel, []byte("think "+ph2[:n/3]))
	outDefault2 := r.PushChannel(DefaultChannel, []byte(ph[n/3:2*n/3]))
	outReason2 := r.PushChannel(ReasoningChannel, []byte(ph2[n/3:2*n/3]))
	outDefault3 := r.PushChannel(DefaultChannel, []byte(ph[2*n/3:]))
	outReason3 := r.PushChannel(ReasoningChannel, []byte(ph2[2*n/3:]))

	assert.Equal(t, "call 13800138000", string(cat(outDefault, outDefault2, outDefault3)))
	assert.Equal(t, "think 13900139000", string(cat(outReason, outReason2, outReason3)))
}

func TestStream_ToolChannelPrefixIndexing(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	r := NewStreamRestorer(m)

	n := len(ph)
	out0 := r.PushChannel(ToolChannelPrefix+"0", []byte(ph[:n/2]))
	out1 := r.PushChannel(ToolChannelPrefix+"1", []byte("x"))
	out0b := r.PushChannel(ToolChannelPrefix+"0", []byte(ph[n/2:]))

	assert.Equal(t, "13800138000", string(cat(out0, out0b)))
	assert.Equal(t, "x", string(out1))

	remain := r.FlushChannels()
	assert.Empty(t, remain)
}

func TestStream_FlushChannelsReturnsOnlyPending(t *testing.T) {
	m := newMapping()
	r := NewStreamRestorer(m)
	_ = r.PushChannel(DefaultChannel, []byte("text {{incomplete"))
	_ = r.PushChannel(ReasoningChannel, []byte("think {{x"))
	toolCh := ToolChannelPrefix + "0"
	_ = r.PushChannel(toolCh, []byte(`{"a":"{{y`))

	rem := r.FlushChannels()
	if len(rem) != 3 {
		t.Fatalf("FlushChannels len = %d, want 3", len(rem))
	}
	assert.Equal(t, "{{incomplete", string(rem[DefaultChannel]), "default residual mismatch")
	assert.Equal(t, "{{x", string(rem[ReasoningChannel]), "reasoning residual mismatch")
	assert.Equal(t, "{{y", string(rem[toolCh]), "tool residual mismatch")
	// FlushChannels 后 pending 清空。
	assert.Empty(t, r.FlushChannels())
}
