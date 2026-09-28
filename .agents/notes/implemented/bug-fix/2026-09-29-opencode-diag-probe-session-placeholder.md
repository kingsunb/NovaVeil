# Agent Note: 测试与后台探测经专用参数注入合法 opencode 会话号

Status: implemented

## 问题

OpenCode 上游要求 `x-opencode-session` 是 `ses_` 加 12 位小写十六进制加 14 位字母数字的格式。真实转发路径从客户端会话解析出合法值再上送；面板测试/逐密钥/评估（`sendTestRequest`）与后台半开探测（`probeChannel`）没有客户端会话，此前靠 `injectRandomHeaders` 的共享 `randomValue` 占位来带这个头。

两个调用方给的占位值都不合法：`test.go` 用 `uuid.NewString()`（裸 UUID，被 `injectRandomHeaders` 写入占位会话头），`recovery.go` 传空串（`injectRandomHeaders` 对空串提前返回，头完全缺失）。OpenCode 上游因此返回 400 `MissingSessionID`：面板「测试模型/测试渠道」直接报错，后台半开探测恒失败、使冷却中的 OpenCode 渠道无法恢复。

## 决定

利用 `sendPassthrough`/`sendConverted` 已有的 `opencodeSession ...string` 变参（经 `applyResolvedOpencodeSession` 只对 `OpencodeCompat` 渠道写 `x-opencode-session`）单独注入合法会话号，**不改共享 `randomValue`**：

- `test.go` 的 `sendTestRequest` 保持 `randomValue := uuid.NewString()`（普通动态头同原行为），另铸 `opencodeSession := generateOpencodeSessionID()` 作为变参传入，发起前铸一次、全部重试复用。
- `recovery.go` 的 `probeChannel` 保持 `randomValue` 为空串（探测不注入共享随机头，同原行为），另铸 `opencodeSession` 作为变参传入。

这样合法 `ses_` 只落到 `OpencodeCompat` 渠道的 `x-opencode-session`，不污染 `x-trace-id` 等共享动态头；非 `OpencodeCompat` 渠道既不注入会话头，普通随机头也保持原值，零影响。

## 取代检查

- 部分重叠 [OpenCode 会话号原样上送](../feature/2026-09-22-opencode-session-and-prompt-cache-key.md)：仍是 `x-opencode-session` 注入与解析的权威来源；本次只修正「无客户端会话的探测路径」的会话号注入方式，互链不取代。
- 部分重叠 [渠道测试按生效协议模拟下游](2026-09-28-channel-test-lifecycle-and-native-protocol.md)：该篇记录测试路径复用真实动态头；本次补上「诊断/探测路径经 opencodeSession 变参注入合法会话号、共享随机头保持不动」这条约束，互链不取代。
- 活跃树没有 proposed / rejected 目录；没有完全吸收或需要归档的笔记。

## 备选方案

- **把共享 `randomValue` 直接改成 `generateOpencodeSessionID()`**：一行改动即可让占位会话头达标，但会把 `x-trace-id` 等普通动态头也变成 `ses_` 形状，非 `OpencodeCompat` 渠道的普通随机头从 UUID 变成 `ses_`。上游不校验这些头格式、功能无害，但扩大了影响面并违背「会话号不进入共享 `randomValue`」的既有设计，故改用专用 `opencodeSession` 参数。
- **只修 `test.go`，不修 `recovery.go` 的空串**：能修面板测试报错，但后台半开探测对 OpenCode 渠道仍因头缺失而恒失败，冷却渠道无法恢复，所以一并修。
- **在 `injectRandomHeaders` 内检测空串/非法值并自铸**：把「值是否合法」的判断塞进通用头注入函数，还会牵连非 OpencodeCompat 渠道的普通动态头；由调用方经专用参数保证会话号合法更贴契约。

## 后果

- **收益**：OpenCode 渠道的面板测试/逐密钥/评估不再因 `MissingSessionID` 失败；冷却中的 OpenCode 渠道后台探测能携带合法会话号、可正常恢复；非 `OpencodeCompat` 渠道的普通动态头与既不注入会话头，与修复前完全一致。
- **代价与上限**：占位会话号仍每次请求新铸（无客户端会话），跨多次测试/探测不共享同一会话，只影响 prompt cache 命中、不影响连通性。

## 验证

`go build ./...` 与 `go vet ./internal/relay/` 通过；`go test ./internal/relay/` 全部通过，新增 `TestOpencodeCompatProbesInjectValidSession` 同时断言 `OpencodeCompat=true` 注入合法 `ses_`、`OpencodeCompat=false` 不注入 `x-opencode-session`（覆盖面板测试与逐密钥两条路径）。