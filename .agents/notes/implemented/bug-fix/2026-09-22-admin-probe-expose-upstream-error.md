# Agent Note: 管理端探测端点暴露上游真实报错并附中文语义

Status: implemented

## 问题

`resp.Error` 对所有 5xx 统一兜底：真实 `err` 只进后端日志，客户端只收到共享常量 `ErrInternalServer`（"An unexpected error occurred"）。这层掩盖面向所有路径设计，本意是不向客户端泄露内部与上游细节。但渠道管理页的主动探测端点——「拉取模型」（`/channel/fetch-model`）、「测试连通」（`/channel/test`）、「逐 Key 测试」（`/channel/test_keys`）——上游失败时同样只回这句笼统英文：管理员看到「An unexpected error occurred」后无法判断是密钥无效（401/403）、上游限流（429）还是网络不可达，只能回头翻后端日志的 `http 500: ...` 行。这些端点是管理员会话内的排障入口，掩盖上游报错反而削弱了面板的诊断价值。

## 决定

新增 `resp.ErrorExposed`（`internal/server/resp/resp.go`）：与 `Error` 一样对 5xx 写 `log.Errorf` 并回 `{code, message}`，但**不替换** `message`，把真实 `err` 文案原样回给客户端。仅以下三个管理员探测端点改用 `ErrorExposed`，其余路径仍走 `Error` 兜底：

- `handlers.channel.go` 的 `fetchModel`（两处：回退已存渠道、按表单探测）
- `handlers.channel.go` 的 `testChannel`
- `handlers.channel.go` 的 `testChannelKeys`

三个端点回显的错误经 `probeErrorHuman` 统一加工成「中文语义 + 原始错误」：用 `upstreamHTTPStatusRe` 识别错误文案中的上游状态码（覆盖 helper 的 `upstream returned HTTP %d`、relay 的 `upstream responded %s` 与 httpclient 的 `with status %d` 三种形态），按 `httpStatusLabels` 映射中文（401→没有该密钥/未授权、403→无访问权限、429→触发限流、4xx/5xx 区间兜底），再拼接 `，原始错误：<原文>`。识别不到状态码时原样返回（此类错误本身多为中文或清晰网络错误文案，如「渠道未配置任何密钥」、`dial tcp ... no such host`）。

暴露面的边界：

- **只影响管理员会话**：这三个端点在 `/api/v1/channel/*` 路由组内，仅管理员可访问；普通 API 客户端走 `/v1/` 中转，用 `RelayError`（OpenAI 兼容错误体），不受影响。
- **报错内容本就有限**：`helper.FetchModels` 失败文案如 `upstream returned HTTP 401: <上游错误体前 512 字节>`，由 `readUpstreamErrorSnippet` 截断到 512 字节；`relay.TestChannel`/`TestChannelKeys` 的失败同为上游探测错误。不包含数据库密码等内部 SQL 细节（那些路径仍走屏蔽后的 `Error`）。
- **不使用 HTTP 状态码区分**：探测失败仍返回 500（语义是「这次探测失败」），真实原因放在 `message` 里，不改状态码，避免前端与旧客户端按状态码匹配的隐患。

批量模型同步 `syncChannel` 保持 `Error` 掩盖不变：它返回的是「首个失败渠道的聚合错误」，且会并发遍历多渠道，回显任意单渠道错误反而语义含糊，不在本次范围。

## 备选方案

- **把 `ErrInternalServer` 文案改成中文** — 最强论据是改动最小、用户看得懂；否掉因为它是全局共享常量，改中文会影响所有 5xx（含非管理员路径与非网页 API 客户端），且仍是笼统兜底，不暴露真实原因，对排障无实质帮助。
- **前端加中文兜底文案（`e.message || "拉取失败"`）** — 最强论据是纯前端不动后端契约；否掉因为后端 `message` 非空，兜底永远不触发，且照样看不到上游原因，治标不治本。
- **按上游原因映射到非 5xx 状态码（401/403/429 直返）** — 最强论据是客户端可程序化区分；否掉因为 `FetchModels` 返回的 error 不携带结构化状态码，上游码只存在于文案字符串里，解析文案反推状态码脆弱；探测失败统一 500 + `message` 带原因已满足管理员排障诉求。
- **让 `Error` 增加一个「是否暴露」布尔参数** — 最强论据是少一个导出函数；否掉因为会迫使所有现有调用点改签名且默认值语义易错（漏传 true 即泄露），独立的 `ErrorExposed` 把「暴露」做成显式、可 grep 的调用点。

## 后果

- **收益**：管理员在渠道编辑页点「拉取模型」/「测试连通」/「逐 Key 测试」时直接看到上游真实失败（如 `upstream returned HTTP 401: ...`、`dial tcp ... no such host`），无需再翻后端日志即可定位密钥、限流、网络三类问题。
- **代价与已知上限**：三个端点对管理员回显上游错误片段（最多 512 字节），与既有「密钥明文查看」「渠道代理明文查看」同为管理员可见性边界，不新增独立鉴权；若未来这些端点被非管理员角色复用，需重新评估是否降回 `Error`。`syncChannel` 仍返回统一兜底，多渠道路径聚合报错的最佳呈现另行处理。

## 验证

`internal/server/resp/resp_test.go` 新增 `TestErrorExposedKeepsInternalMessages`：以 500 调 `ErrorExposed`，断言 `message` 为原始上游文案且在 HTTP 401 样例下不回退到 `ErrInternalServer`；存量 `TestErrorSanitizesInternalMessages` 保证 `Error` 的屏蔽行为不变。`internal/server/handlers/channel_test.go` 新增 `TestProbeErrorHumanMapsUpstreamStatus` 表驱动：覆盖 helper/relay/httpclient 三种状态码文案形态、5xx 区间兜底、无状态码原样返回与网络错误不误判，断言中文前缀 + 原始错误原文完整保留在末尾。`go build ./...`、`go test ./internal/server/resp/... ./internal/server/handlers/...` 通过。