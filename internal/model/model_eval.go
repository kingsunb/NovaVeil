package model

import (
	"strings"
	"time"
)

const (
	ModelEvalResultStart = "<<<RESULT>>>"
	ModelEvalResultEnd   = "<<<END>>>"
	ModelEvalPrompt      = "创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画，不要联网，直接做出来。" +
		"请将最终完整的 HTML 代码放在 " + ModelEvalResultStart + " 和 " + ModelEvalResultEnd + " 之间，" +
		"这两个标记之外不要输出任何其他内容。"
	ModelEvalMaxContentBytes = 1024 * 1024
)

type ModelEvalOutcome string

const (
	ModelEvalOK        ModelEvalOutcome = "ok"
	ModelEvalViolation ModelEvalOutcome = "violation"
	ModelEvalError     ModelEvalOutcome = "error"
	// ModelEvalManual 表示手动加入排序、未运行实际评估的条目：仅用于排序与
	// auto 分组，不写评估历史、不累计评估次数，也没有可预览的回复内容。
	ModelEvalManual ModelEvalOutcome = "manual"
)

// ModelEvalSummary 保留评估时的渠道/模型快照。无级联外键，模型同步或渠道删除不清除历史。
// 列表只读取这些字段，原始回复在打开详情时读取。
type ModelEvalSummary struct {
	ID               int64            `json:"id" gorm:"primaryKey;autoIncrement"`
	ChannelID        int              `json:"channel_id" gorm:"index:idx_model_eval_target,priority:1"`
	ChannelModelID   int              `json:"channel_model_id"`
	ChannelName      string           `json:"channel_name" gorm:"type:text"`
	ChannelType      ChannelProvider  `json:"channel_type" gorm:"size:64"`
	ModelName        string           `json:"model_name" gorm:"size:512;index:idx_model_eval_target,priority:2"`
	Outcome          ModelEvalOutcome `json:"outcome" gorm:"size:16;index:idx_model_eval_outcome,priority:1"`
	CreatedAt        time.Time        `json:"created_at" gorm:"index;index:idx_model_eval_target,priority:3;index:idx_model_eval_outcome,priority:2"`
	CompletedAt      time.Time        `json:"completed_at"`
	LatencyMS        int64            `json:"latency_ms"`
	PromptTokens     int64            `json:"prompt_tokens"`
	CompletionTokens int64            `json:"completion_tokens"`
	Error            string           `json:"error" gorm:"type:text"`
	ContentTruncated bool             `json:"content_truncated"`
}

type ModelEval struct {
	ModelEvalSummary `gorm:"embedded"`
	Prompt          string `json:"prompt" gorm:"type:text"`
	// size 让 MySQL 使用可容纳 1 MiB 回复的文本列；SQLite/Postgres 使用 text。
	Content string `json:"content" gorm:"size:1048576"`
}

// ModelEvalContentOutcome 与前端 extractEvalHtml 的包裹判定保持一致。
func ModelEvalContentOutcome(content string) ModelEvalOutcome {
	start := strings.Index(content, ModelEvalResultStart)
	if start < 0 {
		return ModelEvalViolation
	}
	remaining := content[start+len(ModelEvalResultStart):]
	end := strings.Index(remaining, ModelEvalResultEnd)
	if end < 0 || strings.TrimSpace(remaining[:end]) == "" {
		return ModelEvalViolation
	}
	return ModelEvalOK
}
