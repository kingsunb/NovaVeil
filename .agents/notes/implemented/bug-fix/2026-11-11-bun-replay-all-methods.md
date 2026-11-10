# Agent Note: Bun 传输恢复所有方法断连重放

Status: implemented

## 问题

`BunTransport` 在复用连接、首字节前断开时会把请求重放到新连接。审计曾建议只对幂等方法重放，避免 POST 重复提交或重复计费；用户要求保持原有行为，认为连接抖动造成的失败比小概率重复更不可接受。原决定见 [2026-11-10-bun-replay-and-sync.md](2026-11-10-bun-replay-and-sync.md)。

## 决定

- 所有 HTTP 方法在 `info.Reused && dropped && !retried && ctx.Err()==nil` 时重放一次。
- sidecar 路径仍不重放；连接缓存 64 软上限、Anthropic 分页截断、启动同步生命周期修复保持不变。
- 测试同时覆盖 POST 与 GET：预热连接后断开，均重放一次并成功。

## 备选方案

- **仅幂等方法重放**：可避免 POST 重复提交，但会把连接复用抖动放大为请求失败；用户明确否掉。
- **完全不重放**：行为变化更大，且 GET 也会受连接抖动影响。
- **在 relay 层做请求级重试**：需要重新构造请求体并决定是否复用 Key/渠道，复杂度和重复计费风险更高。

## 后果

- POST 在复用连接断连时可能被上游处理两次，需接受重复计费/重复生成的极小概率。
- 连接复用抖动不再直接暴露给调用方；GET/HEAD 等幂等请求同样只重放一次。
- 业务层仍保留自己的重试与故障转移，不依赖传输层兜底。

## 测试

- `internal/client/bun_test.go` 的 `TestBunTransportReplaysNonIdempotentOnce` 与 `TestBunTransportReplaysIdempotentOnce`。

## 验证

```sh
go test ./internal/client -count=1
```
