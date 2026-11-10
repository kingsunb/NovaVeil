# Agent Note: Bun 传输重放边界、模型分页截断与启动同步生命周期

Status: implemented

> 2026-11-11：Bun 重放边界改为所有方法重放，见 [2026-11-11-bun-replay-all-methods.md](2026-11-11-bun-replay-all-methods.md)。本笔记其余决定仍有效。

## 问题

静态审计发现四个独立缺口：BunTransport 在复用连接断开后对 POST 也自动重放一次，可能重复提交；Anthropic 模型列表最多读 32 页后把部分结果当成功，SyncModelsTask 会据此删除渠道 auto 模型；cmd/start.go 在 task.RUN 之外另起 goroutine 做启动同步，绕过 runningWG，停机时可能与 DB 关闭竞态；bunClients 按代理地址无上限增长。

## 决定

- BunTransport 只对 GET/HEAD/OPTIONS/TRACE 在复用连接、首字节前断开时重放一次；POST 等非幂等方法直接返回错误。
- fetchAnthropicModels 在 32 页上限后仍有 has_more 时返回 ErrModelListTruncated，不返回部分列表；SyncModelsTask 识别该错误并保留现有模型，不做替换。
- 删除 cmd/start.go 的临时启动同步 goroutine，启动同步只由 task.Init 注册的 runOnStart 入口触发，纳入 runningWG 与 StopAll 生命周期。
- bunClients 复用 maxCustomProxyClients=64 软上限；超过时清空缓存并关闭空闲连接，再按需重建。
- 旧笔记 [修复线上 8888 体检发现的低风险服务问题](./2026-10-22-audit-8888-low-risk-fixes.md) 中“保留两个启动入口”的决定由本条取代；ErrSyncInProgress 仍保留用于并发同步语义。

## 备选方案

- **继续对所有方法重放一次** — 能最大限度掩盖连接复用抖动，但 POST 可能重复创建/计费，且无法确认上游是否已处理；否掉。
- **32 页后返回部分列表并告警** — 可保留更多模型，但 SyncModelsTask 会把它当完整列表删除缺失的 auto 模型，破坏分组/评估引用；否掉。
- **保留 cmd/start.go 的独立 goroutine 并加 recover/等待** — 能保留启动即同步的时序，但需复制任务生命周期与停机等待；runOnStart 已有同样语义且纳入 runningWG，否掉。
- **bunClients 按 key 做 LRU 逐出** — 更平滑，但现有 customProxyClients 采用整体清空，复用同款实现更少代码；当前连接池按代理地址复用，整体清空只关闭空闲连接，因此不选 LRU。

## 后果

- **收益**：非幂等请求不再被静默重放；分页截断不再触发破坏性模型替换；启动同步与停机等待统一由 task 包管理；Bun 客户端缓存有硬上限。
- **代价与已知上限**：POST 在复用连接断开时会把 EOF 返回给调用方，由上层决定是否重试；超过 32 页的模型列表会同步失败并保留旧模型；清空 bunClients 会让下一次请求重建连接池；启动同步现在发生在 server.Start 之后、task.RUN 启动时，时机略晚但仍在服务就绪后的首个调度内。

## 验证

- `go test -count=1 ./internal/client ./internal/helper ./internal/task`
- `go test -race -count=1 ./internal/client ./internal/task`（若环境支持）
- 手工检查：POST 复用连接断开只发 2 次请求；GET 发 3 次；Anthropic 列表第 32 页仍 has_more 时返回 ErrModelListTruncated。
