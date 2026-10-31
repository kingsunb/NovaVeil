# Agent Note: 别名流透传上游 error 事件

Status: implemented

## 问题

`TestOpencodeIdentity_StreamFailuresAndClose` 失败并报 `failure swallowed`：上游在 SSE 里下发 `{"error":{"message":"failed","type":"upstream"}}` 时，身份别名流把它当成普通 JSON 事件继续排队，`Err()` 返回 nil，客户端会拿到一个静默截断的流而不是失败信号。

`analyzeStreamEvent` 只检查 `choices` / `stop_reason`，error 帧没有这些字段，因此原有判定识别不到。

## 决定

- `opencodeIdentityStream.transform` 遇到含非空 `error` 字段的事件直接返回错误，由 `Next` 收尾并写入流错误。
- `error: null` 的正常帧不受影响。

## 备选方案

- **依赖 `analyzeStreamEvent` 识别**：它按协议检查 choices，error 帧没有 choices，识别不到。
- **在 flush 阶段统一判断**：错误要等到流结束才暴露，尾部事件已经排进队列，失败语义出现得太晚。

## 后果

- **收益**：上游错误事件立即转成流错误，请求走既有失败/换目标路径，不再被当成空响应交付。
- **代价与已知上限**：error 字段非空的帧一律视为终止错误，若上游把可恢复告警塞进 error 字段会被当作失败。

## 验证

```sh
go test ./internal/relay/ -run 'TestOpencodeIdentity_StreamFailuresAndClose' -v
go test ./internal/relay/ -run 'TestOpencodeIdentity' -v
```