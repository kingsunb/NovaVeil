# Agent Note: 鉴权方案解析、路由非法方法、前端 401 广播与 PR 检查审计修复

Status: implemented

## 问题

2026-09-20 后续审计确认四个跨层问题：

1. **SEC-06**：`APIKeyAuth` 对 `Authorization` 头先 `TrimPrefix("Bearer ")` 再用其余谓作 API Key——非 Bearer 方案会被误当成 Key 继续查库，且 `Bearer` 大小写语义多余。
2. **SEC-07**：`router.registerRoute` 对未知 HTTP 方法静默回退成 GET 注册，配置错误被隐藏。
3. **FE-01**：前端只把 local 定义的 401 广播给 401 处理器；`fetch` 响应 401 与流式聊天/APIKey 前端广播不同源，聊天页不清会话、不刷新脱敏会话状态。
4. **DEV-06**：PR 模板第二项只要求“不包含测试文件”即可裸更不解释，不能保证契约/行为修复带测试。

## 决定

- **SEC-06**：`APIKeyAuth` 改用 `strings.Fields` 切分 `Authorization`，必须恰好两个字段且第一段 `EqualFold` 为 `Bearer`，否则直接 401，不把非 Bearer 方案落入 Key 查询分支。
- **SEC-07**：`registerRoute` 返回 `error`，未知方法用 `fmt.Errorf` 报错；`RegisterAll` 向上传播错误而不是吞掉。路由注册失败会被启动格挡。
- **FE-01**：`api.ts` 导出 `broadcastStreamAuthFailure(res, message)`，对 `/chat/completions` 的流式非 OK 响应先广播 401；`Chat.tsx` 监听 `apiUnauthorizedEvent` 清会话和消息，并刷新脱敏会话 ID；`clearSession` 同时清 `CHAT_SESSION_KEY` 与 `CHAT_MASK_SESSION_KEY`。
- **DEV-06**：`template-check.yaml` 的 requiredLines 第二项改成：契约/行为修复包含对应测试；纯文档或纯文案 PR 才可以不带测试文件。

## 备选方案

- **SEC-06 保留 TrimPrefix 但大小写不敏感**：改动更小，但 `Basic` 等方案仍会落入查库分支，歧义语义保留；选择显式拒绝非 Bearer。
- **SEC-07 改成日志告警并回退 GET**：更兼容，但会掩盖错误配置，选择启动期失败。

## 后果

- **收益**：非法鉴权方案被拒绝；错误路由方法在启动期可见；前端聊天收到 401 时立即清理残保留并广播全局登录态；PR 检查把测试义务显式化。
- **代价与已知上限**：若未来支持非 Bearer 鉴权（如 `Api-Key` 方案）需重访 `APIKeyAuth`；`registerRoute` 也要求在 `RegisterAll` 时对错误做处理。

## 验证

- `internal/server/middleware/auth_test.go`：非 Bearer 方案 401 且不落 Key 查询。
- `internal/server/router/router_test.go`：未知方法返回错误，`RegisterAll` 传播错误。
- 后端 `go build ./...` 与受影响包 `go test` 通过。
- 前端 `pnpm typecheck`、`pnpm vitest run` 通过（`api.test.ts`、`Chat.tsx` 相关用例）。