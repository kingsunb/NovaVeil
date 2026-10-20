# Agent Note: OpenCode 免费渠道只同步 free 模型，列表标注自动同步状态

Status: implemented

## 问题

两个问题都出在「内置免费渠道出厂不自动同步」这条既有设定上。

一是同步范围：一旦管理员给 OpenCode 免费渠道打开 AutoSync，`SyncModelsTask` 会把 `/v1/models` 返回的全部模型都写成 auto 模型，包含付费模型。免费档渠道的 Key 是公开的 `public`，这些付费模型既不可用，又会在模型列表与分组选择里制造噪音。

二是可见性：渠道列表只显示「免费 / 内置」两个标签，看不出这个渠道到底开没开自动同步。而上下文窗口、能力徽章依赖同步结果，用户看到徽章不显示时无法从列表判断原因，只能点进编辑器翻开关。

## 决定

### 免费渠道只同步名字含 free 的模型（`internal/task/sync.go`）

重建 auto 模型集合时增加一道过滤：

```go
onlyFree := channel.IsFree && channel.OpencodeCompat && model.OpenCodeTier(channel.BaseURL) == "zen"
```

命中则该轮只保留模型名（忽略大小写）含 `free` 的条目，其余跳过。手工添加的模型不受影响——过滤发生在 `manualNames` 判断之后，手动模型本来就走另一条分支。

判定用三个条件的交集，不单看档位：

- `IsFree`：内置免费渠道由 `freeChannelFromDefinition` 强制写 true；付费渠道与用户自建渠道为 false，因此付费 OpenCode 渠道（`/zen/v1`，档位 go）与其他免费渠道（如 Kilocode，非 OpenCode 兼容）都不受影响。
- `OpencodeCompat` + 档位 zen：把范围限定在四个内置 OpenCode 免费渠道（`/inference` × 3 与 `/zen` × 1）。

### 渠道列表标注自动同步状态（`web-next/src/pages/Channels.tsx`）

名称行在「免费 / 内置」之后追加一枚标签：`auto_sync` 为真显示「自动同步」，为假显示「未自动同步」。两个状态都显式呈现，而不是只在开启时才出现——用户需要能一眼看出「这个渠道没开同步，所以没有 ctx 徽章」。

## 备选方案

- **只按档位判定（`OpencodeCompat && tier == zen`）** — 少一个条件；否掉是因为档位为 zen 的非免费渠道也会被误伤，且现有同步测试用的渠道不设 `IsFree`，会被这条规则连带过滤，测试语义被污染。
- **按模型名是否含 `free` 在展示层过滤，同步仍写全量** — 不改同步逻辑；否掉是因为脏数据会一直留在库里，分组选择、评估排序、用量统计都要各自再过滤一遍，等于把一处判断散成四处。
- **只在编辑器的开关处提示同步状态** — 改动最小；否掉是因为用户定位问题正是从列表开始的，必须点进去才能判断等于没解决。

## 后果

- **收益**：免费渠道的模型列表只留可用模型；列表页能直接看出同步开关状态，徽章不显示时有明确解释。
- **代价与已知上限**：过滤依赖模型名含 `free` 这个约定，上游若改名（例如以 `-trial` 结尾）会漏掉或误判；`IsFree` 是渠道级标记，用户把自建渠道标成免费也不会触发过滤（因为还要求 `OpencodeCompat`）。前端只加了展示，没有把开启同步做成列表页的快捷操作。
- 重访信号：上游免费模型命名规则改变，或出现非 OpenCode 的免费渠道也需要同样的模型过滤。

## 验证

- `internal/task/sync.go` 中 `onlyFree` 出现在 auto 模型重建循环之前，且过滤条件在 `manualNames` 判断之后。
- 免费渠道（`IsFree && OpencodeCompat && BaseURL` 含 `/zen` 或 `/inference`）同步后，`channel_models` 里 auto 来源的行全部含 `free`。
- 渠道列表（`/channels`）每个渠道卡片都带「自动同步」或「未自动同步」标签之一；`sampleChannel.auto_sync=false` 时渲染为「未自动同步」。
