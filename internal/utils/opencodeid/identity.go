// Package opencodeid: 身份派生——信号提取、scope 混淆、msg_ ID turn 钉住。
// 从上游 opencode2api/internal/identity 逐行移植, 适配 []byte body。
package opencodeid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// claudeCodeSessionPattern 提取 Claude Code user_id 中的 session UUID。
var claudeCodeSessionPattern = regexp.MustCompile(`_session_([0-9A-Fa-f-]{36})$`)

// ClaudeCodeSessionID 从 user_id 提取会话 UUID。
func ClaudeCodeSessionID(userID string) string {
	if userID == "" {
		return ""
	}
	if match := claudeCodeSessionPattern.FindStringSubmatch(userID); match != nil {
		return match[1]
	}
	var value map[string]any
	if json.Unmarshal([]byte(userID), &value) == nil {
		if s, ok := value["session_id"].(string); ok {
			return s
		}
	}
	return ""
}

// IsOfficialSessionID 判断是否为官方客户端 minted 的 ses_ 会话号。
func IsOfficialSessionID(signal string) bool {
	return officialSessionPattern.MatchString(signal) &&
		descendingTimePartPattern.MatchString(signal[4:16])
}

// ScopedSessionID 混入调用者 scope 后确定性派生 ses_ 会话号。
// 官方号原样保留; 其余信号混入 scope 再哈希, 使不同调用者不共享会话。
func ScopedSessionID(scope, signal string) string {
	if IsOfficialSessionID(signal) {
		return signal
	}
	if scope != "" {
		signal = scope + "\x00" + signal
	}
	return SessionIDFromSignal(signal)
}

// ExtractSessionSignal 从 10+ 源提取会话信号, 空则回退 conversationSeed / previous_response_id。
func ExtractSessionSignal(headers http.Header, body []byte) string {
	signal := firstNonEmpty(
		headers.Get("x-opencode-session"),
		headers.Get("x-opencode-session-id"),
		headers.Get("x-session-affinity"),
		headers.Get("x-session-id"),
		headers.Get("session_id"),
		headers.Get("conversation-id"),
		headers.Get("conversation_id"),
		gjson.GetBytes(body, "conversation_id").String(),
		gjson.GetBytes(body, "metadata.session_id").String(),
		ClaudeCodeSessionID(gjson.GetBytes(body, "metadata.user_id").String()),
	)
	if signal == "" {
		signal = ConversationSeed(body)
	}
	if signal == "" {
		signal = gjson.GetBytes(body, "previous_response_id").String()
	}
	return signal
}

// DeriveSessionID 提取信号、混入 scope、确定性派生 ses_ 会话号。
// 信号全空时随机生成, 与上游 DeriveRequestIDs 的最终回退一致。
func DeriveSessionID(headers http.Header, body []byte, scope string) string {
	signal := ExtractSessionSignal(headers, body)
	if signal == "" {
		signal = GenerateSessionID()
	}
	return ScopedSessionID(scope, signal)
}

// ConversationSeed 指纹对话开头: system/instructions + 首条 user turn。
func ConversationSeed(body []byte) string {
	var b map[string]any
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	prelude := make([]any, 0, 2)
	for _, field := range []string{"system", "instructions"} {
		if value := b[field]; value != nil {
			prelude = append(prelude, value)
		}
	}
	first, ok := firstUserTurn(b, &prelude)
	if !ok {
		return ""
	}
	encoded, err := json.Marshal(append(prelude, first))
	if err != nil {
		return ""
	}
	return string(encoded)
}

// firstUserTurn 返回首条 user 消息内容, system/developer 消息追加到 prelude。
func firstUserTurn(body map[string]any, prelude *[]any) (any, bool) {
	if input, ok := body["input"].(string); ok && input != "" {
		return input, true
	}
	for _, field := range []string{"messages", "input"} {
		arr, ok := body[field].([]any)
		if !ok {
			continue
		}
		for _, raw := range arr {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			switch role {
			case "system", "developer":
				*prelude = append(*prelude, item["content"])
			case "user":
				if content := item["content"]; content != nil {
					return content, true
				}
			}
		}
	}
	return nil, false
}

// UserTurn 指纹当前 user turn: user 消息数(跳过 tool_result) + 末条 content 哈希。
func UserTurn(body []byte) string {
	var b map[string]any
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	if input, ok := b["input"].(string); ok && input != "" {
		return turnFingerprint(1, input)
	}
	count := 0
	var last any
	for _, field := range []string{"messages", "input"} {
		arr, ok := b[field].([]any)
		if !ok {
			continue
		}
		for _, raw := range arr {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			if role != "user" || carriesToolResult(item["content"]) {
				continue
			}
			count++
			last = item["content"]
		}
	}
	if count == 0 {
		return ""
	}
	return turnFingerprint(count, last)
}

// turnFingerprint 用位置和 content 哈希标识一个 user turn。
func turnFingerprint(count int, content any) string {
	encoded, _ := json.Marshal(content)
	sum := sha256.Sum256(encoded)
	return strconv.Itoa(count) + ":" + hex.EncodeToString(sum[:8])
}

// carriesToolResult 判断 content 是否携带 tool_result 块。
func carriesToolResult(content any) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok {
			if t, _ := block["type"].(string); t == "tool_result" {
				return true
			}
		}
	}
	return false
}

// messageIDPattern 匹配官方客户端 msg_ ID。
var messageIDPattern = regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// UserMessageID 返回 v1 lane 的 x-opencode-request 值。
// 入站合法 msg_ 原样; 否则按 (session, turn) 钉住, 同一 turn 复用同一 ID。
func UserMessageID(existingMsg string, body []byte, session string) string {
	if messageIDPattern.MatchString(existingMsg) {
		return existingMsg
	}
	return userMessages.idFor(session, UserTurn(body), time.Now())
}

const (
	messageTTL        = 6 * time.Hour
	messageStoreLimit = 8192
)

type messageEntry struct {
	id   string
	seen time.Time
}

// messageStore 按 (session, turn) 钉住 msg_ ID, 6h TTL, 8192 上限, LRU 淘汰。
type messageStore struct {
	mu     sync.Mutex
	turns  map[string]messageEntry
	latest map[string]messageEntry
}

var userMessages = &messageStore{
	turns:  map[string]messageEntry{},
	latest: map[string]messageEntry{},
}

// idFor 返回钉住的 ID, 未知或过期时 mint 新 ID。
func (s *messageStore) idFor(session, turn string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := session + "\x00" + turn
	entry, ok := s.turns[key]
	if turn == "" {
		entry, ok = s.latest[session]
	}
	if !ok || now.Sub(entry.seen) > messageTTL {
		entry = messageEntry{id: GenerateMessageID()}
	}
	entry.seen = now
	if turn != "" {
		s.turns[key] = entry
	}
	s.latest[session] = entry
	if len(s.turns) > messageStoreLimit || len(s.latest) > messageStoreLimit {
		s.prune(now)
	}
	return entry.id
}

// prune 清理过期条目, 再裁到 3/4 上限。
func (s *messageStore) prune(now time.Time) {
	for _, entries := range []map[string]messageEntry{s.turns, s.latest} {
		for key, entry := range entries {
			if now.Sub(entry.seen) > messageTTL {
				delete(entries, key)
			}
		}
		for key := range entries {
			if len(entries) <= messageStoreLimit*3/4 {
				break
			}
			delete(entries, key)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
