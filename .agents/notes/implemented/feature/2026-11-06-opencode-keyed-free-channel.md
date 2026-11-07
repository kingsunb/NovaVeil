# Agent Note: OpenCode 免费档密钥渠道与按 Key 隔离的账号池

Status: implemented

## 问题

内置 OpenCode 免费渠道全部使用公开占位 Key `public`：`OpenCode Free V1`、`OpenCode Free V2`、旧名兼容渠道与 V1 备用副本都指向 `https://opencode.ai/inference`。管理员若想把真实 Key 填进现有渠道，会把公共免费流量与密钥免费流量混在同一条渠道、同一套 IP 代理账号池和同一份体检台账里，既无法区分流量，也无法按 Key 判断健康度。

密钥型免费渠道仍会遇到上游按出口 IP 的 403/429 惩罚，只做逐 Key 测试无法保护真实流量：测试通过不代表持续可用，被拒时也没有可轮换的出口。第一版据此把 keyed 渠道排除在 `{account}` 代理账号池之外，只保留逐 Key 测试；该结论已被纠正为「keyed 免费渠道配置 `{account}` 后仍走代理账号池，只是按 Key 隔离运行」。

## 决定

1. **渠道标记与内置定义**：`model.Channel` 增加 `Keyed bool`（数据库列 `keyed`，默认 false），`ChannelCreate` 强制 `Keyed=false`，普通自定义渠道不能伪造该身份。`BuiltinFreeChannels` 新增 `OpenCode Free V1 账号池`：`BaseURL=https://opencode.ai/inference`、`OpencodeCompat=true`、`OpencodeLane=v1`、`Keyed=true`，不设置 `Key`、`Keys` 或 `ChannelProxy`；模型列表与 `OpenCode Free V1` 一致。`freeChannelFromDefinition` 对 keyed 渠道强制 `Enabled=false` 并清空 Key/Keys，启动补建按 Name 幂等，不覆盖管理员后配的 Key 与启停状态。
2. **保留免费档整形与 Key 轮询**：keyed 渠道仍满足 `isOpencodeFreeLane`（`OpencodeCompat && OpenCodeTier=="zen"`），免费档改写、会话头与传输路径不变。Key 来自管理员配置的 `Keys`，由 `selectChannelKey` 原子游标轮询选择并跳过冷却中的 Key；多 Key 时 Key 级 401/403/429 只冷却对应 Key（`markChannelKeyCooldown`），不因渠道是 keyed 而绕过路由、failover 或 Key 轮询。
3. **默认无代理、配置 `{account}` 后入池**：keyed 渠道出厂不带 `ChannelProxy`，`proxyNeedsOpencodeAccount` 不再排除 `Keyed`，判定仍是免费档 + 含 `{account}` 的代理模板。管理员未配置代理时它不进入账号池、不做账号轮换；配置后与其他免费渠道一样进入 IP 账号池，keyed 与普通 public 免费渠道按不同渠道 ID 并行。
4. **台账增加 key_id 维度**：`model.OpencodeProxyAccount` 增加 `KeyID`（列 `key_id`，`size:16;not null;default:''`），唯一索引改为 `idx_oc_proxy_channel_key_account`，覆盖 `(channel_id, key_id, account)`。迁移 027 先把存量 `key_id` 的 NULL 回填为空串，删除旧 `idx_oc_proxy_channel_account`，再创建新索引；旧行 `key_id` 为空串，仍归旧的单 Key/public 渠道。keyed 渠道的每把 Key 使用 `stableChannelKeyID`（SHA-256(Key 明文) 前 8 位十六进制）作为 `key_id`，同一密钥多次提交保持稳定。
5. **账号池、绑定、计票按 Key 分区**：进程内池键改为 `ocProxyPoolKey{channelID,keyID}`，选号、释放、成功、429 计票、失败与封禁均有 `...ForKey` 版本，空 `keyID` 回退旧的渠道级池。会话绑定、429 双会话计票窗口、24 小时失败窗口与冷却全部按 Key 隔离，同渠道不同 Key 的池互不共享。同一 Key 的账号仍受 `ocProxyPerAccountLimit=5` 并发请求与 `ocProxyPerAccountSessions=1` 活跃会话限制，同会话并行请求共用账号；不同 Key 互不影响，因此这两个上限保持不变。
6. **体检与台账带 key_id**：`prepareAccountProbe` 不再拒绝 `Keyed`，改为要求 `proxyNeedsOpencodeAccount`；keyed 渠道配置代理后，手动体检按请求的 `key_id` 选 Key，自动体检遍历渠道配置的全部 Key 并分别用其 ID 探测。keyed 渠道的账号不随机生成，固定为所选 Key 的稳定 `ID`（`prepareAccountProbe` 忽略 `accounts`/`count`），账号数等于 Key 数，不提供新增其他账号的入口。IP 账号台账、体检与删除接口都接收或返回 `key_id`，账号体检页对配置了 `{account}` 的 keyed 渠道按 Key 展示账号池、体检与台账。逐 Key 测试（`/channel/test_keys`）继续保留，仅作即时诊断，不写 IP 台账。
7. **兼容边界**：普通 public 免费渠道继续使用空 `key_id`，行为不变；keyed 渠道与普通 public 免费渠道可以同时存在、并行分组；迁移 027 只回填存量并替换索引，不删除账号行。

## 备选方案

- **只做逐 Key 测试，keyed 不进 IP 账号池（第一版）**：测试只能证明某把 Key 当时可用，真实流量仍会撞上出口 IP 的 403/429；没有代理账号轮换就无法持续服务，也无法把 IP 健康记到对应 Key 上。已被纠正并否掉。
- **继续按生成数量随机生成 keyed 账号**：账号池会出现与 Key 不对应的额外出口，既违反「一个 Key 一个代理」的运维约定，也无法把账号健康准确归因到 Key。最终改为 Key.ID 固定账号，不生成、不新增。
- **把单账号并发上限 5 改成 1**：这是为了掩盖「多把 Key 共用同一账号池」的冲突，但会把普通 public 免费渠道也一起限死，牺牲吞吐，且不解决 Key 之间冷却、失败计数与台账归属。最终选择按 Key 分池，保持 5 并发 / 1 活跃会话不变。
- **继续拒绝 keyed 进入账号池**：与上游仍按 IP 惩罚的事实冲突；keyed 渠道被 403/429 后只能换 Key，成本高且不可持续。否掉。
- **把真实 Key 直接填进现有 `OpenCode Free V1`**：公共与密钥流量混在同渠道、同池、同台账，无法区分两类流量，也无法让希望保留 `public` 的实例继续使用公共通道。否掉。
- **新建普通自定义渠道并勾选 OpencodeCompat**：`ChannelCreate` 强制 `OpencodeCompat=false`，这是防止伪造内置免费档行为的既有不变量。否掉。
- **新建独立 Key 台账或独立账号池**：会与现有逐 Key 测试、Key 冷却、账号台账形成第三套状态，数据库、接口与页面都要重复建设；在现有唯一键上增加 `key_id` 更小，且旧行可用空串兼容。否掉。
- **把 keyed 渠道出厂设为启用**：keyed 渠道没有 Key，启用后只会产生 401；因此 `freeChannelFromDefinition` 对 keyed 默认禁用。

## 后果

- **收益**：public 与 keyed 免费渠道可以同时存在、独立分组与切换；keyed 渠道保留免费档整形，并能在配置 `{account}` 后使用 IP 账号池轮换；不同 Key 的账号池、会话绑定、冷却与台账完全隔离，单 Key 的健康问题不会污染其他 Key；存量账号行无需迁移数据即可继续使用。
- **代价与已知上限**：keyed 渠道出厂禁用，管理员必须手动配置 Key 并启用；代理默认关闭，不配置 `{account}` 就没有 IP 轮换。每个 Key 只有 `Key.ID` 对应的一个固定账号，不能生成或新增其他账号；固定账号冷却、并发满或会话占用时请求按失败处理，不创建替代账号。`ocProxyMaxAccounts=30000` 按 `(channel_id, key_id)` 池计数，但实际账号数由 Key 数决定。Key 明文变更会生成新的 `key_id`，旧池与旧台账行保留但不再被该 Key 使用。迁移 027 必须先于功能使用完成。
- **兼容**：既有 public 免费渠道的默认启用、Key 轮询、IP 账号池、403/429 冷却与逐账号体检不变；keyed 渠道默认禁用且空 Key，不会在升级后自动产生不可用成员。

## 验证

```sh
go test ./internal/db/migrate/ -run TestScopeOpencodeProxyAccountsByKey
go test ./internal/relay/ -run 'TestPrepareAccountProbeKeyedUsesKeyID|TestProxyNeedsOpencodeAccountKeyed|TestOcProxyPoolForKeyIsolation' -v
go test ./internal/relay/...
go test ./internal/server/...
cd web-next && pnpm test -- ProxyAccounts
pnpm verify-notes
```

- `go test ./internal/db/migrate/ -run TestScopeOpencodeProxyAccountsByKey`：覆盖迁移 027 的空 Key 回填、旧索引删除、新唯一索引按 Key 放行同账号。
- `go test ./internal/relay/ -run 'TestPrepareAccountProbeKeyedUsesKeyID|TestProxyNeedsOpencodeAccountKeyed|TestOcProxyPoolForKeyIsolation|TestOcProxyFixedAccountSingleSession' -v`：覆盖 keyed 账号固定为 Key.ID、忽略生成参数、同渠道不同 Key 的账号池隔离，以及固定账号不会为其他会话新增账号。
- `go test ./internal/relay/...`：覆盖 `proxyNeedsOpencodeAccount` 对 keyed 渠道的判定、同渠道不同 Key 的账号池隔离，以及业务转发按 `selectedKey.ID` 传入账号池的链路。
- `cd web-next && pnpm test -- ProxyAccounts`：覆盖账号体检页对配置了 `{account}` 的 keyed 渠道按 Key 选择、体检、台账与删除。
- `pnpm verify-notes`：校验本笔记的目录、状态与格式门禁。
- 线上验证：给 `OpenCode Free V1 账号池` 配置两把 Key 与含 `{account}` 的代理模板，分别触发体检与真实请求，确认两次请求落在不同 `key_id` 的账号台账，且同一 Key 的账号仍受 5 并发 / 1 活跃会话限制。
