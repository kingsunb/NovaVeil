# Agent Note: shell 工具描述平台措辞实抓修正（Windows 也是 bash，非 powershell）

Status: implemented

## Problem

2026-10-23 在 Windows 11（Git Bash 与 PowerShell 两种启动环境、后者已清 SHELL 变量）用官方 `@opencode/cli@2.1.4` 对本地 sink 实抓，与 Linux 官方实抓全量逐字节 diff：12 个工具定义中唯一差异是 shell 描述的平台词，真实 Windows 客户端发的是：

> `Commands run on Windows using bash.`

而 [v2 parity 笔记](./2026-10-24-opencode-v2-fingerprint-parity.md) 按源码分析把 `opencodeToolDescription` 定为替换成 "on Windows using powershell"——恰好制造出一个真实客户端不会发的字符串，方向反了。措辞源头是 opencode2api 的 `v2ShellDescription`（[2026-10-19 笔记](../architecture/2026-10-19-opencode2api-compat-followup.md)），源码分析沿用了它。

二进制侧证据：`opencode.exe` 中 `"using bash"` 整串出现 0 次，描述是 `on ${平台} using ${shell}` 运行时拼接；Windows 上官方客户端固定以 Git Bash 作为执行 shell（与启动它的 shell 无关），故解析结果恒为 bash。

## Decision

`internal/relay/opencode_free.go` 的 `opencodeToolDescription`：Windows 平台只替换平台词，`"on Linux using bash"` → `"on Windows using bash"`，shell 词恒为 bash。比对（`opencodeToolMatches`）与注入（`opencodeFreeTool`）两条调用路径自动随之生效：Windows 网关上真实 opencode 客户端的 shell 描述能匹配通过不再触发替换，注入/替换生成的定义与官方 Windows 客户端逐字节一致。

## Alternatives considered

- **保留 powershell 措辞，兼容无 Git Bash 的 Windows 环境** — 上游逐字节比对的对象是官方客户端的产出；无 Git Bash 时官方客户端的措辞未实证（本机装了 Git Bash，无法构造该场景），为一个未证实的分支保留与主流实抓相悖的字符串不合算；否掉。
- **从入站请求探测客户端 shell 再决定措辞** — 入站是 Claude Code 等非 opencode 客户端，其运行 shell 与官方 opencode 客户端的探测结果无关，探不出来也没有意义；否掉。

## Consequences

- **收益**：Windows 网关的 shell 描述与官方 Windows 客户端实抓逐字节一致；`opencodeToolMatches` 不再因措辞差一个词而触发不必要的工具替换。
- **代价与已知上限**：无 Git Bash 的 Windows 环境官方措辞未实证（可能是 powershell），重访信号是上游对无 Git Bash 环境的请求出现工具描述 403。措辞源头 opencode2api 的 `v2ShellDescription` 与官方行为不符，继续以它为参照会复现同类偏差。
- **重访信号**：opencode 2.1.5+ 改变 shell 探测逻辑（如 Windows 原生 shell 化）或上游校验规则变化。

## Verification

```sh
go test ./internal/relay/ -run TestOpencodeToolDescriptionPlatform
```

- 实抓样本：`Windows` 官方 `20261023T205144.633980Z-003-v2.body` 与 `20261023T205328.470266Z-005-v2.body`（本机 `~/opencode-fp/captures/`），Linux 官方样本在抓包机 `/data/workspace/fingerprint/captures/20261023T185730.677423Z-002-v2.body`，二者除 shell 平台词外逐字节一致。
- `TestOpencodeToolDescriptionPlatform`：Windows 平台替换为 "on Windows using bash"，其余平台与非 shell 工具原样返回。
