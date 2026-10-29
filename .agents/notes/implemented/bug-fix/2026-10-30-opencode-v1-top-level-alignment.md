# Agent Note: v1 档出站顶层黑名单与 max_tokens 补齐

Status: implemented

## 问题

nova 客户端经 v1 档转发时，顶层携带官方 1.x 基线没有的新式参数：`max_completion_tokens: 245104`、`store: false`、`prompt_cache_key: "session-…"`，且缺少旧式 `max_tokens`，顶层键序也与官方 1.19.5 实抓不一致。

2026-10-30 在 super 中转（自身即 OpenCode 免费档转发）上的对照实验证明：顶层参数**不触发**上游拒绝（10 键原序 + B 的 CRLF system + 41 工具返回 200），真正的必要条件是 system 换行符 CRLF 与工具总数 ≤约 70。尽管如此，为使 v1 出站顶层与官方 1.x 基线逐键一致，仍按维护者决定做参数收敛。

## 决定

`internal/relay/opencode_free.go` 的 `shapeOpencodeFreeBody` v1 档 chat 分支：

- 黑名单剥离客户端顶层 `store`、`prompt_cache_key`、`max_completion_tokens`；
- 缺 `max_tokens` 时补 `opencodeV1CompletionLimit = 32000`（官方实抓值），替换原「两字段皆缺才补 128000」的旧逻辑；
- 首条 system 内容换行符统一为 CRLF（`normalizeOpencodeSystemCRLF`，字符串与 text parts 数组两形态都处理）——实验证明 LF 必被上游拒绝，官方 Windows 客户端恒为 CRLF；
- 键序由既有 `normalizeOpencodeChatBody(v1ChatKeyOrder)` 承担，无新增逻辑。

作用域仅 v1 档（`/zen` 形态）chat 协议。v2 档不动：`max_completion_tokens` 是 v2 官方键序（`v2ChatKeyOrder`）的组成部分，v2 剥离会破坏官方形态；v2 的 system 换行形态未实测，不盲改。

## 备选方案

- **v1/v2 同步剥离**：`max_completion_tokens` 在 v2 是官方字段，同步剥离破坏 v2 官方形态，否。
- **保持客户端参数透传**：实验证明无咎，但出站顶层将与官方基线长期存在键差；维护者选择形态统一。
- **仅客户端侧修 CRLF**：治标不治本，任意 LF 第三方客户端经网关仍会被拒；网关整形处统一归一才是形态不变量。

## 后果

- **收益**：v1 出站顶层与官方 1.19.5 实抓逐键一致，system 换行形态与官方 Windows 客户端一致，`docs/modules/relay.md` 中的「客户端自带 `max_completion_tokens` 不归一」未对齐项消除。
- **代价**：客户端无法经 v1 档使用 `store` / `prompt_cache_key` 语义（实验证明上游不依赖它们）；仅发 `max_completion_tokens` 的客户端会被补上 `max_tokens: 32000`；system 中的裸 LF 被改写（文本语义不变）。
- **边界提示**：通过上游还须满足工具总数 ≤约 70（实测 ≥73 必拒，41 可过，边界 42~72 未细测）。网关自定义工具上限 33（满载总数 73）按实测已越线，维护者决定先测边界再收紧，本次不动。

## 验证

```sh
go build ./...
go test ./internal/relay/ -run 'TestShapeOpencodeFreeBody' -v
```

- `TestShapeOpencodeFreeBodyV1StripsNewStyleParams`：三字段剥离 + `max_tokens` 补 32000 + 客户端自带 `max_tokens` 保留。
- `TestShapeOpencodeFreeBodyV1SystemCRLF`：LF system 归一为 CRLF，已 CRLF 的保持无裸 LF。
- 部署后经 v1 档发一条带新式参数与 LF system 的请求，抓包确认出站为官方 8 键形态且 system 全 CRLF。
