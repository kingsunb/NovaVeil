# 独立环境部署

本文档说明在不依赖 Docker 的前提下，从源码构建并运行 NovaVeil。
日常用法是「拉 GitHub 上游 → 本地生产构建 → 8080 冒烟」，不是本机前后端热更新。
容器化部署见 [安全部署](SECURE_DEPLOYMENT.md)。

> **前置结论**：本仓库已在本地独立环境实测通过——前端构建、Go 二进制编译、
> 服务启动、前端页面与登录接口均正常。下文步骤均经实测验证。

## 一、环境要求

| 依赖 | 版本要求 | 说明 |
|------|---------|------|
| Go | `go.mod` 声明版本（当前 1.26.7） | 见下方「Go 工具链」说明 |
| Node.js | ≥ 22.19 | 前端构建 |
| pnpm | ≥ 11.21 | 前端依赖与构建 |
| Git | 任意 | `build.sh` 注入版本信息时需要 |

默认构建（服务器/桌面平台）不需要 Android NDK。仅 `--include-android` 才需要 NDK r28c。

## 二、Go 工具链（独立编译环境）

`go.mod` 声明的 Go 版本通常高于发行版自带 Go。本仓库不使用 `toolchain` 指令，
而是依赖 Go 的 **工具链自动切换** 机制：当 `GOTOOLCHAIN=auto`（默认值）且本地 Go
版本低于 `go.mod` 要求时，`go` 命令会自动下载并切换到所需版本的独立工具链。

### 2.1 查看当前状态

```bash
go env GOTOOLCHAIN GOVERSION    # 期望: auto / 本地 Go 版本
go version                      # 在项目根目录执行，应自动切换到 go.mod 声明版本
```

在项目根目录执行 `go version` 时，若输出与 `go.mod` 声明版本一致，说明工具链已就绪。

### 2.2 工具链存放位置

自动下载的工具链位于模块缓存中，形如：

```
$(go env GOMODCACHE)/golang.org/toolchain@v0.0.1-go<版本>.<GOOS>-<GOARCH>/bin/go
```

这是「独立编译环境」——它与系统 Go 隔离，仅在该项目目录下生效，不影响系统其他 Go 项目。

### 2.3 离线 / 受限网络环境

若运行环境无法访问 `go.dev` 下载工具链，可在联网机器上预取后随项目一同分发：

```bash
# 联网机器：在项目目录触发一次自动下载
cd NovaVeil && go version
# 把整个 GOMODCACHE 目录（含 toolchain）拷贝到目标机器，并设置
export GOMODCACHE=/path/to/copied/modcache
export GOTOOLCHAIN=auto
```

或显式指定本地已安装的 Go（版本需 ≥ go.mod 声明版本）：

```bash
export GOTOOLCHAIN=go1.26.7        # 指向已安装的具体版本
```

### 2.4 排查「版本不匹配」报错

若出现 `go: go.mod requires go >= ... but ... is in use` 之类报错，依次检查：

1. `GOTOOLCHAIN` 是否被设为 `off` 或某个不存在的版本——改回 `auto`。
2. 网络是否能访问 `go.dev` / 代理（`GOPROXY`）。
3. `GOMODCACHE` 目录是否可写（工具链需下载到此处）。

## 三、构建

### 3.1 日常：上游更新 → 构建 → 启动 8080

仓库根目录：

```bash
export GOPROXY=https://goproxy.cn,direct   # 访问 proxy.golang.org 超时时加上
git fetch origin
git merge --ff-only origin/main           # 有未提交改动先 git stash
bash scripts/build.sh --local             # 前端 + 当前架构二进制，跳过许可证/zip
bash scripts/run-local.sh                 # 停旧进程、启动 8080、GET / 必须 200
```

`--local` 仍会 `pnpm install --frozen-lockfile`、`pnpm run build`、`go build`，但跳过第三方许可证报告和 zip（这两步依赖干净的 pnpm store 索引和系统 `zip`，日常验证不需要）。产物是 `build/bin/novaveil-linux-amd64`，前端已嵌入。只改后端时加 `--skip-frontend`。

`run-local.sh` 保留 `data/`（配置、SQLite、初始密码），日志写 `logs/novaveil.log`。改端口：`NOVAVEIL_SERVER_PORT=9000 bash scripts/run-local.sh`。浏览器打开 `http://127.0.0.1:8080`。

完整发布构建（许可证 + zip + 多架构）仍用 `scripts/build.sh`（不加 `--local`）。

### 3.2 前端 → 后端（手动两步）

前端构建产物会被嵌入 Go 二进制，**必须先构建前端**。

```bash
cd NovaVeil

# 1) 前端：产物写入 static/out/，供 Go embed 抓取
cd web-next
pnpm install --frozen-lockfile     # 首次或依赖变更时；lockfile 不变可省略
pnpm run build                     # tsc -b && vite build
cd ..

# 2) 后端：嵌入 static/out 后编译单文件二进制
go build -o novaveil main.go       # 产出 ./novaveil（约 60MB，含前端）
```

> `web-next/vite.config.ts` 中 `build.outDir` 指向 `../static/out`，
> `static/static.go` 用 `//go:embed all:out` 嵌入该目录，二者约定一致。

### 3.3 一键发布构建（scripts/build.sh）

`build.sh` 封装了前端构建、多平台交叉编译、第三方许可证清单与发布归档：

```bash
# 默认：linux/amd64，含前端与许可证，产出 build/archives/*.zip + SHA256SUMS
scripts/build.sh

# 指定多平台
scripts/build.sh --targets linux/amd64,linux/arm64,darwin/arm64

# 全平台发布矩阵（不含 Android）
scripts/build.sh --targets all

# 仅编译当前平台二进制，跳过前端与归档（最快验证编译是否通过）
scripts/build.sh --targets linux/arm64 --skip-frontend --skip-licenses --no-archive
```

`build.sh` 通过 `-ldflags` 注入版本、提交哈希与构建时间；手动 `go build` 不带这些参数，
`Version` 会显示为 `dev`。需要准确版本信息时用 `build.sh` 或手动传入：

```bash
VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo dev)
COMMIT=$(git rev-parse --short HEAD)
go build -trimpath -o novaveil \
  -ldflags="-X 'github.com/kingsunb/NovaVeil/internal/conf.Version=${VERSION}' \
            -X 'github.com/kingsunb/NovaVeil/internal/conf.Commit=${COMMIT}' \
            -s -w" main.go
```

## 四、运行

### 4.1 直接启动

```bash
./novaveil start
# 或不编译二进制，直接源码运行（开发常用）
go run main.go start
```

二进制默认监听 `127.0.0.1:8080`；如需对外（容器/LAN）显式设 `host=0.0.0.0`（容器模板用
`NOVAVEIL_SERVER_HOST=0.0.0.0`，本地脚本 run-local.sh 默认 `0.0.0.0`）。SQLite 数据库落在
`data/data.db`，配置文件 `data/config.json` 首次启动自动生成。浏览器访问 `http://127.0.0.1:8080`。

### 4.2 首次登录

首次启动自动创建管理员 `admin`，随机密码写入 `data/initial-admin-password`（权限 `0600`）：

```bash
cat data/initial-admin-password     # username: admin / password: <随机>
```

登录后**必须立即修改密码**才能进行其他操作；改密成功后该引导文件自动删除。

### 4.3 开发模式（前后端分离热更新）

```bash
# 终端 1：前端开发服务器（Vite，端口 5174）
cd web-next && pnpm install && pnpm run dev

# 终端 2：后端
go run main.go start

# 访问 http://localhost:5174
```

## 五、配置

配置文件 `data/config.json`，所有项均可被 `NOVAVEIL_` 前缀环境变量覆盖。完整项见
[README](../README_zh.md#配置文件)。独立部署常用：

```json
{
  "server": { "host": "127.0.0.1", "port": 8080 },
  "database": { "type": "sqlite", "path": "data/data.db" },
  "log": { "level": "info" }
}
```

| 场景 | 做法 |
|------|------|
| 改端口 | `NOVAVEIL_SERVER_PORT=9000 ./novaveil start` |
| 用 MySQL | `"database": {"type":"mysql","path":"user:pwd@tcp(host:3306)/novaveil"}` |
| 用 PostgreSQL | `"database": {"type":"postgres","path":"postgresql://user:pwd@host:5432/novaveil?sslmode=disable"}` |
| 反代后启用安全 Cookie | `NOVAVEIL_SECURITY_COOKIE_SECURE=true`（直接 HTTP 保持 false） |
| 反代后按真实客户端 IP 限速 | `NOVAVEIL_SERVER_TRUSTED_PROXIES=127.0.0.1/32`（逗号分隔，非空时覆盖配置文件） |

> MySQL / PostgreSQL 需先手动建库，程序自动建表。SQLite 无需任何前置操作。

## 六、升级与回滚

独立环境升级即「重新构建 + 替换二进制 + 重启」。内置自更新会在下载 Release 归档后
校验 `SHA256SUMS` 再替换；若新版本反复启动失败，下次启动自动回滚到 `.old`。

```bash
# 手动升级（与 3.1 日常循环相同，data/ 保持不变）
cd NovaVeil
git fetch origin && git merge --ff-only origin/main
bash scripts/build.sh --local
bash scripts/run-local.sh
```

数据目录 `data/` 与二进制相互独立，升级只替换二进制，不触碰 `data/`。

## 七、实测核对清单

本次在本地独立环境（系统 Go 1.22.0 + 自动切换的 1.26.7 工具链、Node v22.23.2、
pnpm 11.23.0）实测通过的项目：

- [x] `pnpm install --frozen-lockfile` —— 依赖与 lockfile 一致
- [x] `pnpm run build` —— 前端产物写入 `static/out/`，含 `.gz` 预压缩
- [x] `go build -o novaveil main.go` —— 产出 60MB 单文件二进制
- [x] `./novaveil start` —— 监听 8080，SQLite 自动建库建表
- [x] `GET /` —— 返回前端控制台 HTML（HTTP 200）
- [x] `POST /api/v1/user/login` —— 初始密码登录成功（HTTP 200，`must_change_password: true`）

## 八、与容器部署的差异

| 维度 | 独立环境（本文档） | Docker（[安全部署](SECURE_DEPLOYMENT.md)） |
|------|-------------------|---------------------------------------------|
| 运行时依赖 | 需自备 Go + Node + pnpm | 仅需 Docker |
| 隔离 | 依赖系统用户与文件权限 | 固定 UID/GID 10001、只读 rootfs、cap_drop ALL |
| 升级 | 重新构建替换二进制 | 更换 `NOVAVEIL_IMAGE` 镜像引用 |
| HTTPS | 自行在反代终止 TLS | 同左，Compose 默认绑 127.0.0.1:8888 |
| 适用 | 开发、内网、无容器运行时 | 生产、多副本、需强隔离 |

独立部署默认不信任任何代理头（`server.trusted_proxies` 为空，`X-Forwarded-For` 不能伪造绕过登录限速）。置于反向代理之后时，把代理 CIDR 或 IP 写入 `server.trusted_proxies`，或设置 `NOVAVEIL_SERVER_TRUSTED_PROXIES`。不要为此修改应用代码。反代终止 TLS 时另设 `security.cookie_secure=true`；进程自己收到的 TLS 请求会自动给 Cookie 加 `Secure`。
