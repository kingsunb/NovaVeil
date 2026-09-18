# Agent Note: build.sh 前置校验 Go 工具链版本

Status: implemented

## 问题

`scripts/build.sh` 之前直接调用 `go build` / `go list -deps`，不校验工具链。在装了旧 Go（如 1.22）的开发机上，由于 `GOROOT` / `GOTOOLCHAIN` 配置不一，构建要到末端才报 `crypto/sha3`、`iter` 等「is not in std」的误导性错误，把「工具链太旧」伪装成「依赖或 stdlib 缺失」。脚本缺少一个可靠的前置失败点，本地与 CI 的失败形态不一致，排查成本高。

## 决定

在 `scripts/build.sh` 参数与环境校验之后、构建之前，新增 `check_go_toolchain()`：读取 `go env GOVERSION`（形如 `go1.26.7`），提取主、次版本与 go.mod 要求的 `1.26` 比较；不足则立即 `exit 2`，打印当前 `go version` 与 `GOROOT`，并提示安装 Go 1.26+ 及「`GOROOT=/path/to/go PATH=/path/to/go/bin:$PATH bash scripts/build.sh`」的重跑方式。非稳定版本号（devel 等）不阻塞，交给 `go build` 自行判断。`usage()` 的 Environment 段同步补 GOROOT/PATH 说明。

## 备选方案

- 在脚本里硬编码某台开发机的 GOROOT 路径：不可移植，CI 与别的开发机会被误伤，未选。
- 只依赖 Go 自身的 `GOTOOLCHAIN=auto` 自动下载：确能兜底，但下载受网络/代理限制，且报错时机晚、信息不明，未采用（校验先行，自动下载作为兜底仍保留）。
- 只在 CI 里检查：CI 已固定 setup-go 1.26.7，真正的缺口恰在本地开发机，未选。

## 后果

- 收益：本地用旧工具链时 build.sh 第一时间失败，报错直接指向「装 Go 1.26+」，不再看到 stdlib 缺失的歧义错误；失败形态本地与 CI 一致。
- 边界：版本比较只覆盖主、次版本（`1.26`），忽略 patch；若未来 go.mod 提高主/次版本要求，需同步这里的字面量 `1.26`。非稳定/devel 版本放行，仍依赖 `go build` 兜底。

## 验证

- `GOTOOLCHAIN=local bash scripts/build.sh --local --targets linux/arm64`（强制本机 1.22）→ `exit 2`，打印「go.mod requires Go 1.26+; found "go version go1.22.0 linux/arm64"」。
- 默认环境（GOTOOLCHAIN=auto 切到 1.26.7）`bash scripts/build.sh --local --targets linux/arm64` → `exit 0`，产出 `build/bin/novaveil-linux-arm64`。
- `bash scripts/run-local.sh` → `GET /` 返回 HTTP 200，冒烟通过。