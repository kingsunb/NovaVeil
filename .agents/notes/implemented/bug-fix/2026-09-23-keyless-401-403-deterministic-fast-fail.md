# Agent Note: 无密钥渠道 401/403 纳入确定性请求错误快速失败

Status: implemented

## 问题

转发主循环把确定性请求错误(400/404/422)识别为「重试同一成员或等待冷却都不可能改变结果」:
每成员只派发一次, 全败即终止并返回统一「暂无可用渠道」。但上游 401/403 未被纳入: 无密钥
(keyless)渠道——`channel.Keys` 与 `channel.Key` 均为空、不携带任何凭据——收到 401/403 时,
错误被当作普通业务失败走正常失败路径: 按 `member_max_attempts` 反复重试同一成员、每个间隔空等
`member_retry_interval_seconds`, 耗尽后才切换下一成员。可 keyless 渠道没有任何凭据可轮换
(多 Key 轮换路径因 `len(channel.Keys)>1` 不成立而跳过), 401/403 是持久的配置性拒绝, 重试
只会空转烧掉轮次与时长预算, 与当年 Codex developer 角色 400 空转 30 轮是同一类问题。

## 决定

`internal/relay/handler.go` 主循环的确定性快速失败分支(原仅 400/404/422)扩展条件: 当错误状态码
为 401/403 且本轮选中凭据为空(`selectedKey.Key == ""`, 等价于无密钥渠道)时, 同样进入该分支——

- 该成员照常 `recordRouteFailureIfReal` 计失败并武装冷却、`clearSessionStickyByItem` 清粘合;
- 标记 `nonRetryable` 后立即切换下一优先级成员, 不重试、不等待;
- 全部启用成员各失败一次后 `markFailed` + `rejectRequest(..., errNoAvailableChannels)`,
  下游收到统一 `400 + 暂无可用渠道`(上游 401/403 详情仅保留在内部状态与错误日志)。

边界(务必守住):

- keyed 多 Key 渠道的 401/403 仍走上一段的多 Key 轮换(标记该 Key 冷却后换下一把 Key),
  不进入本分支。
- keyed 单 Key 渠道(有真实凭据)的 401/403 保持旧的正常失败路径(进冷却、按语义重试)。
  判定用「本轮选中凭据是否为空」而非「状态码是否为 401/403」, 正是为了不改动 keyed 渠道语义。
- 429 不纳入: 限流可能到期恢复, 仍属可恢复失败。

## 备选方案

- **把 401/403 一律纳入确定性快速失败(不分 keyless/keyed)** — 最强论据是 401/403 语义统一;
  否掉因为 keyed 多渠道有凭据可轮换, 多 Key 轮换路径已依赖 401/403 触发; 单 Key 渠道也可能
  在管理员更换密钥后恢复, 一刀切会破坏既有轮换与「密钥被轮换后可重试」的语义(正被
  `TestSingleKeyChannelKeepsLegacyFailurePath` 钉死)。
- **新开一个 keyless 专用快速失败分支** — 最强论据是不改现有 400/404/422 分支、改动面小;
  否掉因为两处逻辑完全相同(计失败、清粘合、去重、全败终止), 复制粘贴会造成两条平行实现日后
  漂移, 扩展现有条件把 keyless 判定写成内联布尔最省维护成本。
- **判定信号用 `len(channel.Keys)==0` 或 `channel.Key==""`** — 在 `normalizeChannelKeys` 保证
  「Keys 为空或每条非空」的前提下与 `selectedKey.Key == ""` 等价; 否掉因为后两者读配置真值,
  对历史脏数据 `Keys=[{Key:""}]` 会漏判(仍空转), 而 `selectedKey.Key == ""` 度量「本轮实际未
  携带凭据」这个真正决定重试是否白费的谓词, 且已在作用域、零额外调用。

## 后果

- 收益: keyless 渠道配置错误/上游鉴权配置错误时, 请求立即以统一「暂无可用渠道」收尾, 不再
  空转 `member_max_attempts × member_retry_interval_seconds`; 失败轨迹只剩每成员一条 4xx 记录,
  日志干净。
- 代价与已知上限: 该分支如今覆盖 400/404/422 与 keyless 401/403 两类确定性错误; keyed 渠道的
  401/403 语义不变。下游仍无法从 HTTP 状态码区分「认证/配置错」与「请求参数错」(与 400/404/422
  一致, 均返回 400)——若日后需要区分, 应新增独立哨兵并让 rejectRequest 映射回 401/403, 属独立
  增强而非本改动的一部分。

## 验证

`internal/relay/keyless_test.go` 新增 `TestKeylessAuthRejectionFailsFastAcrossMembers`
(表驱动 401/403): 两个 keyless 成员各返回 401/403 时逐项断言 (1) 每成员上游命中恰好 1 次
(不按 member_max_attempts 重试); (2) 耗时 < 8s(不空等 5s 重试间隔); (3) 下游 400 +
「暂无可用渠道」且不泄漏上游详情; (4) 请求终态 `StatusFailed` 且恰好两条
`AttemptFailed/ErrClassUpstream4xx` 轨迹。该用例与 `TestNonRetryable400FailsFastAcrossMembers`
(400)、`TestSingleKeyChannelKeepsLegacyFailurePath`(keyed 单 Key 403 保留旧语义)、
`TestMultiKeyAuthRotationEndToEnd`(多 Key 401 仍轮换)共同守护边界。`go test ./internal/relay/`
与 `pnpm verify-notes` 通过。