# Agent Note: OpenCode 代理账号按可用分层选号并统一 UTC 零点冷却

Status: implemented

## 问题

旧选号只看连续失败、验证状态与最近成功时间，且 `ocProxyUsable` 会把超过 7 天的已验证账号整体排除：可用池一空就直接新建随机账号，既不会先用「曾经成功、只是过期」的历史账号，也不会先用「当天还没体检」的账号。台账页的「可用」却已经按体检健康或近期真实成功派生，两套口径不一致。

同时，403/429 冷却优先采用上游 `Retry-After`。该值通常正好是到 UTC 零点的剩余秒数，但异常值或过期值会把账号锁满 24 小时；不同拒绝路径缺头时又各自回落，口径不统一。用户 m05094 明确要求：只有实际体检成功或真实请求成功过的账号才进入可用；冷却统一到 UTC 零点；可用池没有时优先历史可用与当天未测试。

## 决定

- 选号改为四级分层（数值越小越优先）：`ocProxyTierUsable=0` 为体检健康或近 7 天真实成功；`ocProxyTierHistory=1` 为曾成功但当前不满足可用层（超期或最近体检非健康）；`ocProxyTierUntested=2` 为 UTC 日未体检且无成功记录；`ocProxyTierFallback=3` 为 UTC 日已测且非健康。`ocProxyCandidateTier` 负责定层，`ocProxyCandidateBetter(a,b,now)` 先比层，同层再比连续失败少、已验证、最近成功。「当天」统一按 UTC 日界，与冷却重置对齐，台账的「今日已测」筛选与成功天数统计同口径。
- `ocProxyUsable` 只保留「并发未满 + 未冷却」两个硬条件，让历史/未测试/当天失败层都能在可用池空时按层回退；不再因 7 天过期直接排除账号。
- `ocProxyAccount` 增加 `lastProbeAt`、`lastProbeOutcome`、`successCount`，加载池时从 `opencode_proxy_accounts` 行恢复，重启后分层不丢。真实成功与体检成功都累加 `successCount`；体检成功同时写 `lastProbeAt=now`、`lastProbeOutcome=healthy`。新增 `markOpencodeProxyProbeOutcomeForKey`，体检落库后即时同步进程内池。最近体检为 `error` 或 `rejected` 且没有更晚的真实成功时，近期成功不能把账号留在可用层。
- `opencodeRejectCooldown(now)` 一律返回距下一个 UTC 零点的时长（上限 24 小时），忽略 `Retry-After`；`markOpencodeProxyRejected*` 与 `markOpencodeProxyRateLimited*` 删除 `retryAfter` 参数，业务转发与上下文探测调用点同步收敛。
- 台账「可用」定义保持只认体检健康或真实成功；网络错误、被拒与冷却仍计入不可用。选号回退到历史/未测试层不改变台账展示。

本决定取代 [429 冷却优先取 Retry-After](./2026-10-26-opencode-429-retry-after-cooldown.md) 的时长来源；403/429 同类处理、两会话计票、换号重试与 Key 不冷却等其余语义不变。旧篇仅保留当初采纳 `Retry-After` 的取证与理由，互链见该篇顶部。

## 备选方案

- **继续优先采用 `Retry-After`，仅加异常值上限** — 最强理由是上游若改为非按天配额，秒数比固定零点更准；否掉：当前免费档实测就是按 UTC 天重置，异常值反而会把账号锁到 24 小时，且各拒绝路径的头不可靠。
- **只放宽 7 天过期排除，保留旧比较器** — 改动更小；否掉：旧比较器仍让零失败的未验证新号压过已验证的历史号，用户要的「先用历史可用」无法实现。
- **只按最近成功时间排序** — 实现简单；否掉：从未成功过的账号没有时间可排，会与历史账号混在一起，无法表达「当天未测试」这一层。
- **直接删除过期历史账号** — 池更干净；否掉：用户明确要求可用池为空时优先历史可用，删除会让冷启动重新随机生成账号。
- **把网络错误账号永久排除出选号** — 台账更严格；否掉：所有账号都失败时仍需要一个最后回退，且用户只要求「可用」标签严格，并未要求禁止兜底请求。

## 后果

- 收益：可用池为空时按「历史可用 → 当天未测试 → 当天已测非健康」稳定回退，不再一空就新建随机账号；冷却截止与免费档 UTC 天配额对齐，重启后由数据库恢复。
- 代价与已知上限：若上游未来改为非 UTC 天粒度或按身份续期，固定 UTC 零点会低估冷却时长，重访信号是观察 `Retry-After` 与零点再次出现系统性偏差；当天已测非健康账号仍是最后回退，可能再吃一次失败。
- 兼容：普通 public 与 keyed 免费渠道共用同一套分层与冷却；keyed 每 Key 固定一个账号，分层只影响它在多账号池中的优先级。

## 验证

```sh
go test ./internal/relay/ -run 'TestOcProxy|TestOpencodeRejectCooldown|TestOpencodeProxy429' -v
go test ./internal/relay/...
cd web-next && pnpm test -- ProxyAccounts
```

- `TestOcProxyCandidateTier`、`TestOcProxyPickFallsBackByTier`：锁定四级分层与回退顺序；`TestOcProxyCandidateTierProbeErrorAfterSuccess`、`TestOcProxyCandidateTierProbeRejectedAfterSuccess`：最近体检非健康覆盖旧成功。
- `TestOcProxyProbeOutcomeUpdatesPool`：体检结论即时进入进程内池。
- `TestOpencodeRejectCooldown`、`TestOpencodeProxy429CoolsUntilUTCMidnight`：冷却固定到下一个 UTC 零点并忽略 `Retry-After`。
