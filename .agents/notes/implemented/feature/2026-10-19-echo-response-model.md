# Agent Note: 聚合分组回显请求模型名，不透传真实上游模型名

Status: implemented

## 问题

NovaVeil 的聚合分组（如 `auto`）把客户端请求的分组名当成模型名接收，在请求方向改写成真实成员模型名发往上游（`handler.go` 的 `sjson.SetBytes(raw.Body, "model", channelModel.Name)`），但响应方向原样透传：非流式 `c.Writer.Write(result.body)`、流式逐事件 `sse.Encode` 都不碰响应里的 `model` 字段。于是真实上游模型名（如 `space-bunny-free`）一路泄回客户端。

下游网关（如 new-api）记录"请求模型=auto、上游请求模型=auto、响应模型=space-bunny-free"，触发"响应模型不一致"诊断告警，并把真实模型名展示在用量日志里。管理员不希望聚合分组暴露内部选中的真实模型。

## 决定

在 `GroupRelayConfig` 增加按分组开关 `EchoResponseModel bool`（JSON `echo_response_model`），默认关闭：零值 `false`，旧分组 JSON 反序列化自动得关，无需数据迁移，`DefaultGroupRelayConfig` 不显式列出。开启后，在响应写给客户端前把 `model` 字段改写回客户端请求的分组名（`request.Model`）。

接入点两处，均在脱敏还原之后、字节写出之前：

- 非流式 `handler.go`：`restoreNonStream` 之后、`Content-Length` 校验与 `c.Writer.Write` 之前。
- 流式 `handler.go`：`restoreStreamEvent` 之后、`chunks = append` 与 `sse.Encode` 之前。

改写按**客户端协议**（`format`，即客户端入口协议）定位字段路径，因为写给客户端的响应始终是客户端协议形态：

- **非流式**：OpenAI Chat / Anthropic Messages / OpenAI Responses / Embedding 的非流式响应均用顶层 `model`，统一 `sjson.SetBytes(body, "model", …)`；不含 `model` 的响应体（图片/音频等）原样返回，不凭空注入。
- **流式**：OpenAI Chat 每 chunk 的顶层 `model`；Anthropic `message_start` 里的 `message.model`；Responses `response.*` 事件里的 `response.model`；Embedding 不流式。只改写已存在字段，`[DONE]`/`ping`/`content_block_delta` 等无 model 的事件原样返回。

Gemini（`APIFormatGeminiContents`）只作上游渠道类型、不是客户端入口协议（`Forward` 仅接 Chat/Responses/Anthropic/Embedding/图片/音频/视频），客户端响应里不会出现 Gemini 的 `modelVersion`，故无需处理。合成终止帧（`terminalStreamFrames`）的 Chat model 为空串、Anthropic/Responses 无 model 字段，不泄露真实名，不改写。

实现集中在 `internal/relay/echo_model.go`（`echoNonStreamResponseModel` / `echoStreamResponseModel`），用 `gjson` 判存在 + `sjson` 点状改写，与仓库既有改写手法一致。

## 备选方案

- **在下游网关（new-api）侧改写响应 model** — 最强论据是告警与展示都发生在 new-api，就近改最直接；否掉因为 new-api 的 `relay/common/response_model.go` 注释明确"diagnostic only: it must never change … downstream output"，设计上只观察不改写，没有响应方向替换开关，其 `model_mapping` 是请求方向替换；硬改违背其设计哲学且 new-api 是受保护项目。聚合方回显请求名才是聚合网关的标准职责。
- **只在非流式改写、流式仅改首 chunk** — 最强论据是开销最小且足以让下游诊断器看不到不一致；否掉因为后续 chunk 仍带真实名直达终端客户端，泄露未真正闭合，半 measure。
- **不加开关、全局默认开启** — 最强论据是一劳永逸隐藏真实模型；否掉因为存在需要透传真实模型名的合法场景（调试、按真实模型计费展示），按分组 opt-in 更可控，且默认全关保证不影响存量流量。

## 后果

- **收益**：开启分组不再向下游与终端客户端泄露内部选中的真实上游模型名，下游"响应模型不一致"告警随之消失；按分组粒度可控；出厂全关不影响存量。
- **代价与已知上限**：开启后 OpenAI Chat 流式逐 chunk 多一次 `gjson`+`sjson`（约 1–2µs/chunk），长流累计百微秒级，被秒级 LLM 延迟淹没，其余协议仅改写携带 model 的单个事件。重访信号：上游新增携带 model 的 SSE 事件类型、或极端高 QPS 短流场景下需加字节级快速路径。

## 验证

`internal/relay/echo_model.go` + `echo_model_test.go`（四协议非流式/流式、`[DONE]` 与无 model 事件不改写、空 echoModel 与无 model 体不注入）；接入点 `internal/relay/handler.go` 非流式与流式两处；开关字段 `internal/model/group.go` `GroupRelayConfig.EchoResponseModel`。`go test ./internal/relay/ ./internal/model/` 通过。
