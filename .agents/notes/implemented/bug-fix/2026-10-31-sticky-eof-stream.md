# Agent Note: 事件流 EOF 粘滞包装

Status: implemented

## 问题

`TestOpencodeConvertedKeepsClientContract/anthropic/v1/stream=false` 稳定 panic：`bufio.Scanner.Scan` 空指针，栈落在 `readStreamWindow` 第一次读取 `defaultSSEDecoder` 时。

根因有两层：

1. `github.com/looplj/go-sse` 的 `Parser.Next` 在读到 EOF 时会把内部 `inputScanner` 置为 nil，**之后再调用 `Next` 就会在 nil scanner 上空指针**；
2. axonhub 的 `anthropicInboundStream.Next` 在 source 返回 EOF 后，会入队合成的 `message_stop` 等收尾事件继续返回 true；队列排空后再次调用 source，就触发第 1 条的重复 EOF。

同协议透传不走 inbound 转换层，没有中间流重复读取，所以只有跨协议折叠路径会崩。

## 决定

- 新增 `stickyEOFStream`：首次 `Next()` 返回 false 后记录 `done`，后续调用直接返回 false，不再触达底层流。
- 只在 `conversionMiddleware.OnOutboundRawStream` 包装，即包在所有转换流的最底层；上层 Anthropic 入站的重复读取会得到稳定的 false，而不是二次访问已释放的 scanner。
- 不改 axonhub / go-sse 依赖，也不改折叠与窗口校验逻辑。

## 备选方案

- **升级 axonhub/llm 到最新**：核对过 `v0.0.0-20261029211818-b941556253f8`，`httpclient/decoder.go` 与 go-sse 版本均未变化，升级不能修复；且 `go.sum` 更新需要 Go 工具链，本轮无法执行。
- **在第三方解码器里加 nil 判断**：改依赖包，升级即回退，并且掩盖流生命周期契约。
- **折叠路径不读完、只读窗口**：会丢掉跨协议响应的后半段与终止事件，改变交付内容。

## 后果

- 收益：跨协议非流式折叠不再 panic；所有转换流在 EOF 后都满足“Next 可安全重复调用”的契约。
- 代价与已知上限：在底层流首次返回 false 之后，即使实现还想再吐数据也会被截断；标准 Stream 契约本就不允许该用法，因此不视为损失。

## 验证

```sh
go test ./internal/relay -run 'TestOpencodeConvertedKeepsClientContract' -v -count=1
go test ./internal/relay -run 'TestOpencodePassthroughChatKeepsClientContract|TestOpencodeConvertedKeepsClientContract' -v -count=1
go build ./...
```