package mask

import "bytes"

// 流式 SSE 增量还原器。这是脱敏功能最难的部分: 占位符可能被拆分到多个 SSE event 中,
// 必须缓冲拼接后才能完整匹配还原。设计详见 docs/脱敏开发/03-流式还原设计.md。

const (
	// DefaultChannel 正文通道键。reasoning_content / tool-call arguments 使用独立通道键,
	// 各通道的 pending 互不干扰, 避免不同字段的占位符前缀在缓冲中混合。
	DefaultChannel = "default"
	// ReasoningChannel 推理增量字段通道键。
	ReasoningChannel = "reasoning"
	// ToolChannelPrefix tool-call arguments 按槽位缓冲的通道键前缀, 完整键为 tool-<index>。
	ToolChannelPrefix = "tool-"
	// pendingMax 占位符最长约 {{LABEL_6位}} ≈ 23 字节; pending 超过 64 字节仍未闭合,
	// 说明不是占位符(可能是代码里的 {{ 模板语法), 直接刷出避免无限缓冲。
	pendingMax = 64
)

// StreamRestorer 流式增量还原器。pending 按通道缓冲尾部可能不完整的占位符前缀。
type StreamRestorer struct {
	mapping *Mapping
	pending map[string][]byte
}

// NewStreamRestorer 构造还原器。
func NewStreamRestorer(mapping *Mapping) *StreamRestorer {
	return &StreamRestorer{mapping: mapping, pending: make(map[string][]byte)}
}

// Mapping 返回底层映射表, 供 tool-call arguments 等按事件整词还原复用。
func (r *StreamRestorer) Mapping() *Mapping { return r.mapping }

// Push 喂入一个增量 chunk 到默认通道, 返回可立即写给客户端的已还原字节。
//
// 处理流程(详见 docs/脱敏开发/03 §2.2):
//  1. 拼接 buffer = pending + chunk;
//  2. 从末尾找最后一个 "{{", 若其后无 "}}" 闭合, 则该处起为可能不完整的占位符前缀, 暂存 pending;
//  3. 对确认内容还原其中完整占位符并输出;
//  4. pending 超过 64 字节强制刷出(非占位符, 模板语法)。
//
// 中途无内容时返回空切片, 调用方不写出、不 Flush, 等待下一 event(绝不主动断流)。
func (r *StreamRestorer) Push(chunk []byte) []byte {
	return r.PushChannel(DefaultChannel, chunk)
}

// PushChannel 喂入一个增量 chunk 到指定通道, 返回可立即写给客户端的已还原字节。
// 不同字段(content/reasoning_content/tool-call arguments)使用独立通道,
// 避免不同字段的占位符前缀在 pending 中互相干扰。
func (r *StreamRestorer) PushChannel(channel string, chunk []byte) []byte {
	buf := append([]byte(nil), r.pending[channel]...)
	buf = append(buf, chunk...)

	out, remain := r.scanAndRestore(buf)
	if len(remain) > pendingMax {
		// 超过上限仍未闭合, 不是占位符, 刷出。
		out = append(out, remain...)
		remain = nil
	}
	if remain == nil {
		delete(r.pending, channel)
	} else {
		r.pending[channel] = remain
	}
	return out
}

// Flush 在流终止时调用, 把默认通道滞留的 pending 原样吐出。
// pending 只含未闭合前缀(非占位符), 原样输出让客户端可见, 绝不猜。
func (r *StreamRestorer) Flush() []byte {
	return r.FlushChannel(DefaultChannel)
}

// FlushChannel 在流终止时把指定通道滞留的 pending 原样吐出。
// 用于 reasoning_content / tool-call arguments 等独立通道的收尾。
func (r *StreamRestorer) FlushChannel(channel string) []byte {
	remain := r.pending[channel]
	delete(r.pending, channel)
	return remain
}

// PendingChannels 返回当前仍滞留 pending 的通道名。用于评估是否需要在流终止时收尾。
func (r *StreamRestorer) PendingChannels() []string {
	channels := make([]string, 0, len(r.pending))
	for ch := range r.pending {
		channels = append(channels, ch)
	}
	return channels
}

// FlushChannels 把所有通道滞留的 pending 原样吐出, 返回按通道名索引的残留。
// 调用方负责按各通道对应的 SSE 字段分别包装, 避免把 reasoning/tool 通道残留误写成 content。
func (r *StreamRestorer) FlushChannels() map[string][]byte {
	out := make(map[string][]byte, len(r.pending))
	for ch, remain := range r.pending {
		out[ch] = remain
		delete(r.pending, ch)
	}
	return out
}

// scanAndRestore 扫描 buf: 还原其中完整占位符, 并把尾部可能不完整的占位符前缀分离到 pending。
func (r *StreamRestorer) scanAndRestore(buf []byte) (output, pending []byte) {
	// 从末尾找最后一个 "{{"。
	idx := bytes.LastIndex(buf, []byte("{{"))
	if idx < 0 {
		// 无完整 "{{" 开头。但末尾可能有一个单独的 "{" ({{ 的前半字节),
		// 需暂存等待下一 chunk 拼接, 否则 "{{" 被拆分时占位符永远无法闭合。
		if len(buf) > 0 && buf[len(buf)-1] == '{' {
			return r.restoreComplete(buf[:len(buf)-1]), buf[len(buf)-1:]
		}
		// 无占位符开头, 全部为确认内容。
		return r.restoreComplete(buf), nil
	}
	// 该 "{{" 之后若有 "}}" 闭合, 则无未闭合前缀, 全部为确认内容。
	if bytes.Contains(buf[idx:], []byte("}}")) {
		// 但 "{{" 之前可能仍有未闭合内容——不会, 因 LastIndex 取最后一个 "{{",
		// 它之前的 "{{" 要么已闭合(被 Contains 覆盖), 要么不存在。全量还原安全。
		return r.restoreComplete(buf), nil
	}
	// 未闭合: 之前为确认内容, 从 idx 起暂存。
	return r.restoreComplete(buf[:idx]), buf[idx:]
}

// restoreComplete 还原 buf 中所有完整占位符。不完整或未登记的占位符原样保留。
func (r *StreamRestorer) restoreComplete(buf []byte) []byte {
	if r.mapping == nil || len(buf) == 0 {
		return buf
	}
	s := string(buf)
	out := PlaceholderRe.ReplaceAllStringFunc(s, func(token string) string {
		orig, ok := r.mapping.Lookup(token)
		if !ok {
			return token
		}
		return orig
	})
	return []byte(out)
}
