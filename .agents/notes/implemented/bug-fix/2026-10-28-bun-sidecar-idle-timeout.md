# Agent Note: 关闭 bun sidecar 的 10 秒空闲掐流

Status: implemented

## 问题

OpenCode 免费档的思考模型频繁断流：客户端看到流被截断，网关错误日志稳定出现 `upstream_network / unexpected EOF`，用量记 0。现象看起来像上游掐连接，实际抓包（网桥上只看 TCP 元数据）显示相反：

- 连接 `172.18.0.3:55660 → 172.65.90.23:443` 存活 6 分钟，双向正常（收 4.76 MB、发 798 KB）；
- 上游最后一个字节在 `22:23:58.99`，之后静默；
- `22:24:10.616` **我们这一侧发出 RST**，9 毫秒后落错误日志。

也就是说没人掐我们，是我们自己把流掐了。差值 11.6 秒正好压在 Bun 的默认空闲阈值上。

免费档走 `client.BunTransport`，请求由 bun sidecar 转发：sidecar 用 `Bun.serve` 暴露 `/forward`，把 `fetch` 的上游响应体直接作为流式响应体回给 Go。脚本没有设置 `idleTimeout`，而 Bun 官方文档写明：`Bun.serve` 默认 `idleTimeout` 为 10 秒，且**空闲计时器在流式响应期间同样生效——流里超过阈值没有字节，Bun 会在响应中途关闭连接**。思考模型在推理阶段静默十几秒是常态，于是每次长思考都必现截断；Go 侧读到分块体没有结束分块，只能得到 `unexpected EOF`。

## 决定

`internal/client/bun_sidecar.js` 的 `Bun.serve` 增加 `idleTimeout: 0`，关闭 sidecar 侧的空闲超时。sidecar 只服务本进程的转发请求（`127.0.0.1`、仅 `/forward`），连接生命周期应当由 relay 自己的超时与取消（首事件超时、流空闲超时、成员超时、客户端取消、并发槽位释放）控制，而不是由承载它的 HTTP 服务器再叠一层与业务无关的 10 秒。

`internal/client/bun_sidecar_test.go` 增加进程级回归 `TestBunSidecarProcessStreamSurvivesIdleGap`：上游先写一帧、静默 11 秒再写第二帧，断言第二帧到达且读取无错。该用例需要 bun，未配置 `NOVEIL_BUN_BIN`/`BUN_BIN` 或 `-short` 时跳过。

## 备选方案

- **把 `idleTimeout` 设成 255（Bun 允许的最大值）**：静默超过 4 分钟仍会掐流。思考模型并非不可能停这么久，等于把同一个缺陷推远一点，不解决根因。
- **只对 `/forward` 调 `server.timeout(req, 0)`**：效果与全局关闭一致，但多一层运行时调用；sidecar 别无他用，全局关闭更直白。
- **由我们插心跳保活**：SSE 帧由上游产生，sidecar 无法凭空补帧；伪造注释帧会污染下游语义，且掩盖上游真实静默。
- **在 Go 侧把读错误当正常结束**：数据确实缺失，放行会把截断的回答当完整交付，属于造假。
- **换掉 sidecar，改用纯 Go 的 uTLS 客户端**：指纹维护成本与风险都高，且当前形态已在生产验证，为这个缺陷不值当。

## 后果

- **收益**：思考阶段静默不再掐流，免费档长轮次可以完整跑完；`unexpected EOF` 这一类错误从根上消失，客户端不再整轮重试。
- **代价**：sidecar 不再回收"空闲"连接，连接释放依赖 Go 侧取消与 transport 的 `IdleConnTimeout`（90 秒）；bun 进程随父进程退出，泄漏窗口有界。
- **作用面**：只影响走 `BunTransport` 的渠道（OpenCode 免费档 `OpencodeCompat && tier=zen`）。常规 Go 客户端路径不受影响。
- **口径提醒**：`unexpected EOF` 之前被归到 `upstream_network`，本次修复后若仍出现，才是真正的上游或链路截断。

## 验证

```sh
# 需要 bun 可执行文件; 未配置时该用例跳过
NOVEIL_BUN_BIN=/usr/local/bin/bun go test ./internal/client/ -run 'TestBunSidecarProcess' -v
go test ./internal/client/    # 全量回归
```

- 第一条覆盖 sidecar 的两个进程级行为：正常转发与"静默 11 秒后继续产出"，后者在关闭空闲超时之前必然失败（读取报 `unexpected EOF`）。
- 部署后观察面板错误日志：思考轮次不应再出现 `unexpected EOF`；`usage_buckets` 中该模型的 `error_count` 应回落。
