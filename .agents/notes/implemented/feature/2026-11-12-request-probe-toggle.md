# Agent Note: 请求触发半开探测增加分组开关

Status: implemented

## 问题

分组全部成员冷却且冷却到期时，选路会在请求路径上调用 `recoverExpiredItems` 合成探测，并把胜出成员的 `ProbeItemID` 占住，等待真实业务二次确认。该路径没有独立开关：`background_probe_enabled` 只控制后台定时探测，关掉它仍会在有 CLOSED 成员时异步探测到期成员，也会在全冷却时合成探测。管理员需要在不改代码的情况下关闭请求触发的探测，让冷却到期成员直接由真实业务请求恢复。

## 决定

`GroupRelayConfig` 新增 `RequestProbeEnabled`，JSON 字段 `request_probe_enabled`，零值 `false` 表示默认关闭。分组编辑页用同名的正向开关“请求触发半开探测”展示，保存时直接写入布尔值。

开启后：

- 有 CLOSED 成员时，对冷却到期成员调用 `ensureProbeLocked` 异步探测，业务请求继续使用 CLOSED 成员。
- 全部成员冷却且存在到期成员时，合成探测并使用 `ProbeItemID` 占用，等待真实业务二次确认。
- 后台定时探测不受影响，仍由 `background_probe_enabled` 独立控制。

关闭后（默认）：

- 有 CLOSED 成员时，不再对冷却到期成员调用 `ensureProbeLocked`。
- 全部成员冷却且存在到期成员时，按优先级取第一个到期成员，用 `claimHalfOpenLocked` 占用并置 `ProbeItemID`，直接把它作为本请求的业务候选；成功走 `recordRouteSuccess` 恢复，失败走 `recordRouteFailure` 重新冷却并换下一个。

## 备选方案

- **复用 `background_probe_enabled`** — 最强论据是少一个配置项；否掉因为它语义是后台定时器，管不到请求路径，关掉后仍会合成探测。
- **负向字段 `request_probe_disabled` 默认 false** — 最强论据是旧分组缺字段时零值可保持旧行为；否掉因为默认语义与用户要求相反，前端还要取反，容易误配。
- **正向字段 `request_probe_enabled` 默认 true** — 最强论据是字段名与行为一致；否掉因为用户明确要求默认关闭，旧分组缺字段应直接关闭探测。
- **彻底删除请求触发探测** — 最强论据是路径最简；否掉因为需要保留管理员显式开启的恢复能力。
- **只增加探测超时配置** — 最强论据是不改变恢复语义；否掉因为问题根因是要不要合成探测，调超时不能消除 `ProbeItemID` 占用与额外请求。

## 后果

- **收益**：默认不再发起合成探测，避免恢复探测消耗额度或占用请求路径；全冷却时由真实业务请求直接验证恢复。
- **代价与已知上限**：关闭后，全冷却且无真实流量时成员会一直保持冷却；首个真实请求承担恢复成本；多个到期成员只按优先级取第一个，不比较延迟。默认关闭会改变旧分组的现有行为。
- **兼容性**：新增字段为可选 JSON，旧客户端保存时若缺少该字段会回落到零值 false（关闭）。

## 验证

- 后端：`TestPickGroupItemRequestProbeDefaultDisabledUsesExpiredDirectly` 验证默认关闭下直接返回首个到期成员、不调用探测、占据 `ProbeItemID`，业务成功后释放；`TestPickGroupItemRequestProbeDefaultDisabledSkipsExpiredWhenClosedExists` 验证有 CLOSED 成员时不探测到期成员且保留冷却记录。
- 前端：`types.test.ts` 校验默认 false；`Groups.test.tsx` 校验开关默认关闭、开启后保存 `request_probe_enabled=true`，重新打开仍保持开启。
- 文档：[路由配置](../../../../docs/DEVELOPMENT_routing.md) 与 [relay 模块](../../../../docs/modules/relay.md) 已补充语义。
