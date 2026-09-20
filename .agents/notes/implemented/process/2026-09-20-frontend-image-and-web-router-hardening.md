# Agent Note: 前端镜像 tag 固定、pnpm 11.21 与 web-router 加固

Status: implemented

## 问题

2026-09-20 后续审计 DevOps 域指出：

- **DEV-03**：`web-next/Dockerfile` 的两个基础镜像 `node:22-alpine`、`nginx:1.27-alpine` 未 pin digest。
- **DEV-05 / OLD-19 / OLD-20**：`docker-compose.yml` 的 `web-router` 可选 profile 使用 mutable `nginx:1.27-alpine`，且缺少主服务的 `read_only`/`cap_drop`/`security_opt`/healthcheck。
- **DEV-09**：`web-next/Dockerfile` 写死并启用 pnpm@9，与 CI/docs 当前要求的 pnpm 11.21+ 不一致；`README.md` 与 `docs/SECURE_DEPLOYMENT.md` 仍引用已删除的 `release` workflow。

## 决定

- `web-next/Dockerfile` builder 改为 `node:22.19.0-alpine`，runtime 改为 `nginx:1.27.5-alpine`；在 FROM 旁边注明：离线无法查询 Docker Hub digest，待 registry-lookup 后应改为 `@sha256:...` 不可变引用。
- `web-next/Dockerfile` 的 pnpm 改为 `corepack prepare pnpm@11.21.0 --activate`，与 `web-next-ci.yaml`/`test.yaml` 的 `pnpm/action-setup` 版本一致；`web-next/package.json` 未声明 `packageManager`，因此 Dockerfile 与 CI 都以 11.21.0 为准。
- `docker-compose.yml` 的 `web-router` 镜像固定为 `nginx:1.27.5-alpine`（与 Dockerfile runtime 一致），并补齐：
  - `read_only: true` + `/tmp`、`/var/cache/nginx`、`/var/run` 三个 tmpfs；
  - `security_opt: no-new-privileges:true`；
  - `cap_drop: [ALL]` + `cap_add: [NET_BIND_SERVICE]`（容器内 nginx 监听 80，必须保留这一个最小端口能力）；
  - `healthcheck`：官方 nginx alpine 自带 curl，使用 `curl -fsS http://127.0.0.1/healthz`；
  - `pids_limit` 与 json-file 日志滚动，与主服务形态对齐。
- 文档只改 DEV-09 涉及的 release workflow 引用：`README.md:64` 改为手动触发 `build` 工作流做完整多架构审计、push `main` 由 `docker-publish` 自动发 `latest`/`:sha-*`；`docs/SECURE_DEPLOYMENT.md:40-43` 同步改为 `docker-publish` 与 `build` 两个现行工作流名。

## 备选方案

- 基础镜像直接 pin `@sha256:` digest：最符合审计理想，但当前环境无法访问 Docker Hub registry 获取 digest；仓库里也没有现成 node/nginx digest 可供复用，只能先用完整版本 tag 并留注释。
- `node:22.14.0-alpine` 等更早完整 tag：可固定完整版本，但与 CI 的 Node 22.19.0 不一致；未选，选 `22.19.0-alpine` 对齐 CI。
- 把 `web-router` 的 nginx 改为容器内监听 8080，从而可以完全不保留 `NET_BIND_SERVICE`：需要同步改 `web-next/router.nginx.conf` 与 compose 端口映射，改动面超出本次允许范围（且端口语义本就在容器内 80），未选。
- 只给 web-router 加 healthcheck 但不加 `cap_add`：`cap_drop: [ALL]` 下 nginx master 无法绑定 80，容器会启动失败，未选。

## 后果

- 收益：独立前端镜像与 web-router 的基座/工具链不再随 mutable alpine tag 漂移；web-router 与主服务、web-next 服务具备同一级别的只读根文件系统与 capability 收敛；pnpm 11.21 在 Docker 内与 CI 对齐。
- 代价与边界：完整版本 tag 仍可能被上游重新发布，属于离线限制下的过渡状态；web-router 保留了一个 `NET_BIND_SERVICE` capability，这是绑定 80 的必要成本；官方 nginx alpine 自带 curl 的 healthcheck 依赖上游继续保留 curl。
- 后续 registry 可达时，应优先把 `node:22.19.0-alpine` 与 `nginx:1.27.5-alpine` 换成 digest 引用。

## 验证

- `web-next/Dockerfile` 关键行复核：`node:22.19.0-alpine`、`pnpm@11.21.0`、`nginx:1.27.5-alpine`。
- 使用 Python 解析 `docker-compose.yml` 以及可解析的 workflow 文件，均通过。
- `grep` 确认 `README.md` 与 `docs/SECURE_DEPLOYMENT.md` 已无 `release` workflow 引用（本文件标题词除外）。