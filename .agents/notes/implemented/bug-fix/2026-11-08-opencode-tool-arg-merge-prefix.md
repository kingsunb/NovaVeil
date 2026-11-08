# Agent Note: 修复工具参数流式拼接的前缀误吞

Status: implemented

## 问题

2026-11-08 用户报告：dsh harness 经本地 NovaVeil 走 OpenCode Free V1 渠道时，`compress` 工具调用失败。

```
Unexpected token 's'
Received arguments: {"content": [startId": "b112", "endId": "b140", ...}
```

数组第一个元素缺开头的 `{"`，JSON 解析在 `s` 处失败。

排查结论（重要，与最初判断相反）：

- **工具定义没有被改写**。`ensureOfficialToolset` 只把官方 14 个工具插到数组最前，客户端自带工具经 `ocToolRename` 仅改 `function.name`，`description` 与 `parameters` 原样保留。
- **参数没有被适配改写**。`flushPending` 里只有 `official==true`（官方工具）才调 `adaptOfficialToolArguments`；`compress` 是哈希别名，`official=false`，参数原样透传。
- 唯一会动参数字节的是 `mergeOfficialToolArguments` 的前缀判定，见下。

原实现两个前缀分支都可能吞内容：

```go
if strings.HasPrefix(next, previous) { 替换 }   // 等长也会命中
if strings.HasPrefix(previous, next) { return } // 真前缀增量被当重复丢弃
```

第二个分支是本轮修复对象：增量片段若恰好是已累积内容的真前缀（例如累积到 `{"a":1}` 后上游发来 `{"`），旧逻辑直接丢弃该片段，拼接结果缺字符。

## 决定

`internal/relay/upstream.go` 的 `mergeOfficialToolArguments` 收紧判定：

1. `next == previous`：完全重复，忽略（保留原有防重复能力）。
2. `len(next) > len(previous) && HasPrefix(next, previous)`：严格超集，视为累积式重发，整体替换。
3. 其余一律追加，按标准增量流处理。

同时新增诊断：`flushPending` 合成工具调用时若 `!gjson.Valid(args)`，用 `log.Warnf` 打出工具名、长度与首 60 字节，便于下一次复现时立刻区分「上游给的」还是「我们拼坏的」。

## 备选方案

- **在合成前修复缺失的 `{`**：属于创可贴，且无从判断缺的是哪一层括号，否决。
- **放弃本地合成、直接透传上游 tool_calls**：会丢掉工具名从哈希还原成客户端名的能力，客户端无法执行，否决。
- **只在 JSON 非法时回退用最后一帧原始参数**：最后一帧可能只是增量片段，回退后参数同样残缺，否决。

## 后果

- 收益：增量流不再可能因前缀判定丢字符；参数一旦非法会留下可检索的告警，不必再靠猜测定位。
- 代价：若某上游确实以「等长重复」以外的真子集形式重发参数，现在会追加而非忽略，可能产生重复内容。标准 OpenAI 流式不受影响。
- **已知上限**：本轮没有确证用户那次失败的根因就是该分支。工具定义未被改写这一点已由代码路径证明，但参数是上游生成错误还是本地拼接错误，仍需按新告警实测一次才能定论。不宣称本次修复必然解决问题。

## 验证

由用户执行（AGENTS 禁止 AI 运行构建与测试）：

```sh
go test ./internal/relay -run 'TestMergeOfficialToolArguments' -v
go build ./...
```

新增 `upstream_tool_args_test.go` 覆盖四类输入：标准增量、累积重发、完全重复、真前缀增量不被吞。

复现排查：用 harness 再发一次 `compress`，若 NovaVei 侧出现 `tool call "compress" 参数非法 JSON` 告警，说明上游给的就是残缺参数；若没有告警而客户端仍报错，则问题在客户端解析侧。
