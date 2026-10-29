# Agent Note: 分组级拒绝标题类轻请求

Status: implemented

## 问题

dsh 一类客户端每次会话会额外发一次标题生成请求：单轮、`role: developer`、不带工具，正文形如 `Create a concise title ...` 加 `Generate the session title: ...`。OpenCode Zen 免费档的额度按出口 IP 计且先到先得，一次 `dsh run` 会消耗 title + main 两份额度，标题请求直接吃掉一半。

上游无法区分这类请求：免费档整形会给每个请求注入官方系统提示与 11 个工具（`internal/relay/opencode_free.go` 的 `shapeOpencodeFreeBody`），整形之后标题请求与主请求同形。所以识别只能在网关入口、用客户端原始正文做。

## 决定

### 识别（`internal/relay/title_request.go`）

两个前置条件：`messages` 是数组且非空、客户端未声明 `tools`（缺字段或空数组）。满足后按标记分两档判定：

- **指令型标记**（`you are a title generator`、`output only a thread title`、`generate a title for this conversation`、`generate the session title`、`create a concise title`）出现在 `system` 或 `developer` 消息里即判定，**不看消息条数**。有的客户端把整段会话作为额外的 user 消息塞进标题请求（system 指令 + 一句诱导语 + 会话正文共三条），条数不是可靠判据。
- **泛用标记**（`session title`、`concise title`、`title generator`）出现在任意消息、且 `messages` 条数 ≤ 2 时判定。这一档兜住把标题措辞写在 user 消息里的客户端，条数限制避免长篇对话里偶然出现的「标题」措辞被误判。

指令型标记限定系统角色，避免 user 真让模型「写个标题」这类内容生成请求被拦。消息文本兼容 `content` 为字符串与 content parts 数组两种形态。

### 开关与判定位置

分组级开关 `reject_title_requests`（`GroupRelayConfig.RejectTitleRequests`，默认 false，旧分组 JSON 反序列化为关）。判定在 `internal/relay/handler.go` 的 `Forward` 里，位于「分组存在性校验」之后、「登记请求状态」之后、「脱敏」之前：

- 命中即 `markFailed` + 以 400 回给客户端，**不发起任何上游调用**
- 不写错误日志：标题请求数量多，落库只会挤掉真实渠道错误
- 判定必须早于整形，因此读的是 `raw.Body`（客户端原文）

因为判定在入口而不是在选成员之后，开关只认顶层分组（请求模型名对应的分组），引用链子分组的开关不生效——与上下文窗口过滤的生效范围不同。

### 错误类别

`ErrClass` 新增 `client_rejected`，与 `channel_busy` 同属本地准入拒绝：未发起上游调用，不代表任何渠道健康度。前端日志页的分类筛选与 `ErrClass` 联合类型同步补上。

### 前端

分组编辑器「路由策略」页在「上下文窗口过滤」之后新增第七个开关「拒绝标题类请求」，描述里点明客户端标题生成会失效。

## 备选方案

- **把标题请求改写到小模型分组**：标题功能保留，但要新增模型改写层，响应里的 `model` 会变，请求日志的模型归因失真，API Key 的模型白名单校验顺序也得重排。改动与代价都高于直接拒绝，本轮不做。
- **只在免费档跳过成员，让同组付费成员接住**：语义最精确，能同时做到「免费档不烧额度」和「标题仍可用」，但要接入选路层的跳过集合（`contextSkipped` 那套）与全跳过时的专用终态，还要处理粘合成员与非免费成员混排。等真的出现「想留标题」的需求再做。
- **客户端 `small_model`**：OpenCode 原生支持把标题生成指到另一个模型，零改动；但 dsh 没有等价配置，只能解决一半，因此作为并行建议而非本仓方案。
- **用自定义渠道的固定回复顶替标题请求**：零上游成本，但标题恒为同一句假数据，且属于伪造成功响应。
- **用 `max_completion_tokens` 小值作为判据**：dsh 标题请求传 4096、主请求传 256000，但该字段随客户端版本变化，不适合当契约。
- **把条数上限从 2 提到 3**：改动最小，但判据仍是「数消息」——客户端换个模板把会话拆成几条就漏，不如直接认「系统提示声明了自己在干什么」。

## 后果

- **收益**：开启该开关的分组上，标题请求零上游消耗、零额度占用。
- **代价**：该分组的客户端标题生成拿 400，功能失效；若客户端会重试，日志里会出现多条 `client_rejected`，据此判断是否需要去重。
- **误伤边界**：无工具、正文含标题标记的真实提问会被拒；system 提示里写了「生成标题」字样的真实会话同样会被拒；带工具的编码请求不受影响。
- **默认关闭**：存量分组行为不变，无需数据迁移。

## 验证

```sh
go test ./internal/relay/ -run 'TestIsTitleGenerationRequest|TestTitleRequest' -v
```

- `TestIsTitleGenerationRequest`：判定边界（系统指令跨条数命中、user 角色措辞不越界、parts 正文、空 tools、超两条消息、声明工具、无 messages 字段）。
- `TestTitleRequestRejectedByGroupPolicy`：开关打开时 400 + 专用提示，假上游调用次数必须为 0。
- `TestTitleRequestForwardedWhenGroupPolicyOff`：开关关闭时同一请求照常转发。
- `TestTitleRequestWithToolsForwarded`：开关打开但声明了工具时按普通请求转发。
