# Agent Note: Bun sidecar 本地回环鉴权 token

Status: implemented

## 问题

Bun sidecar(为 OpenCode 形态请求提供本地 TLS 指纹代理, 见 [TLS 行为](2026-10-26-opencode-bun-sidecar-tls-behavior.md)与[空闲超时](2026-10-28-bun-sidecar-idle-timeout.md))只绑定 127.0.0.1 随机端口但无任何鉴权: 同机任意进程都能向它 POST 任意 URL/任意头, 把它白嫖成转发代理, 借用网关进程的网络环境与出口形态。

## 决定

- `internal/client/bun_sidecar.go` 启动时用 crypto/rand 生成 128 位 hex token, 经环境变量 `NOVEIL_SIDECAR_TOKEN` 传入 sidecar 进程(沿用该 sidecar 既有的 NOVEIL_ 环境变量前缀)。
- sidecar(`bun_sidecar.js`)对每个 /forward 请求校验 `X-Noveil-Sidecar-Auth` 头, 缺失或不匹配一律 403, 校验先于解析转发载荷。
- 该头仅用于 Go 与 sidecar 的本地握手: Go 侧设在发往 sidecar 的本地请求上; 转发上游的头完全来自载荷的 `input.headers`, 鉴权头无路径进入上游请求(对上游指纹敏感, 见 OpenCode 形态对齐约定)。
- stdout 回传 `{port}` 的现有握手不动, token 走环境变量不占 stdout。

## 备选方案

- **token 走 stdout 握手回传** — 最强论据是不引入环境变量; 否掉因为 stdout 是端口回传的专用通道, 混入 token 需改握手协议且引入解析顺序耦合, 环境变量是进程间传递秘密的标准位。
- **只靠 127.0.0.1 绑定** — 最强论据是回环地址外网不可达; 否掉因为威胁模型是同机进程(其他用户会话、被攻陷的本地脚本), 回环绑定对它们全部可达, 绑定不构成鉴权。

## 后果

- **收益**: 同机进程无法再未授权使用 sidecar 的转发能力。
- **代价**: 每请求一次字符串比较(可忽略); 新增一个环境变量与一个本地头, 需与转发载荷头严格隔离(有测试钉住)。

## 验证

- `internal/client/bun_sidecar_test.go`: 无头/错 token 一律 403、对 token 放行(真实 bun 进程, short 模式跳过); RoundTrip 回归断言鉴权头不进入转发载荷。
