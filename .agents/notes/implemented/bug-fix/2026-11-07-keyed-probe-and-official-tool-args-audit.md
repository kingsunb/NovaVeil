# Agent Note: 账号池与官方工具回映审计修复

Status: implemented

## 问题

对 0faa4e3、84eb3fa、31af099、5687b23 四个提交的静态审计发现三处会真实破坏行为的缺陷：

- 官方工具参数适配把「客户端 schema 无 `properties`」当成「白名单为空」。`buildOfficialToolTargets` 只在 `parameters` 是对象时置 `hasSchema`，`{"type":"object"}` 这类声明会得到空的 `properties` 映射；`adaptOfficialToolArguments` 随后逐个键查白名单，全部查空并丢弃，工具入参被改写成 `{}`，客户端收到空调用。
- keyed 免费渠道的上下文容量探测直接报错。`ocProbeSelectChannelKey` 对 `keyID == "" && channel.Keyed` 返回「密钥型渠道需指定渠道 Key」，而 `probeOnce` 固定传空 `keyID`，该渠道在渠道测试页永远得不到容量结论；若简单放行空 `keyID`，`pickOpencodeProxyAccountForKey` 会落回旧公共池并生成随机账号，污染 `key_id = ''` 台账。
- 密钥渠道页的「测试全部密钥」结果会串渠道。`testKeys` 的响应闭包只认旧渠道，请求在途时切换下拉，旧渠道结果仍会 `setResults` 覆盖到新渠道下方，且 `testing` 状态在新渠道一直为真，无法重新测试。

## 决定

- `internal/relay/opencode_tool_alias.go`：`adaptOfficialToolArguments` 在 `!hasSchema` 或 `len(properties) == 0` 时原样返回参数。无 `properties` 就没有裁剪与改键依据，保留上游参数比清空更符合客户端声明语义；有 `properties` 时的白名单裁剪、别名改键与 `webfetch` 默认 `prompt` 行为不变。
- `internal/relay/context_probe.go`：`probeOnce` 在 `channel.Keyed` 时取 `channelKeyTestCandidates` 首条的 `ID` 作为 `keyID` 再进入 `probeOnceWithAccount`。容量探测本身不分 Key 维度，固定首条即可让凭据选择与代理账号都落在同一 Key 分区；无候选 Key 时仍由 `ocProbeSelectChannelKey` 报「渠道未配置任何密钥」。账号体检路径不变，keyed 仍必须显式指定 Key。
- `web-next/src/pages/ProxyAccounts.tsx`：`KeyedChannelSection` 用 `testSeq` 序号标记每轮测试。发起请求时自增并捕获序号，渠道切换的 `useEffect` 同时自增序号、清空结果并复位 `testing`；响应、错误提示与 `finally` 只在序号仍是当前值时落地，过期请求整段失效。
- `web-next/src/lib/api.ts` 与页面内 `AccountProbeJob` 补齐后端已返回的 `key_id`：体检启动响应、作业轮询响应、台账顶层均声明 `key_id: string`，台账条目由可选改为必填，消除 `unknown as` 掩盖的契约漂移。

## 备选方案

- **无 `properties` 时继续按空白名单裁剪** — 与 `additionalProperties:false` 的严格声明在形式上统一；但严格声明总会写出 `properties`，空 `properties` 只表示客户端未约束字段，裁剪必然清空入参，否决。
- **keyed 上下文探测沿用空 `keyID` 走旧公共池** — 改动最小且不需要选 Key；但会绕过「按 Key 固定账号」的隔离约定，随机账号还会写进公共台账，否决。
- **测试期间禁用渠道下拉来避免串结果** — 能挡住主动切换，实现也最简单；但无法处理已发出请求晚于渠道列表刷新返回的情况，且把等待成本转嫁给用户，因此采用序号失效，下拉保持可切换。

## 后果

- **收益**：无 `properties` 声明的客户端不再收到空参数工具调用；keyed 渠道恢复上下文容量探测且代理账号不串分区；逐 Key 测试结果只归属于发起时的渠道，过期响应与错误提示一并丢弃。
- **代价与已知上限**：上下文探测固定首条 Key，其他 Key 的代理账号冷却状态不影响探测结论，容量结论不代表每个 Key 都可用；序号方案只覆盖 `KeyedChannelSection`，普通渠道的账号体检作业仍有各自的轮询生命周期，未在本次改动范围内。

## 验证

由用户执行（AGENTS 禁止 AI 运行构建与测试）：

```sh
go test ./internal/relay -run 'TestAdaptOfficialToolArguments|TestBuildOfficialToolTargets|TestRestoreOfficialToolCalls|TestOfficialToolStream|TestOcProxy|TestContextProbe' -v
go test ./internal/relay && go vet ./internal/relay
cd web-next && pnpm exec vitest run src/pages/ProxyAccounts.test.tsx && pnpm typecheck
```

重点用例：新增 `TestAdaptOfficialToolArgumentsKeepsArgsWithoutProperties` 覆盖空 `properties`；新增「逐 Key 测试延迟响应期间切换渠道不串用旧结果」覆盖旧响应丢弃与新渠道立即可测。
