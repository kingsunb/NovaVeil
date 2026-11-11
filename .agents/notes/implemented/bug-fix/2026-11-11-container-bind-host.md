# Agent Note: 容器镜像默认监听所有接口，smoke 失败可见

Status: implemented

## 问题

2026-11-11 的 `docker-publish` 在 `Scan and smoke test exact platform images` 阶段连续两次挂起 20 分钟以上（Trivy 0.74.0 与 0.76.2），`test` 与 `Verify Agent Notes` 都已通过；取消后 runner 报告遗留 `smoke-test-image.sh` 与 `wget` 进程。

根因是镜像只继承二进制的默认 `server.host=127.0.0.1`（`internal/conf/config.go:146`）。Compose 模板显式设置 `NOVAVEIL_SERVER_HOST=0.0.0.0`（`docker-compose.yml:68`、`docker-compose.local.yml:48`），所以常规部署不受影响；但 [verify-image-archives.sh](../../../../scripts/verify-image-archives.sh) 直接 `docker run` 镜像并发布 `-p 127.0.0.1::8080`，容器内应用只监听 loopback，宿主端口无法到达应用，[smoke-test-image.sh](../../../../scripts/smoke-test-image.sh) 的就绪探测 `wget -T 3` 在 Docker 端口代理连接上长时间阻塞，形成 CI 挂死。脚本其余循环都有 60/45 次上限，只有该探测与 `docker run`、`docker exec` 没有总时限。这是 [CI 与文档门禁](../process/2026-11-10-ci-docs-gates.md) 的验证脚本首次在真实镜像上暴露的问题。

## 决定

- `scripts/dockerfile/Dockerfile` 的 ENV 增加 `NOVAVEIL_SERVER_HOST=0.0.0.0`，镜像单独运行时也能被宿主端口访问；`docker run -e` 与 Compose 仍可覆盖。
- `scripts/smoke-test-image.sh` 的就绪探测优先 `curl -fs --max-time 3`（总时长上限，代理无响应也不会无限等待），无 curl 的主机回退 `wget -T 3`；补充启动、就绪、加固检查、健康检查、读取初始密码与登录阶段日志；带 Cookie 的管理写请求同时发送同源 `Origin`，覆盖 OriginProtection 的 CSRF 校验。
- `scripts/verify-image-archives.sh` 为每架构扫描、加载与 smoke 增加阶段日志，并用 `timeout 600` 包住 native smoke（无 `timeout` 的主机直接执行），把潜在挂起变成可诊断的失败。

## 备选方案

- **只在 entrypoint 里 export 默认值**：能工作，但镜像 `inspect` 看不到默认监听地址，排障更难。
- **只在 smoke 的 `docker run` 加 `-e NOVAVEIL_SERVER_HOST=0.0.0.0`**：CI 会绿，但真实用户直接运行镜像仍不可达，等于用测试环境配置掩盖镜像缺陷。
- **把二进制默认值改成 0.0.0.0**：破坏本地默认只监听 loopback 的安全语义。
- **移除 smoke**：丢失真实登录、数据库与转发链路验证。

## 后果

镜像单独运行即可通过宿主端口访问；Compose 显式设置的环境变量不变，仍可覆盖。smoke 失败最长 10 分钟（`timeout 600`）即可暴露，日志能定位到扫描、加载、就绪、加固、健康检查或 API 阶段。容器内监听 `0.0.0.0` 的暴露面仍由宿主端口绑定、反向代理与防火墙控制，[部署安全](../../../../docs/SECURE_DEPLOYMENT.md) 的既有要求不变。

## 验证

本仓库禁止 AI 执行构建与测试，以下命令由用户执行：

```sh
bash -n scripts/smoke-test-image.sh scripts/verify-image-archives.sh
pnpm verify-notes
git push origin main
```

推送后由 `docker-publish` CI 实跑；预期 `Scan and smoke test exact platform images` 在扫描后 1-2 分钟内输出 `smoke: waiting for HTTP readiness`、`smoke: HTTP ready` 与 `smoke: real login passed` 等阶段日志并完成。
