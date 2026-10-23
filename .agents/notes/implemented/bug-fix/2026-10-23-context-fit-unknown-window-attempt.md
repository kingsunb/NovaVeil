# Agent Note: 窗口未知的分组成员照常尝试

Status: implemented

> 窗口来源与上游拒绝处理由 [仅手动窗口参与选路且请求级不可用成员不冷却](2026-10-23-manual-window-only-and-infeasible-skip.md) 接管；未知手动窗口照常尝试仍有效。

## 问题

分组开启上下文过滤后，窗口未知（`effectiveContextWindow` 返回 0）的成员被当作「装不下」直接跳过。24 成员分组中只有两个手动标注 1M 窗口的成员会被尝试；它们失败进入冷却后，其余窗口未知成员仍不参与派发，请求最终无候选可用。

旧决定为了阻止 689K 请求打到实际只有 200K 的未知成员，选择了跳过未知窗口。线上故障表明这个代价不可接受：目录未同步或人工值遗漏会让绝大多数成员失去承载资格，而窗口偏小的成员本来可以由上游 400 明确判定。

## 决定

窗口未知的成员按正常优先级参与选路与派发，不放到最后，也不特殊排序。`contextFitsWindow` 对 `contextWindow <= 0` 返回 `true`，把「是否装得下」交给上游判定。

只有已知窗口且 `estimateModelTokens > window` 时才跳过。已知窗口不足的成员继续进入请求级 `contextSkipped`，并复用既有粘合清理、引用链占用归还与 `exclude` 逻辑。

`errContextWindowTooSmall` 只在所有可达候选都是已知窗口且全部装不下时返回。全未知窗口候选集的 `fitting` 大于 0，不会触发本地 400；`maxCandidateWindow` 仍只统计已知窗口。

## 备选方案

- **继续跳过未知窗口** — 能避免把超大请求先送到窗口偏小的未知成员；否掉是它会让未同步目录的成员在过滤开启时整体不可用，线上已出现全部候选耗尽。
- **把未知窗口成员放到最后尝试** — 能优先使用已知大窗口成员；否掉是用户明确要求按正常优先级尝试，且未知成员不应因元数据缺失被降级。
- **全未知候选集仍返回 400** — 能保持偏好设置不变；否掉是窗口未知不等于装不下，本地拒绝会重新制造本次故障。

## 后果

- **收益**：窗口元数据缺失不再阻断选路；目录未同步或人工值遗漏的成员可以立即承载请求；已知小窗口成员仍不会收到明确装不下的请求。
- **代价与已知上限**：超大请求可能先打到窗口偏小的未知成员，由上游 400 兜底并进入既有确定性错误路径；估算误差仍存在，窗口未知时无法在本地提前拒绝。

## 改动清单

- `internal/relay/context_fit.go:77-82`：未知窗口改为放行。
- `internal/relay/handler.go:183`：量化提示改为统计已知窗口候选的最大值。
- `internal/relay/context_fit_routing_test.go:235-291`：未知窗口优先命中用例，以及已知小窗口遇未知候选仍跳过的用例。
- `internal/relay/context_fit_test.go:86`：单元测试改为未知窗口返回放行。
- `internal/model/group.go:39`、`web-next/src/lib/types.ts:283`、`web-next/src/locales/zh-CN.ts:42`、`web-next/src/pages/Groups.tsx:1541`、`web-next/src/pages/ContextProbe.tsx:38,476`：同步「未知窗口照常尝试」文案。
- `docs/modules/relay.md:17`：补充路由生命周期中的未知窗口语义。

## 验证

- `GOTOOLCHAIN=go1.26.10 go test ./internal/relay/ -run 'ContextFit|ContextSkip|Context' -count=1`
- `GOTOOLCHAIN=go1.26.10 go test ./... -count=1`
- `npx -y tsx scripts/agent-notes/verify-agent-note-tree.ts && npx -y tsx scripts/agent-notes/verify-agent-note-format.ts`

## 已知上限

- 窗口未知的成员不会接受本地容量预判，超大请求可能增加一次无谓上游尝试。
- 上游未返回可识别的上下文超限错误时，仍按既有失败分类处理，不会由本地窗口过滤补判。
