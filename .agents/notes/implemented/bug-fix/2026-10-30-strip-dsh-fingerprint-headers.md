# Agent Note: opencode 渠道只透传官方客户端请求头

Status: implemented

## 问题

第三方客户端接入网关后，会把自身的私有标识头一路透传到上游 OpenCode。实测抓包（`/root/capture.log`，2026-10-29 23:59）里，dsh 客户端的一次对话请求带了两组官方 CLI 从不发送的头：

- `X-Bili-*` 六个：`x-bili-hop`、`x-bili-plugin`（值 `dsh`）、`x-bili-plugin-context-window`、`x-bili-plugin-conversation`、`x-bili-plugin-model`、`x-bili-tunnel`
- `X-Stainless-*` 七个：`arch`、`lang`、`os`、`package-version`、`retry-count`、`runtime`、`runtime-version`

同一次抓包里，官方 opencode 客户端（`User-Agent: opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4`）的请求**这两组头一个都没有**。也就是说它们不是官方形态的一部分，而是插件与 Node SDK 自行附加的。

把它们转发给上游有两个问题：一是把内部插件名与隧道标记（`dsh`、`x-bili-tunnel: 1`）泄露给第三方服务；二是请求头集合与官方客户端不一致，多出可被识别的非官方特征。

## 决定

`internal/relay/channel.go` 新增 `opencodeOfficialHeaders` 白名单与 `stripNonOfficialHeaders`，在 `applyChannelConfig` 入口调用，条件为 `channel.OpencodeCompat` 为真。**不在白名单内的一律删除**，而不是只删已知的几个前缀：

白名单 = 抓包实证的 15 个官方头（`accept`、`accept-encoding`、`authorization`、`content-type`、`content-length`、`connection`、`host`、`user-agent`、`x-opencode-client`、`x-opencode-project`、`x-opencode-request`、`x-opencode-session`、`x-opencode-session-id`、`x-session-affinity`、`x-session-id`）+ 鉴权头（`x-api-key`、`x-goog-api-key`、`api-key`）+ 协议头（`anthropic-version`、`anthropic-beta`）+ 追踪头（`traceparent`、`b3`）+ 父会话头（`x-opencode-parent-session-id`、`x-opencode-parent-id`）。

选白名单而非黑名单：黑名单只能拦住今天已知的 `x-bili-*` 与 `x-stainless-*`，明天换个插件加一批新前缀就漏了；白名单默认拒绝，新增头必须显式放行，才能保证「上游只看到官方头」这个不变量。

作用域限定在 `OpencodeCompat` 渠道：非 opencode 渠道（OpenAI、Anthropic 官方接口）可能合法使用 `x-stainless-*` 作为 SDK 指纹，全局剥离会误伤。剥离发生在渠道自定义头注入之前，因此 `custom_header` 配置的头不受影响。

## 备选方案

- **黑名单只删已知前缀（`x-bili-`、`x-stainless-`）**：改动最小，但只能拦住今天见过的头。第三方插件换个前缀（例如 `x-acp-`、`x-dsh-`）就漏过去了，白名单默认拒绝才能守住「只传官方头」这个不变量。
- **在客户端侧关掉这些头**：治不了其他第三方客户端，网关仍会对未整改的客户端透传。且 dsh 插件的头是其隧道功能所需，客户端侧未必能删。
- **做成渠道配置项（`header_strip_prefixes`）**：更灵活，但要新增数据库列、`ChannelUpdateInput` 字段与前端表单项，本次需求只有一个场景，属于过度设计。
- **在建连前重建整个头集合（白名单只留官方头）**：这正是本次采纳的做法，见「决定」。
- **只剥 `x-bili-*` 不剥 `x-stainless-*`**：抓包显示官方客户端两个都没有，只剥一半等于保留另一个非官方特征。

## 后果

- **作用面**：仅 `OpencodeCompat` 渠道的出站请求；其他渠道与其他转发路径不受影响。
- **收益**：上游只看到官方客户端会发的头，内部插件标识不再外泄。
- **代价**：如果某个客户端依赖 `x-stainless-*` 触发上游的特定行为（如 SDK 版本路由），剥离后行为会变。目前无证据表明上游按该头分流。
- **配套改动一**：`x-opencode-request` 在 v2 档（`/inference`）改为**同样注入**。原实现据「v2 网关对此头返回 403」在 v2 分支提前 return 跳过该头，但 2026-10-30 抓包显示官方 1.19.4 客户端在 `/inference` 上**两个头都发**（`x-opencode-request: msg_18861d9ce001...` 与 `x-session-affinity: ses_e779...` 同时存在），旧假设已被实测推翻。改动落在 `internal/relay/opencode_free.go` 的 v2 分支。
- **配套改动二**：v1 档（`/zen`）补发 `x-session-affinity` 与 `X-Session-Id`。原实现只让 v1 镜像 `x-opencode-session-id`，但实抓的 v1 客户端（UA `opencode/1.19.4`）三个头齐全，说明「v1 不发 affinity」同样是过时假设。改动落在 `internal/relay/random_headers.go` 的 v1 分支。
- **文档同步**：`docs/modules/relay.md`、`opencodeid.go`、`builtin.go`、`random_headers.go` 四处描述一并纠正。

## 验证

```sh
go build ./...
go test ./internal/relay/ -run 'TestChannel' -v
```

部署后以 dsh 客户端发一次对话，看 debug 日志的出站头：

```sh
docker logs novaveil --since 2m 2>&1 | grep -A 25 'outbound headers'
```

预期：`x-bili-*` 与 `x-stainless-*` 全部消失，`x-opencode-client`、`x-opencode-project`、`x-opencode-session` 等官方头仍在。
