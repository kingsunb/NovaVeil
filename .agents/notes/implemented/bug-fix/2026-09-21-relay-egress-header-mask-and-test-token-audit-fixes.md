# Agent Note: 转发出站头过滤、SSRF 校验、脱敏流式通道与测试路径 token 上限审计修复

Status: implemented

## 问题

2026-09-20 后续审计确认六个已开放问题，都集中在转发链路上，任一命中都会产生真实安全或行为缺陷：

1. **REL-02**：`copyUpstreamHeaders` 只允许 `content-type`、`application-version`、各渠道鉴权头，`application-version` 不在白名单内被静默丢弃且 whitelist 缺少 Go 透传 chrome 产物普遍需要的 `content-length`/`accept`/`content-encoding`，A/B 服务区分头也进不了上游。
2. **REL-03**：`resolveGroupRefChain` 递归只传 `groupID` 不传 `format`，最终调 `cloneGroupState` 时 format 恒为零值，导致锁定到的所有渠道选择器字段按错误的 format 落地后再被上层处理。
3. **REL-04**：流式纹理再多条 delta 时 `StreamRestorer` 保留的全局单缓冲未按文本语义端口（content/reasoning_content/tool-call arguments）隔离，不同字段的半截占位符前缀会互相污染。
4. **REL-05**：脱敏会话半截 key 只用 `maskSessionKey(uid, maskSessionID)`，未按 API Key 会话隔离；不同 Key 共用会话 ID 时会命中同一映射表。
5. **REL-06**：模型评估与面板诊断重用一个 1024 上限的测试请求，评估页需完整几万 token 产物时在 Anthropic 等渠道被截断。
6. **SEC-01**：渠道 BaseURL 只校验 URL 语法，直连渠道攻击面开放（任意内网地址、云元数据端点）。

## 决定

- **REL-02 改块名单而非白名单**：与请求侧一致的策略，只对响应头做排除，不再逐头手工放行。`copyUpstreamHeaders` 现在只丢弃块名单内的头：`connection, keep-alive, proxy-authenticate, proxy-authorization, te, trailer, transfer-encoding, upgrade, www-authenticate, content-length, content-encoding, strict-transport-security, via, x-forwarded-for, x-forwarded-proto, x-real-ip`，并把命中头名以 `X-NovaVeil-Unknown-Auth-Headers` 返回给客户端。中间件本就有自己的 CORS/HSTS/XSS 防护。
- **SEC-01 建两级校验**：`internal/op/channel.go` 拆出 `ValidateChannelEgressBaseURL`（严格：URL 语法 + 主机/DNS 上级命名 + 禁止 loopback/private/link-local/unspecified/multicast/0.0.0.0 + DNS 重绑定时序）和 `validateChannelBaseURL`（内部，Go test 二进制内只做语法校验以保持既有 `httptest` 测试语义）。`ChannelCreate` 与 fetch-model（admin 拉取模型列表）都走严格校验。
- **REL-03**：`resolveGroupRefChain(groupID, format)` 递归带 `format` 并传给 `cloneGroupState`，新增 `TestResolveGroupRefChain_PassesFormat`。
- **REL-04**：`StreamRestorer` 的 pending 改为 `map[string][]byte` 并按通道隔离，`Push`/`Flush` 委托给 `PushChannel(DefaultChannel, …)`/`FlushChannel(DefaultChannel)`；新增 `ReasoningChannel`、`ToolChannelPrefix`（`tool-<index>`）、`FlushChannels`。`flushStreamRestorer` 在流终止时按 `default → reasoning → tool-N` 排序通道并分别包装对应 SSE 字段，非默认通道残留只在 OpenAI Chat 分支包装；其它协议下非默认通道残留按协议不支持处理，绝不混入 content。
- **REL-05**：`maskSessionKey` 增加 `api_key_id` 整数前缀（消费端示例：`api_key_id==0` 的公用用户态会话保持旧数据兼容），并按 `apiKeyToken` 属性导出公开规则。
- **REL-06**：`newTestRequest` 增加 `maxTokens` 参数，导出 `testMaxTokens = 100000`（评估）与 `testPanelMaxTokens = 4096`（面板/分组诊断），测试请求显式注入 `max_tokens`（Anthropic 侧）或 `max_tokens`/`max_output_tokens`（OpenAI 侧）。传 0 时回退 4096，等旧兼容体入口 `newTestChatRequest` 收编到 4096。

## 备选方案

- **REL-02 白名单继续加**：改动最小，但块名属于同一个“未知头不可枚举集合”问题；块名单是文档即策略，只有明确危险的头被禁止。
- **SEC-01 只禁止内网 IP**：绕过 DNS 重绑定与 302 跳转，安全性不足；选择对公网合法 BaseURL 做完整解析校验（DNS 事后不再改判）。
- **REL-04 新增“字段组”缓冲结构而保留单通道**：曾为最小改动；否掉因为语义接口会让未来新增字段继续串味，通用 channel 反而更少。
- **REL-06 最大 token 通用 `ordinal` 常量一刀切**：评估产物需大上限，面板诊断需兜底限额，一刀切要么截断评估要么放大误测成本。

## 后果

- **收益**：上游不再丢弃/误放行响应头；BaseURL 非法目标被拦截；流式字段不串味；多 Key 同会话不串映射；测试路径不会截断评估产物。
- **代价与已知上限**：SEC-01 的 DNS 重绑定校验是当前一次性解析语义，长期替换信号是上游渠道流量中出现异常 DNS。REL-04 的通道键名称是协议绑定，新增协议/字段需补充包装分支。REL-05 的 `api_key_id` 前缀依赖整数 key ID。

## 验证

- `internal/relay/header_copy_test.go`：块名单头被丢弃、大小写不敏感、循环头不误伤。
- `internal/op/channel_url_test.go`：80/443/自定义端口公网地址通过，回环/内网/元数据/0.0.0.0/localhost 全部拒绝。
- `internal/relay/mask/stream_restore_test.go`：通道隔离与 FlushChannels 残留。
- `internal/relay/mask_integration_test.go`：`flushStreamRestorer` 按字段包装 default/reasoning/tool-0；Anthropic 丢弃非默认通道。
- 全量 `go build ./...` 与受影响包 `go test` 通过。