# Agent Note: 脱敏零命中 JSON 不再因重序列化误标「已脱敏」

Status: implemented

## 问题

脱敏开关开启且至少一条规则生效时，`applyRequestMask`（`internal/relay/mask_integration.go`）用 `bytes.Equal(masked, body)` 判定「是否真的脱敏」：字节未变 → 返回 nil 映射；字节变了 → 返回非 nil 映射与命中明细。但 JSON 请求体在引擎 `Apply` 里走 `mapJSONStringValues` 逐 token 重序列化（紧凑重建、`writeJSONString` 用 `json.Marshal` 做 HTML 转义），**即使一个字符串都没命中**，重序列化后的字节也可能与原始 body 不同——最典型是去空格（`{"a": 1}` → `{"a":1}`）、转义 `<`/`>`/`&`。于是：

- `bytes.Equal(masked, body)` 为 false → 返回非 nil 映射，`handler.go` 里 `maskMapping != nil` → `request.Masked = true`，日志详情展示「已脱敏」；
- 但 `res.Matches` 为空 → `truncateMaskMatches` 返回 nil → `mask_matches` 命中明细为空；
- `raw.Body = masked` 还把请求体悄悄改写成了重序列化后的格式，即使无需脱敏。

统一表现为用户看到的 bug：**日志显示「脱敏」、命中明细无内容、实际没脱敏**，且只出现在 JSON 请求体没有命中任何规则的部分请求上（纯文本零命中时 `maskText` 原样返回，不触发）。

## 决定

`applyRequestMask` 判定「是否真的脱敏」改为以 `len(res.Matches) == 0` 为准，而不是 `bytes.Equal(masked, body)`。依据是引擎契约 `maskCandidates`：候选收集后按贪心选中非重叠区间，首个非重叠候选必被选中并把命中写入 `matches`（`!seen[orig]` 分支），因此「发生了替换」与「命中明细非空」严格同真同假，用命中数判定不会漏报也不会误报。

零命中时返回**原始 `body`**（不是重序列化后的 `res.Masked`）+ `nil` 映射 + `nil` 明细，`handler.go` 据此跳过 `Masked` 标记与还原器，请求体逐字节透传；命中时仍返回 `[]byte(res.Masked)` + `res.Mapping` + `res.Matches`。同时移除不再使用的 `bytes` import。

此处的「命中」口径与 [请求体预算、会话隔离、脱敏表容量与污染流截断](2026-09-22-relay-body-budget-sticky-mask-stream.md) 中「这次请求有命中才把映射交给还原」一致：即以 `res.Matches` 非空为准，不再以字节是否变化为准。

## 备选方案

- **让 `mapJSONStringValues` 零命中时回退返回原始字节** — 最强论据是从源头消除字节漂移，输出与输入逐字一致；否掉因为它仍需要一个「本次是否命中过」的返回信号（要么改 `mapJSONStringValues` 签名，要么在外层再比较），而「是否脱敏」的语义本来就该落在命中明细上，字节漂移只是表象；重序列化开销已经发生，回退多一轮判断不解决问题。
- **保持 `bytes.Equal` 判定，改成 JSON 语义相等比较（解析后比较）** — 最强论据是保留旧判定的形状；否掉因为引入完整 JSON 语义相等成本高，且仍解决不了「零命中时也改写了请求体字节」的副作用，治标不治本。
- **在 `handler.go` 里改用命中明细是否为空来设 `Masked`，不动 `applyRequestMask`** — 最强论据是改动更局部；否掉因为 `Masked` 与 `streamRestorer`（用映射表建还原器）强绑定，而且 `raw.Body = masked` 仍会把重序列化字节写入出网请求体，隐患依旧，只有把判定和「透传原始字节」都收在 `applyRequestMask` 出口处才内聚。

## 后果

- **收益**：零命中 JSON 请求不再误标「已脱敏」，「已脱敏」标记与命中明细从此严格同真同假；零命中时请求体逐字节透传，网关不再因脱敏而改写请求体格式（去空格、转义 `<>`& 等），对下游签名/校验类上游更安全。
- **代价与已知上限**：判定口径从「字节变化」收窄为「命中明细非空」。当前引擎保证「有替换必产生 Match 记录」，且 relay 层的 `truncateMaskMatches` 只裁剪下发展示、不改变 `res.Matches` 长度，因此没有「替换了但零命中」的路径；未来若新增一条「替换文本却不记 Match」的脱敏路径，必须同步重评这个判定，否则会退化为「已脱敏但命中明细为空」的旧症状。

## 验证

`internal/relay/mask_integration_test.go` 的 `TestApplyRequestMaskContract` 新增子测试 `zero-hit non-compact JSON returns original bytes and nil mapping`：用带空格 JSON 与含 `<tag> &` 的字符串值两个样例，断言零命中时返回原始字节、nil 映射、nil 命中明细。临时验证程序确认根因：对带空格 JSON 以规则开启态调用 `mask.Apply`，得到 `matches len = 0` 且 `bytes changed = true`（重序列化去空格）。`go test ./internal/relay/... ./internal/op/...` 与 `go vet ./...` 通过；`docs/脱敏开发/README.md` 第八节第 4 条「短路语义」同步更新判定口径。