package op

import (
	"encoding/json"
)

// 命中明细落库归一上限(文档 07 §3.2、design §2.1.3(4))。
// 与 internal/relay 的 maxMaskMatchesPerRequest / maxMaskMatchLabelBytes /
// maxMaskMatchOriginalBytes / maxMaskMatchPlaceholderBytes / maxMaskMatchesTotalBytes
// 数值完全一致, 作为 defense-in-depth 兜住历史/坏数据与未来旁路写入。
// 两处改动必须同步(op 不 import relay, 避免循环依赖, 故需自持等价常量)。
const (
	maskMatchMaxPerLog       = 128       // 单条错误日志保留的命中明细条数上限(对齐 relay maxMaskMatchesPerRequest)。
	maskMatchMaxLabelBytes   = 32        // 单条 label 字节上限(对齐 relay maxMaskMatchLabelBytes)。
	maskMatchMaxOriginalBytes = 256      // 单条命中原文字节上限(对齐 relay maxMaskMatchOriginalBytes)。
	maskMatchMaxPlaceholderBytes = 64    // 单条占位符字节上限(对齐 relay maxMaskMatchPlaceholderBytes)。
	maskMatchMaxTotalBytes      = 32 * 1024 // 命中明细总字节硬上限(对齐 relay maxMaskMatchesTotalBytes)。
)

// maskMatchRecord 是 normalizeMaskMatches 解码用的本地形状, 与 relay.MaskMatch 字段
// 一一对应; op 不得 import internal/relay(循环依赖), 故在此独立定义。
type maskMatchRecord struct {
	Label       string `json:"label"`
	Original    string `json:"original"`
	Placeholder string `json:"placeholder"`
}

// normalizeMaskMatches 把错误日志中的命中明细 json.RawMessage 归一为有界、合法的 JSON 数组,
// 作为落库与读取两侧的统一兜底安全边界(文档 07 §3.2、design §2.1.3(4))。
//
// 行为契约:
//   - nil / 空串 / 无法解码为 []maskMatchRecord(坏 JSON、非数组、非字符串字段) → 返回 nil, 字段缺席, 不夹带坏字节;
//   - 解码成功后逐条按 UTF-8 边界截断 label/original/placeholder 至各自上限,
//     按条数与总字节上限裁剪超限条目, 重编码为合法 JSON 数组;
//   - 不对 original 做敏感字段名 redact: 命中原文本身即被批准下发的敏感片段,
//     redact 会破坏「展示命中原文」的目的(design §2.1.3(4))。
func normalizeMaskMatches(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var matches []maskMatchRecord
	if err := json.Unmarshal(raw, &matches); err != nil {
		return nil
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) > maskMatchMaxPerLog {
		matches = matches[:maskMatchMaxPerLog]
	}
	kept := make([]maskMatchRecord, 0, len(matches))
	total := 0
	for _, m := range matches {
		m.Label = truncateUTF8Bytes(m.Label, maskMatchMaxLabelBytes)
		m.Original = truncateUTF8Bytes(m.Original, maskMatchMaxOriginalBytes)
		m.Placeholder = truncateUTF8Bytes(m.Placeholder, maskMatchMaxPlaceholderBytes)
		itemBytes := len(m.Label) + len(m.Original) + len(m.Placeholder)
		if total+itemBytes > maskMatchMaxTotalBytes {
			break
		}
		kept = append(kept, m)
		total += itemBytes
	}
	if len(kept) == 0 {
		return nil
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}