# Agent Note: 错误文本按去向分流：后台原文、下游脱敏

Status: implemented

## 问题

错误文本同时服务两类读者：管理端状态、SSE、失败摘要与错误日志用于排查，需要完整上游原文；下游客户端只应看到脱敏后的错误。此前在写入点统一脱敏，后台日志丢失 Authorization、API Key 等原始上下文，排障时还要从上游响应体反推。原决定见 [2026-11-10-backend-consistency-hardening.md](2026-11-10-backend-consistency-hardening.md)。

## 决定

- relay 状态、SSE、失败摘要、测试探针与错误日志的 `ErrBrief`/`ErrDetail` 保留原始错误，仅按既有上限截断。
- 仅 `rejectRequest` 生成下游响应时调用 `op.RedactSensitiveText(err.Error())`。
- `error_logs.RequestBody` 继续结构化脱敏；`APIKeySuffix`/`MaskMatches` 维持归一；`ClassifyError` 使用原始错误。
- 上游 8KB 错误片段进入后台原文；下游请求级错误统一脱敏。

## 备选方案

- **全链路脱敏**：安全但后台失去排障上下文，已证明不可用。
- **全链路原文**：排查方便，但会把上游回显的凭据带给下游。
- **只在 JSON 序列化脱敏**：覆盖不到失败摘要、SSE 与错误日志，且正则容易漂移。

## 后果

- 后台与日志可见原始敏感内容，依赖管理端鉴权与日志访问控制；下游只经 `rejectRequest` 脱敏。
- 脱敏仍是模式匹配，未命中已知字段名的秘密可能进入下游错误文本。
- 请求体仍脱敏，后台无法从 `error_logs` 看到完整 prompt；需要原文时应查上游原始响应或抓包。

## 测试

- `internal/relay/state_redaction_test.go`：`markFailed`/`markCanceled`/`finishRound` 保留后台原文，`downstreamErrorText` 脱敏。
- `internal/op/error_log_redact_test.go`：`sanitizeErrorLog` 保留 `ErrBrief`/`ErrDetail`，`RequestBody` 仍脱敏。

## 验证

```sh
go test ./internal/relay ./internal/op -count=1
```
