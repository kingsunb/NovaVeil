# Agent Note: OpenCode 免费档改用常驻 Bun sidecar 真发

Status: implemented

## Problem

2026-10-26 的对照证明：NovaVeil 的 uTLS 传输已经能把 TLS ClientHello 做到与官方 1.19.4 逐字节相同（1502 字节、13 个扩展、ID 与长度全同），HTTP 头序、body 键序、11 个工具定义也已对齐，但同一出口上官方 CLI 返回 200、NovaVeil 返回 429。

关键判据是一次只改传输实现的控制实验：

1. 官方 CLI 直连某 resin 出口：`200 PONG`。
2. 同一账号、同一分钟内，让官方 CLI 经过本地 TLS 终结代理（上游改为 Python/OpenSSL 重连）：`429 FreeUsageLimitError`。
3. 立刻用同一官方 CLI 直连同一账号：`200 PONG`。

请求内容完全由官方 CLI 生成，唯一变量是上游看到的 TLS 连接行为。结论：上游判据不在 ClientHello 报文，而在 TLS 栈的行为特征（会话票据、记录层分片、KeyUpdate 时序等）。uTLS 只能模拟报文，无法模拟 BoringSSL 的连接操作，因此继续补扩展或改签名算法不会有效。

## Decision

1. 新增 `internal/client/bun_sidecar.go` 与 `bun_sidecar.js`：一个常驻 Bun 进程监听本地随机端口，`POST /forward` 接收 `{method,url,headers,body(base64),proxy}`，由 Bun 的 `fetch` 使用真 BoringSSL 发往上游，并把状态码、响应头和响应体流式回传。
2. `GetBunClient` 在显式配置 `NOVEIL_BUN_BIN` / `BUN_BIN` 时返回 `bunSidecarTransport`；未配置时回退现有 uTLS `BunTransport`，显式配置但无法启动时直接报错，不静默降级。不在 PATH 中自动探测 Bun，避免测试环境或意外安装改变线上行为。
3. 代理按请求传给 sidecar，而不是按代理启动进程。因此几千个 `{account}` 出口仍只对应一个 Bun 进程。
4. 上游的所有 HTTP 响应（包括 429/403）原样透传；只有 sidecar 自身网络失败才返回带 `x-noveil-sidecar-error` 头的 502。这样 NovaVeil 能读到真实状态码与 `Retry-After`，账号池才会正确冷却、换号。
5. sidecar 的响应体由 Bun fetch 自动解压，因此回传时删除上游的 `content-encoding` 与 `content-length`，避免长度/编码与已解压正文不一致。
6. sidecar 启动时把父进程 PID 传入，父进程退出后自行退出；Go 侧在 sidecar 异常退出后按需重启。

## Alternatives considered

- **完整 `opencode serve` 子进程池**（oc-bridge 形态）— 与官方客户端同源，确定能 200；否掉：每个会话要管理完整 OpenCode 进程，资源与复杂度远高于一个 fetch 进程，且本轮只缺 TLS 发送层。
- **cgo 链接 BoringSSL** — 一次解决 TLS 行为差异；否掉：构建链、交叉编译和 CI 工具链成本过高，且需要为每个平台维护。
- **继续深化 uTLS** — 改动最小；否掉：判据是连接行为而非报文，无对端反馈时只能盲改；本轮控制实验已证明该方向无效。
- **让 Bun 进程随每个代理账号启动** — 实现直接；否掉：账号池可达数万，进程数不可控。改为单进程按请求传 `proxy`。

## Consequences

- **收益**：免费档请求由真实 Bun/BoringSSL 发出，不再依赖报文级伪装；请求体与工具定义沿用 NovaVeil 现有整形，sidecar 只替换传输层。sidecar 复用同一进程，代理出口数量不影响进程数。
- **依赖**：运行环境必须提供 Bun 1.x；未配置 `NOVEIL_BUN_BIN` 时自动回退 uTLS，行为与改动前相同但仍有 429 风险。Docker 镜像已从 `oven/bun:1.4.4-alpine` 复制同版本 Bun 到 `/usr/local/bin/bun`，并内置 `NOVEIL_BUN_BIN`，无需宿主机另装 Bun。
- **代理限制**：Bun fetch 的 `proxy` 只支持 HTTP/HTTPS 代理，`socks5h://` 会快速失败并明确报错。resin 同时支持 HTTP 与 SOCKS5，渠道模板应使用 `http://Default.{account}:<token>@<host>:2260`。
- **性能**：每个请求多一次本地 HTTP 往返和 base64 编解码；相对上游网络延迟可忽略。所有请求共用一个 Bun 事件循环，需在 50 并发下观察 CPU 与流式延迟。
- **镜像体积**：Bun 二进制约 96 MiB，实测未压缩镜像由 148 MB 增至 236 MB（+88 MB）；从官方 Bun Alpine 镜像复制并追加 `libstdc++` / `libgcc`，运行时仍沿用原 Alpine 3.21.7 基座。
- **安全**：sidecar 仅监听 `127.0.0.1` 随机端口，不暴露公网；传给 sidecar 的代理 URL 含凭据，只存在于本机进程内存与请求 JSON 中。

## Verification

```sh
NOVEIL_BUN_BIN=/path/to/bun go test ./internal/client/ -run 'TestBunSidecar|TestNewBunSidecarClient' -v
go test ./internal/client/ ./internal/helper/ ./internal/model/ ./internal/builtin/ ./internal/db/migrate/
go build ./... && go vet ./internal/client/ ./internal/helper/ ./internal/relay/
```

- `TestBunSidecarRoundTripForwardsAndStreams`：本地转发载荷、代理、头大小写和流式响应契约。
- `TestBunSidecarRoundTripPropagatesError`：sidecar 自身失败必须报错，不能伪造成功响应。
- `TestBunSidecarRoundTripPreservesUpstreamStatus`：429 与 `Retry-After` 必须原样返回给上层。
- `TestBunSidecarProcessRoundTrip`：真实 Bun 子进程收发本地 HTTP 上游；未配置 `NOVEIL_BUN_BIN` / `BUN_BIN` 时跳过。
- 2026-10-26 远程实测：修正官方 ID 算法后，Bun fetch 在同一 CLI 200 出口返回 `STATUS 200`；sidecar 抓包与官方主请求对比，body 键序、`max_tokens=32000`、`tool_choice=auto`、11 个工具逐字节一致，系统提示仅工作目录不同。
- 2026-10-26 最终闭环：6 并发扫描 60 个 resin 账号，命中 `scan1792964122441530` / `65.111.26.69`（官方 CLI 200）；随后 `client.GetBunClient` 经同一账号的 Bun sidecar 返回 `STATUS=200`，耗时 2.104s，响应为真实 SSE。
- 2026-10-26 形态终审：抓官方 CLI 实时主请求体（32600B）与 Nova 生产出站体（33025B）逐字节比对——顶层键序、`max_tokens=32000`、`tool_choice=auto`、`stream_options`、11 个工具定义字节完全一致，system 仅差 `Working directory` 与 `Today's date` 两行（后者由 `{{DATE}}` + 本地时间动态生成），user 文本按客户端输入。二者 sha256 相同的一对（`PROD_TODAY == V5_date`）在有额度节点上返回 200，证明生产出站形态可被上游放行。
- 2026-10-26 运营结论：免费档额度按出口 IP 计且先到先得。官方 CLI 一次 `run` 消耗 title + main 两次额度，用它预扫描只为标记 1 个账号（效率 0.5），而业务请求消耗 1 次额度直接服务 1 个请求（效率 1）；**预扫描是负收益，不要用它预热账号池**。正确做法是让业务请求自己轮换出口，命中即记成功、403/429 即按 `Retry-After` 冷却该账号。

Docker 验证：

```sh
docker build --platform linux/amd64 -f scripts/dockerfile/Dockerfile .
docker run --rm --read-only --tmpfs /tmp:size=64m,mode=1777,noexec \
  --user 10001:10001 --entrypoint /bin/sh <image> \
  -c 'bun --version && echo "console.log(1)" >/tmp/t.js && bun run /tmp/t.js'
```

- 实测镜像内 Bun 1.4.4 可运行，只读根文件系统 + 非 root + `noexec` 的 `/tmp` 下正常；linux/amd64 与 linux/arm64 均已构建验证。
- 实测容器以 `--read-only`、UID 10001 启动后，免费档请求会拉起 `/usr/local/bin/bun run /tmp/novaveil-bun-sidecar-*.js`，并经宿主 resin 返回真实 `status=429`。
- 镜像体积增加约 100 MiB；使用 `COPY --chmod` 避免 `chmod` 触发两个大文件的 overlay copy-up。

## 相关

- 推翻 [v2 档风控调查](./2026-10-25-opencode-v2-lane-investigation.md) 中「差异只能在 TLS 实现层、暂无可行解」的消极结论：差异确实在 TLS 行为层，但用真 Bun 发送可以绕开。
- 端点与 lane 决策见 [/zen 关闭，免费档统一到 /inference](./2026-10-26-opencode-free-zen-closed-inference-only.md)。
- 头集合与 body 整形见 [OpenCode 免费档头集合对齐](./2026-10-20-opencode-free-lane-header-parity.md) 与 [v2 免费档整形](../feature/2026-10-21-opencode-v2-free-tier-shaping.md)。
