# Agent Note: opencode 身份层会话信号提取与 msg_ ID turn 钉住

Status: implemented

## Problem

与上游 opencode2api(57b906b) 对比发现 NovaVeil 的 opencode 身份派生存在三项偏差:

1. **会话信号提取不足**: 上游 DeriveRequestIDs 从 13 级回退链提取会话信号(10 显式 header/body 源 + conversationSeed + previous_response_id + random), NovaVeil 仅查 2 个 header 且仅接受 ses_ 格式。非标准客户端的会话标识丢失。
2. **scope 混淆缺失**: 上游 scopedSessionID 混入 config.Fingerprint(key) 做密码学隔离, 不同调用者不共享会话。NovaVeil 无 scope 混淆, 仅靠缓存键 apiKeyID 隔离。
3. **msg_ ID turn 钉住缺失**: 上游 UserMessageID 按 (session, userTurn(body)) 钉住 msg_ ID, 6h TTL, 同一 user turn 的多个请求(tool steps)共享同一 msg_ ID。NovaVeil 每请求 GenerateMessageID() 纯随机, 与真实 Bun 客户端行为不一致, 构成行为指纹差异。仅影响 v1 lane。

## Decision

### 新建 identity.go (internal/utils/opencodeid/identity.go)

从上游 identity/request.go + message.go 逐行移植, 适配 []byte body(gjson + json.Unmarshal):

- **ExtractSessionSignal**: 10 源提取(x-opencode-session, x-opencode-session-id, x-session-affinity, x-session-id, session_id, conversation-id, conversation_id, body.conversation_id, body.metadata.session_id, claudeCodeSessionID(body.metadata.user_id)) → conversationSeed(body) → previous_response_id。
- **DeriveSessionID**: 主入口, 信号全空时随机回退, 再 ScopedSessionID(scope, signal) 混入 scope 后确定性派生 ses_ ID。
- **ScopedSessionID**: 官方号原样保留, 其余 signal = scope + 0x00 + signal 后 SessionIDFromSignal(signal)。
- **ConversationSeed + firstUserTurn**: system/instructions + 首条 user turn 指纹。
- **UserTurn + turnFingerprint + carriesToolResult**: 跳过 tool_result 的 user 消息计数 + 末条 content 哈希。
- **UserMessageID**: 入站合法 msg_ 原样, 否则 messageStore.idFor(session, turn) 钉住。
- **messageStore**: 6h TTL, 8192 上限, LRU prune, 按 (session + 0x00 + turn) 钉住 msg_ ID。

### handler.go 调用替换

resolveOpencodeSessionHeader(2 header 检查) 替换为 opencodeid.DeriveSessionID(raw.Headers, raw.Body, callerScope)。callerScope = strconv.Itoa(apiKeyID) 或 "console"。

### opencode_free.go msg_ ID 替换

GenerateMessageID() 替换为 UserMessageID(request.Headers.Get("x-opencode-request"), request.Body, request.Headers.Get(opencodeSessionHeader))。

## Alternatives considered

- **在 random_headers.go 修改** — 信号提取需访问 body, random_headers.go 当前不读 body; 放 opencodeid 包更内聚。
- **不混入 scope, 仅扩展信号源** — 不混 scope 则不同调用者用同一信号派生同一 ses_ ID, 可能被上游关联。
- **msg_ ID 不钉住, 仅复用入站值** — 入站无 msg_ 时仍需生成, 不钉住则同 turn 多请求各异, 与 Bun 行为不符。

## Consequences

- **收益**: 会话信号提取覆盖 13 级回退链, 非标准客户端会话标识不再丢失; scope 混淆做密码学隔离; v1 lane msg_ ID 按 turn 钉住, 消除行为指纹差异。
- **代价与已知上限**: messageStore 进程内缓存, 重启后 msg_ ID 重新生成(与上游一致); 8192 上限在极端长对话可能触发 LRU 淘汰(与上游一致); conversationSeed 对大 body 做 JSON 序列化, 有轻微 CPU 开销。
- **不影响 v2 lane**: setV2Headers 不设 x-opencode-request, msg_ ID 钉住仅 v1 生效。

## Verification

```sh
go build ./internal/utils/opencodeid/ ./internal/relay/
go vet ./internal/utils/opencodeid/ ./internal/relay/
```
