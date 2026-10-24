# Agent Note: OpenCode 免费档 429 冷却代理账号 24 小时并换号

Status: implemented

## 问题

OpenCode 免费档的 `{account}` 是出口代理账号，不是真正的上游账号；内置 Key `public` 也不是可轮换的用户凭据。上游返回 429 表示当前出口触发了限速或配额，继续用同一个出口重试只会反复失败。

旧实现把 429 当作 Key 限额：多 Key 渠道先冷却当前 Key 并轮换，单 Key 渠道则落回成员失败计数与冷却。两种路径都没有立即换出口，还会把出口问题扩散成 Key 或成员故障。

## 决定

- 403 与 429 都视为代理账号问题，但使用不同冷却时长：403 为 `ocProxyCooldown = 12h`，429 为 `ocProxyRateLimitedCooldown = 24h`。429 取 24 小时是显式业务口径，用一天量级阻止被限速出口继续参与当天轮换；不解析上游 `Retry-After`，也不累积延长。
- 2026-10-24 起 403 与 429 统一冷却 24 小时，上述差异化时长与 `markOpencodeProxyForbidden` 已废止，见 [OpenCode 免费档 403 与 429 统一冷却 24 小时](2026-10-24-opencode-403-429-uniform-cooldown.md)。
- 拒绝即入槽：冷却不再区分账号是否成功过（旧实现直接丢弃未验证账号），403/429 都写入冷却截止时间并落库，池内始终记得被拒过的出口。
- 账号池每渠道上限 `ocProxyMaxAccounts = 30000`，内存与库同顶；库内淘汰只在满额且距上次淘汰超过 `ocPruneInterval = 10min` 时执行，避免每次落库都做全表排序。
- 业务请求回填代理账号结果时，`proxyAccount != ""` 且状态码为 429 会调用 `markOpencodeProxyRateLimited`，复用 `SaveOpencodeProxyCooldown` 异步落库；403 仍调用 `markOpencodeProxyForbidden`。
- 多 Key 轮换分支只在没有代理账号拒绝时处理 401/429。`proxyAccount != ""` 的 403/429 跳过 Key 冷却，进入同一成员内的换号分支。
- 换号分支用同一个 `opencodeAccountRetries` 预算处理 403/429，上限仍为 `ocProxyMaxRetries = 3`。换号期间不记路由失败、不设 `exclude`、不清会话粘合、不写成员冷却或半开；耗尽后把最后账号写入 `opencodeAccountPinned`，随后回落既有成员失败路径。
- 非 OpenCode 渠道或未使用代理账号的 429 保持原语义：多 Key 渠道冷却当前 Key 并轮换，单 Key 渠道按成员失败处理。
- 容量探测复用同一账号池：`context_probe.go` 遇到 429 也冷却当前账号，但探测仍不换号，保持单发诊断语义。

403 的换号实现与预算见 [OpenCode 免费档 403 时直接换代理账号重试](2026-10-22-opencode-403-account-switch.md)，多 Key 优先级见 [多 Key OpenCode 渠道代理账号拒绝不冷却 Key](2026-10-22-opencode-403-multikey-priority.md)，账号池落库规则见 [按出口 IP 轮换代理账号池](../feature/2026-10-20-opencode-free-proxy-ip-pool.md)。

## 备选方案

- **403 与 429 共用 12 小时冷却**：结构最简单，但 429 的限速窗口通常比单次封锁更长，沿用 12 小时会让同一个耗尽出口更早回到轮换池；用户明确要求 429 冷却 24 小时。
- **429 继续冷却 Key**：普通多 Key 渠道确实需要处理凭据级配额，但 OpenCode 免费档的 `public` Key 不是实际配额主体；继续冷 Key 会错误惩罚渠道凭据，而且无法推进出口切换。
- **只冷却账号，不立即换号**：账号池状态会正确，但当前请求仍落回成员失败路径，单次 429 就可能让整个成员进入冷却。
- **429 无限换号**：恢复概率最高，但会持续创建账号与代理客户端；沿用三次预算可限制单请求资源放大，并在耗尽后交给既有成员失败策略。
- **把 429 直接映射为成员冷却**：实现最短，但出口问题会污染成员健康度与会话粘合，多个出口共享同一成员时尤其不合理。

## 后果

- **收益**：单次 429 只淘汰当前出口；同一请求会换号继续尝试，Key、成员健康度与会话粘合保持干净，24 小时内不会再次选中该出口。
- **代价与已知上限**：单请求最多覆盖 4 个出口，坏出口场景会增加代理连接与延迟；预算耗尽后仍复用最后账号，直到成员冷却或切换渠道。重访信号：上游开始返回可信的 `Retry-After`，或 429 不再与出口绑定。

## 验证

- `TestOpencodeProxy429CoolsAccount24hAndRetriesAnother` 覆盖首个账号 429 后冷却约 24 小时、落库、换号成功、Key 与成员状态未被污染。
- `TestOpencodeProxy429StopsAtRetryLimit` 覆盖连续 429 在 4 个账号后固定复用最后账号并回落成员失败路径。
- `TestOpencodeProxy429MultiKeySwitchesAccountWithoutKeyCooldown` 覆盖多 Key OpenCode 渠道 429 不冷却任何 Key。
- `TestOpencodeProxy403RetriesAnotherAccount` 与 `TestCredentialAndRateLimitStatusesRemainRetryable` 分别回归 403 的 12 小时冷却和非 OpenCode 渠道的 429 Key 冷却。
- `GOTOOLCHAIN=go1.26.7 go test -count=1 ./internal/relay/...` 与 `GOTOOLCHAIN=go1.26.7 go test -count=1 -race ./internal/relay`。
