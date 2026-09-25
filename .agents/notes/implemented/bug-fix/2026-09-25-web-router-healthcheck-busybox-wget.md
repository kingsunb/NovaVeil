# Agent Note: web-router 健康检查改用 BusyBox wget

Status: implemented

## 问题

`docker-compose.yml` 的 web-router 服务 healthcheck 用 `curl -fsS http://127.0.0.1/healthz`，注释声称"官方 nginx alpine 镜像自带 curl"。事实相反：官方 nginx:alpine 只带 BusyBox 的 `wget`（佐证：`web-next/Dockerfile` 的 runtime 阶段特意安装了 wget 才可用其 healthcheck）。容器内找不到 curl，healthcheck 恒失败，web-router 永远不会被判定为 healthy——依赖健康状态的编排（`depends_on: condition`、Swarm/Compose 的 unhealthy 处置）全部失真。同一文件的 novaveil 服务（同样只读 rootfs）早已用 `wget -q -O /dev/null` 写法验证过这条路径可行。

## 决定

healthcheck 改为 `["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1/healthz"]`：`-O /dev/null` 丢弃响应体，与 novaveil 服务同一写法；注释改为如实记录"官方 nginx alpine 镜像不含 curl，只有 BusyBox wget"。同批修正 web-router 段落另一处与实现矛盾的过期注释：镜像行已按 digest 固定（`nginx:1.27.5-alpine@sha256:65645c7b…`），"待 registry-lookup 后应改为 @sha256"的注释改为"升级时同步更新两者并核对 digest"。随本批落地的文档对齐（README/README_zh 发布流程条目改为现状两条链路的如实描述；`docs/SECURE_DEPLOYMENT.md` 补足反代后未配 trusted_proxies 时登录限速与管理令牌桶塌成单一桶、单客户端可锁死全站的后果）属纯事实纠正，并入本批不单独立项。

## 备选方案

### 为什么不用 wget --spider？

BusyBox wget 的 `--spider` 支持不完整（部分构建忽略该 flag 或行为不一致），`-q -O /dev/null` 是同文件已验证、跨 BusyBox 构建稳定的最小写法。

### 为什么不给 web-router 镜像安装 curl 保持原探测命令？

多装一个包扩大 web-router 的攻击面与镜像体积，违背该服务"最小 nginx"的定位；wget 是镜像既有工具，探测语义不变。

## 后果

收益：web-router 恢复可判定健康，容器级编排信号不再恒失败；两条与实现矛盾的过期注释清零，digest 升级路径有明确指引；发布链路文档不再描述未接线的 dev-publish 流程。代价：healthcheck 的退出码语义从 curl 换成 BusyBox wget，极端网络错误的细分错误码有差异（对 /healthz 的 200 判定无影响）；README 与部署文档的发布链描述从此须随 CI 演进保持同步。
