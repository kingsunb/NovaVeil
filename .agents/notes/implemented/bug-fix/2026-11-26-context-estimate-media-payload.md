# Agent Note: 上下文预估剔除媒体载荷并按固定值计

Status: implemented

## 问题

图片/音频/视频以 base64 或 data URI 塞在请求体中，而 `estimateRequestTokens` 把整段 JSON 当文本按「ASCII 4 字符 1 token」计数，带来两个后果：

- 预估严重虚高：一张 64 KB 图片的 base64 约 8.7 万字符，被估成约 2.2 万 token，上游真实仅数百到千余 token。
- 密度被污染：`observeTokenDensity` 用同一基准当分母学习密度，图片请求的 `prompt ÷ base` 极小，常被夹到 0.25 下限；攒够样本后，同一「渠道:模型」的纯文本预估也会被拖偏。

## 决定

- `estimateRequestTokens` 与 `estimateRequestTokensText` 改为：先按原字符宽度计数，再用 gjson 递归识别媒体块，把媒体载荷（base64 或 URL 字符串）的字节从窄字符计数中扣除，最后每个媒体按固定 `mediaTokenCost = 1000` token 折算回基准（`mediaBaseEquivalent = round(1000 / defaultTokenDensity)`）叠加。
- 识别范围：OpenAI Chat `image_url`、OpenAI Responses `input_image`、Anthropic `image.source.data`、`input_audio`、`audio_url`、`video_url`，以及 Gemini `inline_data` / `inlineData` / `file_data`；命中媒体块即不再深入内部，避免嵌套重复计数。
- 非 JSON 请求体（首字符非 `{` / `[`）保持原字符计数；纯文本 JSON 口径不变。
- 路由过滤、拒绝文案、日志兜底（`sanitizeUsage`）与密度学习共用同一基准，四处随之同步修复。

## 备选方案

- **只剔除 base64、媒体不计 token**：最省事，但会固定低估约 1K，且图片请求观测密度偏高，反而污染密度，未选。
- **解码图片按宽高与模型公式估算**：更接近真实，但要解码大图、按模型分叉公式，开销与复杂度高，未选。
- **引入本地 tokenizer（tiktoken 等）**：各上游分词器不同、依赖重，对「是否明显超限」收益有限，沿用既有否决策略。
- **只改路由预估、不改 `estimateRequestTokensText`**：密度学习仍拿含 base64 的分母，污染继续，未选。

## 后果

- **收益**：媒体请求预估从数万降到约 1K/张，且不随图片体积变化；图片请求不再把密度拉到下限，纯文本预估不被连带拖偏；四个出口同源修复。
- **代价与已知上限**：每媒体 1000 是粗估，大图或视频可能偏低；仅识别白名单媒体类型与字段，未知形态可能漏扣；固定值不按模型分辨率差异调整。

## 验证

- `go test ./internal/relay/ -run 'TestEstimateRequestTokens' -count=1`：含媒体用例通过，且媒体载荷大小不影响结果（`TestEstimateRequestTokensMediaIndependentOfPayloadSize`）。
- `go test ./internal/relay/ -run 'Estimate|ContextFit|SanitizeUsage|Density' -count=1` 与 `go test ./internal/op/ -run TestTokenDensity -count=1` 通过，纯文本既有口径未变。
