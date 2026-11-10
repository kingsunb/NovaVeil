# Agent Note: 账号体检不进用量统计，只保留日志

Status: implemented

## 问题

账号体检以真实上游请求探测免费档账号，`finishTestRequest` 原先在 `AccountProbe` 为 true 时把上游真实上报的 usage 写入 `usage_buckets`，与真实转发同口径计 token 与 `request_count`。面板测试、逐 Key 测试、分组测试、模型评估和上下文探测都不写入，`docs/modules/relay.md` 与 [渠道测试生命周期](2026-09-28-channel-test-lifecycle-and-native-protocol.md) 也声明探针不计入用量分桶，账号体检因此成为唯一例外。

后果是同一类诊断请求在 24h/7d/30d 窗口档抬高请求数与 token；`forever` 档的请求数取自进程累计 `relay.TotalRequestCount`（只算真实转发），token 却来自 `usage_buckets`，于是出现「请求数没涨、token 涨了」的口径撕裂。用户要求账号体检完全不进统计，只显示在日志。

## 决定

`internal/relay/test.go` 的 `finishTestRequest` 不再为 `AccountProbe` 调用 `op.RecordUsageBucket`；`usage_buckets` 只由真实转发的 `finish()`（`internal/relay/state.go:672`）写入。

账号体检仍复用 `startTestRequest` / `finishTestRequest`：日志行、运行态、尝试轨迹、失败摘要、请求/响应正文与 `request.Usage` 展示都不变，`ClientIP` 仍标记「账号体检」以区别于「面板测试」。`AccountProbe` 字段保留，只作为来源标记，不再驱动任何计账。

`sanitizeUsage` 的结果仍写 `request.Usage` / `UsageEstimated` 供日志展示；估算值本来就不进分桶。

## 备选方案

- 保持按实时请求计账：能让体检 token 出现在成本视图，但诊断流量不是业务消耗，会污染模型/渠道用量和请求数，且与既有文档和测试边界自相矛盾。
- 体检只计请求数不计 token：仍会抬高窗口档 `request_count`，还要新增一套部分计账分支，口径更碎。
- 体检 token 记到独立「体检」模型名下：能保留成本可见性，但需要新增模型维度与前端过滤，超出用户「只在日志看」的要求。

## 后果

仪表盘、渠道统计与用量分桶不再包含账号体检；体检的 token、耗时和失败仍可在日志列表与详情查看。失去按体检观察上游免费额度的统计视图；需要时直接查日志详情。`forever` 档与窗口档请求数口径的既有差异仍在，但不再被体检放大。

## 验证

新增 `internal/relay/account_probe_stats_test.go`：账号体检调用 `finishTestRequest` 后 `usage_buckets` 无目标模型记录，且 `request.Usage` 保留展示值。

静态检查：`grep -rn "RecordUsageBucket" internal/relay` 只剩真实转发一处。

本仓库禁止 AI 执行构建与测试，以下命令由用户执行：

```sh
go test ./internal/relay -run TestAccountProbeOnlyLogsNotUsage -count=1
go test ./internal/op -run 'TestRecordUsageBucket' -count=1
pnpm verify-notes
```
