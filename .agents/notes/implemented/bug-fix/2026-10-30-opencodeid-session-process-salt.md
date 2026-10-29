# Agent Note: 派生会话 ID 按进程加盐

Status: implemented

## 问题

免费层把会话 ID 挂在额度/风控记账上：同一会话被计费后，后续请求持续 429 `FreeUsageLimitError`，无论表单如何修改。核查本仓库会话铸造点发现，派生会话 `SessionIDFromSignal`（`internal/utils/opencodeid`）的哈希种子无盐——同一信号在同一 UTC 日（v2 按天锚）/同一小时（v1 按小时锚）内跨进程重启派生出**同一个**会话号。客户端侧表单修复（工具名 opaque 等）落地后，被记账的旧派生会话仍是 429 的独立来源。

参考 opencode2api 的同类修复（`internal/identity/request.go` 的 bootNonce）：进程启动时铸随机盐混入合成会话，重启即换新号，进程内保持稳定；官方客户端铸造的合法会话原样透传。该实现还踩过一个坑：合成会话内嵌时间戳若来自哈希会落在 1970，被免费层以 429 拒绝——本仓库派生号的时间锚（当天 UTC 日偏移 / 当前小时）本就合理，无此问题。

## 决定

`internal/utils/opencodeid/opencodeid.go` 新增包级进程盐：`sync.Once` + crypto/rand 16 字节（失败按仓库既有降级口径以纳秒时间填充），混入 `SessionIDFromSignal` 的哈希种子（`"ses\x00" + 盐 + "\x00" + signal`，与参考实现的 bootNonce 混入方式对齐）。效果：

- 同进程内同一信号恒定派生（时间锚与盐均不变），会话粘合、prompt cache 亲和不受影响；
- 跨进程重启盐重铸，同一信号的派生号全新，不再继承上一进程被记账的会话；
- 形态不变（v2 派生号低 12 位恒 `0xfff`，仍匹配官方 descending pattern），官方合法入站会话在函数首行原样返回，不受盐影响。

不动的部分：`GenerateSessionID`（crypto/rand + 调用方缓存，天然满足同进程稳定/跨重启不同）；`msg_` 消息 ID（逐消息语义，无上游长期记账证据，最小变更跳过）。

## 备选方案

- **在 relay 层清缓存代替加盐**：治标——重启后哈希仍确定，同日重启两次会派生出曾用号；盐在铸造源头消除重放。
- **msg_ 一并加盐**：无上游按 msg_ 长期记账的证据，违背最小变更。

## 后果

- **收益**：重启即摆脱派生会话的历史记账；与工具名 opaque 化共同消除 429 的两个身份触发点。
- **代价**：重启后同一对话的派生号改变，上游侧该会话的记账与 prompt-cache 亲和从零开始（预期行为）；正式转发的会话粘合靠 `X-Session-Id` 分组，与 ses_ 值解耦，不受影响。

## 验证

```sh
go test ./internal/utils/opencodeid/ -run 'TestSessionIDFromSignal' -v
go vet ./internal/utils/opencodeid/
```

`TestSessionIDFromSignalSaltedPerProcess`：同进程同信号恒定；改写盐字节后同信号派生新号且形态合法。
