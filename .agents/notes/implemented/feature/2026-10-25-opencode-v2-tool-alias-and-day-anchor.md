# Agent Note: v2 出站工具改名 clint_ + 会话号天锚（参考 opencode2api 2026-10-24 实现）

Status: implemented

## Problem

v2 档调查(见 [v2 调查笔记](../bug-fix/2026-10-25-opencode-v2-lane-investigation.md))穷尽全部可观测维度后仍 429。2026-10-24 夜 jasonxu114514/opencode2api 更新出两项我们缺的形态对齐(commit fd4c1c6d 与 95331011), 经逐项对照确认可移植:

1. **客户端工具污染官方集**: 上游按官方工具集形态校验 v2 会话, 客户端自带的自定义工具名(如 `patcher`、`patch`)与定义留在出站正文里即是可识别的非官方特征; 旧整形只处理 5 个核心工具, 其余客户端名原样出站。
2. **会话号时间戳不稳定**: 小时锚实现同一信号跨小时换号(等效新会话), 且实现上锚粒度与哈希混合位的设计使测试解码口径不直观。

## Decision

工具 schema 覆盖及客户端同名工具替换部分由 [免费档契约修复](../bug-fix/2026-10-30-opencode-free-contract-repair.md) 取代；历史决定保留，会话号天锚不变。当前两档 Chat 都隔离冲突工具并保留客户端参数定义。

1. **v2 工具改名 + 官方全集(`internal/relay/opencode_tool_alias.go`)**: `ensureOfficialV2Toolset` 在 v2 chat 整形链(系统提示注入之后)把客户端自定义工具改名 `clint_` 前缀的无冲突出站名(非法字符转 `_`、重名追加 `_`), 工具数组重排为「官方 12 工具全集(体检模板原文, 顺序一致) + clint_* 客户端工具」; `tool_choice.function.name` 与历史 `tool_calls[].function.name` 同步改名(反查客户端名→出站名)。`shapeOpencodeFreeBody` 第三返回值从 v1 单别名 string 泛化为别名表 `map[出站名]客户端名`, 转发路径(`applyToolAliasRestore`/`restoreToolAliasesInBody`/`restoreToolAliasesInEvent`)对非流式正文、流式窗口事件与剩余事件流统一还原客户端声明名, 未映射名透传。v1 档行为锁定不变(仅 read 占位别名, 无 clint_ 改名)。已知核心工具(如 `patch`, 在 opencode_tools.json 的 14 工具集内但不在官方 12 模板内)仍先按 `ensureOpencodeFreeTools` 校正为真实定义再改名。
2. **会话号天锚(`internal/utils/opencodeid/opencodeid.go`)**: `SessionIDFromSignal` 时间段改为「当前 UTC 日 − 哈希偏移(至多 30 天)」: `current = day_ms<<12 | 无counter`, 与 opencode2api 95331011 同构。48 位字只保留低 36 位毫秒, 上游按当前纪元(k×2^36)展开解码, 拒绝距此刻约一年以上的值——哈希直构的任意 36 位值约半数落在拒绝窗(解释了旧版「多数出口 429」), 天锚值恒在窗内。同一信号全日内恒定派生同一号, 确定性优于小时锚。

## Alternatives considered

- **把 `patch` 等已知工具并入官方 12 模板** — 定义实测过可被上游识别; 否掉: 官方 12 工具集以我们的实抓模板为准, 模板外名字混入反而新增可观测差异, 改名+还原对客户端透明。
- **维持小时锚** — 已实测形态合法; 否掉: 同一信号跨小时换号破坏长会话粘合, 天锚确定性更强且与参考实现对齐, 迁移成本一行。

## Consequences

- **收益**: v2 出站工具集与官方会话形态一致(12+clint_*), 客户端工具对上游不可见; 派生会话号全日稳定且恒在时间窗内; v1/v2 响应还原统一为别名表口径。
- **代价与已知上限**: 出站正文在官方 12 工具外多出 clint_* 工具(官方会话也可能带自定义工具, 形态合法); 模型看到的是 clint_ 名, 若模型在回复文本里念出工具名会是 clint_ 名(响应侧仅还原结构化 tool_calls); 会话号跨天轮换对跨天长会话等效新会话。
- **重访信号**: 上游若开始校验 clint_ 名的注册关系或官方模板工具数变化, 需同步模板与改名策略。

## Verification

```sh
go test ./internal/relay/ ./internal/utils/opencodeid/
```

- `TestEnsureOfficialV2Toolset*`: 改名/官方全集顺序/tool_choice 与历史 tool_calls 反查改名。
- `TestRestoreToolAliasesInEventAndBody`: 非流式与流式增量还原。
- `TestShapeV2KeepsV1LaneUntouched`: v1 档无 clint_ 回归。
- `TestSessionIDFromSignalStaysInWindow`: 纪元重建解码口径锁定天锚窗内与恒定派生。
