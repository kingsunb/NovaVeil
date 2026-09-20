# Agent Note: 本地构建默认当前架构，run-local 优先当前架构产物

Status: implemented

## 问题

`scripts/build.sh --local` 默认构建 `linux/amd64`；在 arm64 开发机上按 README/部署文档执行
`bash scripts/build.sh --local && bash scripts/run-local.sh` 时，`run-local.sh` 会先命中
amd64 产物并报 `cannot execute binary file`，本地冒烟失败。

## 决定

- `build.sh` 增加 `--local` 标志：未显式传 `--targets` 时，默认目标改为 `go env GOOS`/`go env GOARCH` 的当前架构；发布/CI 默认仍为 `linux/amd64`。
- `run-local.sh` 候选优先级调整：先尝试 `novaveil-${GOOS}-${GOARCH}` 的当前架构产物，再回退 linux/amd64、linux/arm64 与根目录无前缀二进制。

## 备选方案

- **文档要求使用者加 `--targets linux/arm64`**：不改代码但文档早已承诺“当前架构二进制”，不如脚本直接兑现。
- **run-local.sh 每次现编二进制**：冒烟路径自动变长，且不再复用 release 级 `build.sh` 产物；选择沿用构建/运行脚本成对使用。

## 后果

- **收益**：任一台 Linux 开发机按文档执行本地构建+冒烟即可工作；`--local` 语义与文档一致。
- **代价与已知上限**：Windows/macOS 上执行 `run-local.sh` 仍需 Linux 环境；发布默认目标不受影响。

## 验证

- `bash -n scripts/build.sh scripts/run-local.sh` 语法通过。
- arm64 开发机执行 `bash scripts/build.sh --local` 构建出 `build/bin/novaveil-linux-arm64`。
- 执行 `bash scripts/run-local.sh` 命中当前架构产物，`GET /` 返回 HTTP 200。
