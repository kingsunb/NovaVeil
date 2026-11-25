# Agent Note: OpenCode 免费档全局上下文窗口与上下文预估日志可见性

Status: implemented

> 估算口径与密度校准见 [上下文 token 密度改由真实流量校准、落库并删除探测页](2026-11-19-context-token-density-from-upstream-usage.md)。本文只记录窗口取值来源与日志展示。

## 问题

免费档（`isOpencodeFreeLane`：OpencodeCompat + zen 档）的可用上下文窗口，此前只能逐渠道手动填 `ModelLimits[name].ContextWindow` 才参与选路。免费档渠道多、模型多时逐个填窗口既繁琐又容易漏配；漏配后窗口为 0 被视为「未知直接放行」，实际上并不会拦截超长请求。

同时，日志里看不到一次请求的预估 token、生效窗口和密度来源。请求一进来，运维无法快速区分三种情况：上下文过滤没开、窗口没配（所以放行）、密度偏低或成员被跳过。这与此前「日志预估」的诉求一致。

## 决定

### 全局窗口只给免费档兜底

新增设置项 `opencode_free_context_window`（token，整数，0=不启用，上限 1000 万）。

- `effectiveContextWindow(channel, channelModel)` 的取值顺序：渠道手动 `ModelLimits` 值且大于 0 → 用它；否则若命免费档 → 用全局设置；再否则 0。
- 全局值只是窗口数值来源，分组仍以 `ContextFitEnabled` 作为唯一开关：开关没开，全局窗口填了也不过滤。
- 付费 OpenCode（`/zen/v1`、`/zen/go`）与其他渠道不受影响。`isOpencodeFreeLane` 本身排除 go 档，此处不变。
- 为避免单测依赖数据库与设置缓存，读取动作封装为可替换的包级函数 `opencodeFreeContextWindow`，默认实现走 `op.SettingGetInt`。

### 请求级摘要与每次尝试明细

请求状态新增请求级字段：`estimated_tokens`、`context_window_max`、`context_skipped`、`context_fit_state`（`disabled|unconfigured|skipped|fits`）。入口按默认密度估算 `estimatedTokens`；开启过滤时扫描后写入实际 `maxWindow` 与跳过成员数，关闭时写 `disabled`。

每次尝试（`AttemptRecord`）新增 `estimated_tokens`、`effective_window`、`density`、`density_source`（`default|learned`）与 `density_samples`（learned 时展示已观测样本数）。由 `observeRoundContext(channel, channelModel, body)` 在 `startRound` 前计算并带入 `RoundTarget`；`TokenDensitySnapshot` 增补 `Learned()` 与 `SampleCount()` 判定来源。

前端日志页在详情里增加「上下文预估」行（状态标签 + 生效窗口），并在每次尝试下增加「预估 token / 窗口 / 密度（默认|实测(N 条)）」一行。

## 备选方案

- 逐渠道/逐模型填窗口：工作量随免费档渠道数线性增长，且新渠道默认漏配，放弃。
- 用同步得到的 `ContextWindow` 参与选路：该值仅供展示，历史已明确不参与过滤，放弃。
- 把全局窗口做成所有渠道通用：会误改付费与其他渠道行为，与「只管免费档」的诉求冲突，放弃。
- 日志只给请求级汇总、不给尝试级密度：无法定位具体成员为何被跳过或密度偏低，粒度不够，放弃。

## 后果

- 免费档无需逐渠道配置即可获得一个统一窗口；渠道级手动值仍可覆盖。
- 全局窗口默认 0，等于维持旧行为（免费档窗口未知放行），零回归。
- 日志多出请求级与尝试级字段，均带 `omitempty`，旧数据与旧前端不受影响。
- 密度来源为 `learned` 时说明已由真实流量校准；`default` 表示仍用冷启动 `4/7.1`，可据此判断「密度低」是否只是尚未学习。

## 验证

- `go test ./internal/relay -run 'TestEffectiveContextWindow|TestContextFitState|TestObserveRoundContext|TestFreeLaneGlobalWindow'`
- `go vet ./internal/relay ./internal/op ./internal/model`
- `cd web-next && pnpm typecheck`
