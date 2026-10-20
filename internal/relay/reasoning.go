package relay

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 多轮 reasoning 回传缓存: DeepSeek/Kimi 等思考模型要求多轮对话把历史 assistant 消息的
// reasoning_content 回传, 否则 400; 部分客户端会剥离该字段。网关按 tool_call.id 缓存
// 成功响应中的 reasoning_content, 发现回传请求缺失时注入补齐。仅覆盖 OpenAI Chat 形状,
// anthropic/responses 客户端格式由转换层处理, 不在此处介入。

const (
	reasoningCacheCapacity = 2048          // 缓存最大条目数, 超容时淘汰最久未使用条目。
	reasoningCacheTTL      = 2 * time.Hour // 条目存活时间, 过期即视为未命中。
)

// reasoningEntry 是缓存中一条 tool_call.id -> reasoning_content 的映射。
type reasoningEntry struct {
	key       string
	value     string
	expiresAt time.Time
}

// reasoningCache 是互斥锁保护的 LRU+TTL 缓存。
// now 可注入以便测试替换时钟; Set 时惰性清理过期条目并按容量淘汰最久未使用条目。
type reasoningCache struct {
	mu       sync.Mutex
	now      func() time.Time // 时钟可注入, 测试用假时钟驱动 TTL 行为。
	ttl      time.Duration
	capacity int
	entries  map[string]*list.Element
	order    *list.List // 头部为最近使用, 尾部为最久未使用。
}

// newReasoningCache 构造指定容量、TTL 与时钟的缓存。
func newReasoningCache(capacity int, ttl time.Duration, now func() time.Time) *reasoningCache {
	if now == nil {
		now = time.Now
	}
	return &reasoningCache{
		now:      now,
		ttl:      ttl,
		capacity: capacity,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get 返回键对应的 reasoning 文本; 命中时刷新近期使用, 过期条目当场清除并视为未命中。
func (c *reasoningCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return "", false
	}
	entry := element.Value.(*reasoningEntry)
	if !entry.expiresAt.After(c.now()) {
		c.order.Remove(element)
		delete(c.entries, key)
		return "", false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

// Set 写入或刷新一条映射, 并顺带惰性清理全部过期条目与超出容量的最久未使用条目。
func (c *reasoningCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	expiry := now.Add(c.ttl)
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*reasoningEntry)
		entry.value = value
		entry.expiresAt = expiry
		c.order.MoveToFront(element)
	} else {
		c.entries[key] = c.order.PushFront(&reasoningEntry{key: key, value: value, expiresAt: expiry})
	}
	// 过期条目在链表上不保证连续(Get 会重排), 全量扫一遍即可, 容量有限代价可忽略。
	for element := c.order.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(*reasoningEntry)
		if !entry.expiresAt.After(now) {
			c.order.Remove(element)
			delete(c.entries, entry.key)
		}
		element = next
	}
	for c.order.Len() > c.capacity {
		element := c.order.Back()
		entry := element.Value.(*reasoningEntry)
		c.order.Remove(element)
		delete(c.entries, entry.key)
	}
}

// reasonings 是转发流程共用的全局回放缓存。
var reasonings = newReasoningCache(reasoningCacheCapacity, reasoningCacheTTL, time.Now)

// getReasoningFor 查询单个 tool_call.id 对应的 reasoning 文本。
func getReasoningFor(toolCallID string) (string, bool) {
	if toolCallID == "" {
		return "", false
	}
	return reasonings.Get(toolCallID)
}

// setReasonings 批量登记一次成功响应中提取的全部映射, 空 id 忽略。
func setReasonings(values map[string]string) {
	for id, text := range values {
		if id == "" {
			continue
		}
		reasonings.Set(id, text)
	}
}

// collectReasonings 从最终响应体提取 tool_call.id -> reasoning_content 并写入缓存。
// 只识别 OpenAI Chat 聚合形状 choices[].message.{tool_calls[].id, reasoning_content},
// 兼容尚未累积成 message 的 choices[].delta 原始形态(尽力而为); 解析失败或形状不符时
// 静默忽略, 不影响正常转发。仅 OpenAI Chat 客户端协议下由 handler 在请求成功后调用。
func collectReasonings(responseBody []byte) {
	choices := gjson.GetBytes(responseBody, "choices")
	if !choices.IsArray() {
		return
	}
	values := make(map[string]string)
	for _, choice := range choices.Array() {
		for _, message := range []gjson.Result{choice.Get("message"), choice.Get("delta")} {
			if !message.IsObject() {
				continue
			}
			reasoning := message.Get("reasoning_content")
			if !reasoning.Exists() || reasoning.Type != gjson.String {
				continue
			}
			toolCalls := message.Get("tool_calls")
			if !toolCalls.IsArray() {
				continue
			}
			for _, toolCall := range toolCalls.Array() {
				id := toolCall.Get("id").String()
				if id == "" {
					continue
				}
				values[id] = reasoning.Str
			}
		}
	}
	setReasonings(values)
}

// injectMissingReasonings 在每轮发送前按缓存补齐客户端剥离的历史 reasoning_content。
// 仅对 OpenAI Chat 客户端协议且渠道类型为 ChannelProviderOpenAI 生效。遍历
// role=="assistant" 且含 tool_calls 的消息:
// 首个 tool_call.id 命中缓存且消息尚无 reasoning_content 字段时注入; 已有值(哪怕空串)
// 一律不覆盖, 未命中不注入也不造占位符。同一 body 重复调用幂等, 不产生重复字段。
func injectMissingReasonings(body []byte, format llm.APIFormat, channelType model.ChannelProvider) []byte {
	if len(body) == 0 || format != llm.APIFormatOpenAIChatCompletion {
		return body
	}
	switch channelType {
	case model.ChannelProviderOpenAI:
	default:
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	next := body
	for index, message := range messages.Array() {
		if message.Get("role").String() != "assistant" {
			continue
		}
		toolCalls := message.Get("tool_calls")
		if !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		// 已有值(哪怕空串)一律不覆盖: 字段存在即说明客户端未剥离或已自行回填。
		if message.Get("reasoning_content").Exists() {
			continue
		}
		reasoning, ok := getReasoningFor(toolCalls.Array()[0].Get("id").String())
		if !ok {
			continue
		}
		updated, err := sjson.SetBytes(next, fmt.Sprintf("messages.%d.reasoning_content", index), reasoning)
		if err != nil {
			continue
		}
		next = updated
	}
	return next
}
