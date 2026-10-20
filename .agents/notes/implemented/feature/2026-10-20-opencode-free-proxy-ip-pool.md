# Agent Note: OpenCode 免费档按出口 IP 轮换代理账号池

Status: implemented

## 问题

OpenCode 免费档的上游 403 已确认是**出口 IP 被标记**，不是请求形态问题：同一份请求换个 IP 就通，原 IP 继续 403。

而本仓的代理账号此前是确定性的：`effectiveChannelForKey` 用 `AccountAliasFor(channelID, keyID)` 的 sha256 前 8 位填 `{account}`，同一渠道同一 Key 永远得到同一个账号，也就是永远同一个出口 IP。这个 IP 一旦被标记，该渠道所有请求一起失效，除了手工改配置没有出路。

不做会怎样：单 IP 被拉黑即整渠道不可用，且管理员无法从管理台感知哪个出口还能用。

## 决定

为 OpenCode 免费档渠道（`isOpencodeFreeLane`：`OpencodeCompat && OpenCodeTier=="zen"`）维护一个按出口 IP 的账号池，只在渠道代理模板含 `{account}` 时启用（`proxyNeedsOpencodeAccount`）。其余渠道与其他占位符用法完全不变。

### 池规则

- **选号优先级**：7 天内成功过、未冷却、并发未满的账号中，取最近成功的那一个。
- **并发上限**：每个账号 5 个并发槽位。全部占满时生成新账号，而不是排队等同一个 IP。
- **成功**：刷新该账号的 `last_success_at`，并异步落库。
- **403**：该账号冷却 12 小时；多次 403 只刷新冷却截止时间，不叠加时长。
- **未验证账号**：内存内参与并发计数与复用，但不落库；首次即 403 的直接丢弃。
- **容量**：每渠道最多保留 200 条，超出按 `last_success_at` 淘汰最旧。

### 存储与读写

- 新表 `opencode_proxy_accounts`（`channel_id` + `account` 唯一索引），只存成功过的账号。
- **读**：每渠道进程内只从库里加载一次（`loadOpencodeProxyPool`），此后选号全走内存，不在请求热路径上查库。
- **写**：成功与冷却都异步落库，不阻塞请求；DB 不可用时降级为纯内存池。
- 并发槽位随请求上下文释放（`context.AfterFunc`），不依赖显式归还。

### 接入点

- `internal/relay/opencode_proxy_pool.go`：池、选号、成功/403 回填、清理。
- `internal/relay/handler.go`：选出账号后用 `ResolveProxyTemplate` 覆盖本轮 `effective.ChannelProxy`；本轮结束后按结果回填成功或冷却。
- `internal/op/opencode_proxy.go`：库操作（加载 / upsert 成功 / upsert 冷却 / 按容量清理）。
- `internal/model/opencode_proxy_account.go` 与新表注册。

`effectiveChannelForKey` 的签名不变，探测与渠道测试路径仍用确定性别名，不占池内并发。

## 备选方案

- **每个请求随机 account** — 一行代码即可；否掉是因为每换一个账号就多一个代理客户端与连接池（`bunClients` 按代理 URL 缓存且无上限淘汰），同时同一会话的请求来自不同 IP，上游更容易判定为多客户端刷量。
- **纯内存池，不落库** — 无表、无迁移；否掉是因为「7 天内成功过优先」与「403 冷却 12 小时」跨重启就失效，进程一重启所有 IP 重新试错，等于把已被标记的 IP 再撞一遍。
- **在管理台预置一组合法账号，池内轮换** — 最贴合 Resin 的计费模型；否掉是因为当前 `{account}` 的值本身就是任意的出口选择标识，用户明确要求随机生成，预置列表会让每次扩容都要人工改配置。
- **403 时同一请求内换号重试** — 用户感知最好；否掉是因为 403 之前可能已经写出了部分响应（流式），换号重试无法保证幂等，且会把一个请求的失败放大成两次上游消耗。当前实现是失败本轮、下轮重新选号。

## 后果

- **收益**：单 IP 被标记不再等于渠道报废；成功过的出口被记住并优先复用，避免反复踩同一个坏 IP；池状态跨重启保留。
- **代价与已知上限**：新增一张表与一次 AutoMigrate；每渠道首次请求要同步读一次库（之后全内存）；内存池与库在多实例部署下不共享，多副本会各自选号；账号数上限 200 是硬编码常量，暂未做成配置项。
- 重访信号：上游开始按账号粒度计费或封号、单渠道账号数长期顶到 200、或多实例部署成为常态需要共享池状态。

## 验证

- `grep -n "pickOpencodeProxyAccount" internal/relay/` 只应出现定义与 handler 的一处调用。
- `grep -n "LoadOpencodeProxyAccounts" internal/relay/` 只应出现在 `loadOpencodeProxyPool` 内，确认热路径不查库。
- 表结构：`opencode_proxy_accounts` 含 `channel_id`、`account`、`last_success_at`、`cooldown_until`，且 `(channel_id, account)` 唯一。
