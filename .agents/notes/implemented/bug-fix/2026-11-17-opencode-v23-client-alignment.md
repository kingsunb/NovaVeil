# Agent Note: 对齐 opencode v2.3.0 客户端形态

Status: implemented

## 问题

上游 opencode 已发布 v2.3.0（2026-11-13），Nova 的免费档指纹仍停在 2.2.6。旧注释还写着「v2 客户端实抓两个头都发」，但 2.2.6 之后官方可能改了头集，Nova 按旧观测发送 `x-opencode-request` 属于多带头。

同时 `x-opencode-project` 在无客户端信号时回退字符串 `global`，而官方客户端从不下发该值。

另外 1.x 免费档停售后一度把四条 v1 渠道全部下线，实际仍需要一条 v1 渠道做旧客户端复现与形态对照。

## 决定

- `opencodeV2ClientVersion` 升到 `2.3.0`。依据是 v2.3.0 二进制内嵌 UA 字符串与两台机器的实抓，不靠 release tag 推断。
- v2 不再发送 `x-opencode-request`：2.3.0 的 title 请求与主请求实抓均无此头，v1（1.19.5）仍然发送。v2 分支改为显式 `Del`，防止存量静态头残留；`random_headers.go` 与 `opencodeid` 注释同步改为「仅 v1」。
- `x-opencode-project` 在 v2 档按渠道派生稳定 40-hex（`sha256("novaveil:opencode-project:"+channelID)[:40]`）；v1 档保持 `global`（1.19.5 实抓即此值）；客户端自带合法 40-hex 时原样透传。
- 内置 OpenCode 官方渠道的 `x-opencode-client` 与静态 UA 由 `desktop` 改为 `cli`，新增迁移 30 归一存量库（只改精确的 desktop 值，其他自定义头保留）。
- 恢复一条 `OpenCode Free V1`（lane v1、public、与 V2 同模型集），其余 v1 副本（账号池、备用1、备用2）仍下线。
- `opencode_probe_v2.json` 按 2.3.0 实抓重建：system 由 185 行/8244 字符升到 191 行/8619 字符（Code Mode 段落重写，新增 session_move 与 worktree 系列说明），工具集由 12 个增到 13 个（新增 `experimental_image_generate`）；动态字段仍参数化为 `{{MODEL_NAME}}`、`{{MODEL_ID}}`、`{{DATE}}`、`{{WORKDIR}}` 四处，`Workspace root folder` 沿用固定值。v1 模板不动（1.19.5 已停售，模板本就取自 1.19.4）。

## 依据

两台机器、三个目录的实抓（2026-11-17）：

| 位置 | `x-opencode-project` |
|---|---|
| 华为云上海 `/home/admin` | `a621bf5c72a88489721dfe4a14241ed2bc5ebcbe` |
| 本机 `D:\AI\github\kingsun\NovaVeil` | `86e7e3a91496dc9289f24601b56978209724e85d` |
| 本机 `C:\Users\Administrator` | `09d316790ad6d1065a53594da8cc677f99ffde52` |

同一目录跨 TUI 进程重启稳定，不同目录不同 → project 是**按项目根目录首次生成并持久化**的随机值，不是按机器、也不是每请求随机。故 Nova 按渠道派生相当于把每个渠道当成一个固定项目根。

头集实抓（三次请求一致）：`User-Agent`、`x-opencode-client: cli`、`x-opencode-project`、`x-opencode-session`/`x-opencode-session-id`/`x-session-affinity`/`x-session-id`（同值）、`b3`、`traceparent`、`Accept: */*`、`Accept-Encoding: gzip, deflate, br, zstd`；无 `x-opencode-request`、无 parent 头。

## 备选方案

- 保持 2.2.6：能跑，但落后两个小版本，且继续发 2.3.0 已移除的头。
- project 保持 `global`：官方从不发该值，属于可识别差异；不改没风险但也没有改进。
- project 按会话派生：会让每个会话都像不同项目，比官方更细，反而偏离。
- 把 project 做成实例级常量并持久化：最贴近「一个安装一个项目」，但要多一层存储；当前按渠道已够用。

## 后果

- 免费档出站 UA 为 `opencode/latest/2.3.0/cli`，与官方一致；v2 请求少一个头，更接近真实客户端。
- project 从 `global` 变为每个渠道一个稳定 40-hex；同一渠道跨目录会话共用一个 project（官方会不同），属于已知近似。
- 升级到含迁移 30 的版本后，内置 `OpenCode` 官方渠道的 desktop 头被改为 cli；`OpenCode Free V1` 若在迁移 29 被删过，会在下一次启动由 `EnsureBuiltinChannels` 重建（新 ID，需要重新加入分组）。
- 内置免费渠道总数 16 → 17；体检与整形注入的系统提示切到 2.3.0 版本，工具集多一个 `experimental_image_generate`，客户端未声明等价名时官方工具原样透传。

## 验证

由用户执行（AGENTS 禁止 AI 运行 build/test/vet）：

```sh
go test ./internal/relay -run 'TestInjectOpencodeCorrelationHeaders|TestApplyOpencodeFreeLaneHeaders|TestOpencodeV2|TestShapeOpencodeFree' -v
go test ./internal/utils/opencodeid ./internal/db/migrate ./internal/builtin -v
go test ./...
```

线上复核：发一条免费档请求，抓 `x-opencode-project` 应为 40 位 hex（非 `global`），`x-opencode-request` 不应出现。
