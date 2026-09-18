<div align="center">

<img src="web-next/public/logo.svg" alt="NovaVeil Logo" width="120" height="120">

### NovaVeil

**为个人打造的简单、美观、优雅的 LLM API 聚合服务**

简体中文 | [English](README.md)

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react)](https://react.dev/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

</div>


## ✨ 特性

- 🔀 **多渠道聚合** - 支持接入多个 LLM 供应商渠道，统一管理
- 🔄 **协议互转** - 支持 OpenAI Chat / OpenAI Responses / Anthropic 三种 API 格式互相转换
- 🛡️ **三态熔断与自动故障转移** - CLOSED / OPEN / HALF-OPEN 熔断状态机，非阻塞半开探测，上游故障自动切换可用渠道
- 🔑 **单渠道多 Key** - 多凭据轮询、401/403 自动冷却换 Key、按账号注入独立代理
- 🚦 **渠道级限速** - 单 Key RPM 滑动窗口限速与渠道最大并发控制
- 🚧 **上游错误拦截** - 拦截所有上游错误并补发协议终止帧，避免中断 Agent 任务
- ⛑️ **紧急兜底** - 全部成员不可用时节流放行最后防线，保障任务不中断
- 🔍 **请求全链路实时可视化** - 客户端发起请求后，即可在前端实时查看完整请求链路与每次尝试轨迹
- 🎨 **优雅界面** - 简洁美观的 Web 管理面板
- 📦 **轻量单文件部署** - 单个二进制文件即可运行，无需额外运行时依赖
- 🗄️ **多数据库支持** - 支持 SQLite、MySQL、PostgreSQL


## 🚀 快速开始

### 🐳 Docker 运行

使用加固后的 Compose 模板，并固定到不可变发布镜像：

```bash
wget https://raw.githubusercontent.com/kingsunb/NovaVeil/main/docker-compose.yml
sudo install -d -o 10001 -g 10001 -m 0700 /var/lib/novaveil
docker volume create --driver local \
  --opt type=none --opt o=bind --opt device=/var/lib/novaveil novaveil-data
export NOVAVEIL_IMAGE='ghcr.io/kingsunb/novaveil-api@sha256:<manifest-digest>'
docker compose pull
docker compose up -d
```

镜像固定使用 UID/GID `10001:10001`，rootfs 只读，仅 `/app/data` 与受限的
`/tmp` tmpfs 可写。Compose 默认仅绑定宿主 `127.0.0.1:8888`，供本机 HTTPS
反向代理访问；若需监听其它地址，请显式设置 `NOVAVEIL_BIND_ADDRESS` 并配置 HTTPS 与防火墙。

> **Docker Hub 迁移说明：** Docker Hub 镜像同步已经停止，
> `ghcr.io/kingsunb/novaveil-api` 是唯一受支持的容器发布源。既有 Docker Hub
> 部署只需把镜像引用改为 GHCR 的版本 tag/digest，保留原 `/app/data` 数据卷，
> 再执行 `docker compose pull && docker compose up -d`。


### 📦 从 Release 下载

> ⚠️ 尚无正式 GitHub Release（仓库当前无 tag/Release）。单二进制请走下方「源码运行」，或由运维手动触发 `release` / `build` workflow 发布 `ghcr.io/kingsunb/novaveil-api`。

### 🛠️ 源码运行

**环境要求：**
- `go.mod` 声明的 Go 版本
- Node.js 22.19+
- pnpm 11.21+

默认构建不需要 Android NDK。`--targets all` 构建非 Android 发布矩阵；正式 Release
还会使用 NDK r28c 加上 `--include-android`，发布 `amd64`、`arm64`、`arm`、`386`
四个 Android 归档。

```bash
# 克隆项目
git clone https://github.com/kingsunb/NovaVeil.git
cd NovaVeil
# 日常：拉上游 → 本地生产构建 → 启动 8080
git pull --ff-only
bash scripts/build.sh --local
bash scripts/run-local.sh
```

完整发布归档（许可证 + zip + 多架构）仍用 `bash scripts/build.sh`。

> 💡 **提示**：前端构建产物会被嵌入到 Go 二进制文件中，所以必须先构建前端再启动后端。

> 📖 独立环境构建与运行（日常拉上游、`--local` 构建、8080 冒烟、工具链、离线、发布 `build.sh`）见 [独立环境部署](docs/STANDALONE_DEPLOYMENT.md)。

**开发模式**

```bash
cd web-next && pnpm install && pnpm run dev
## 新建终端,启动后端服务
go run main.go start
## 访问前端地址
http://localhost:5174
```

### 🔐 默认账户

首次启动会自动创建管理员账户 `admin`，随机密码写入 `data/initial-admin-password`，权限为仅属主可读写（`0600`），不会写入常规日志。首次登录后必须修改密码才能进行其他操作；改密成功后该引导文件会自动删除。

> ⚠️ **安全提示**：请仅从受保护的数据卷读取引导密码，并立即修改。


### 📝 配置文件

配置文件默认位于 `data/config.json`，首次启动时自动生成。

**完整配置示例：**

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 8080
  },
  "database": {
    "type": "sqlite",
    "path": "data/data.db"
  },
  "log": {
    "level": "info"
  }
}
```

**配置项说明：**

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `server.host` | 监听地址 | `127.0.0.1` |
| `server.port` | 服务端口 | `8080` |
| `database.type` | 数据库类型 | `sqlite` |
| `database.path` | 数据库连接地址 | `data/data.db` |
| `log.level` | 日志级别 | `info` |

> **说明**：二进制默认监听 `127.0.0.1`；加固 Compose 模板在容器内显式设 `NOVAVEIL_SERVER_HOST=0.0.0.0`（宿主绑定 `127.0.0.1:8888`），`scripts/run-local.sh` 亦默认 `0.0.0.0` 以便局域网访问。需要时显式设置 `server.host`。

**数据库配置：**

支持三种数据库：

| 类型 | `database.type` | `database.path` 格式 |
|------|-----------------|---------------------|
| SQLite | `sqlite` | `data/data.db` |
| MySQL | `mysql` | `user:password@tcp(host:port)/dbname` |
| PostgreSQL | `postgres` | `postgresql://user:password@host:port/dbname?sslmode=disable` |

**MySQL 配置示例：**

```json
{
  "database": {
    "type": "mysql",
    "path": "root:password@tcp(127.0.0.1:3306)/novaveil"
  }
}
```

**PostgreSQL 配置示例：**

```json
{
  "database": {
    "type": "postgres",
    "path": "postgresql://user:password@localhost:5432/novaveil?sslmode=disable"
  }
}
```

> 💡 **提示**：MySQL 和 PostgreSQL 需要先手动创建数据库，程序会自动创建表结构。

**环境变量：**

所有配置项均可通过环境变量覆盖，格式为 `NOVAVEIL_` + 配置路径（用 `_` 连接）：

| 环境变量 | 对应配置项 |
|----------|-----------|
| `NOVAVEIL_SERVER_PORT` | `server.port` |
| `NOVAVEIL_SERVER_HOST` | `server.host` |
| `NOVAVEIL_DATABASE_TYPE` | `database.type` |
| `NOVAVEIL_DATABASE_PATH` | `database.path` |
| `NOVAVEIL_LOG_LEVEL` | `log.level` |
| `NOVAVEIL_GITHUB_PAT` | 用于获取最新版本时的速率限制(可选) |


## 📖 功能说明

### 📡 渠道管理

渠道是连接 LLM 供应商的基础配置单元。

**Base URL 说明：**

程序会根据渠道类型自动补全 API 版本和端点路径，您只需填写服务根地址即可：

| 渠道类型 | 自动补全路径 | 填写 URL | 完整请求地址示例 |
|----------|-------------|----------|-----------------|
| OpenAI Chat | `/v1/chat/completions` | `https://api.openai.com` | `https://api.openai.com/v1/chat/completions` |
| OpenAI Responses | `/v1/responses` | `https://api.openai.com` | `https://api.openai.com/v1/responses` |
| Anthropic | `/v1/messages` | `https://api.anthropic.com` | `https://api.anthropic.com/v1/messages` |
| Gemini | `/v1beta/models/:model:generateContent` | `https://generativelanguage.googleapis.com` | `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent` |

> 💡 **提示**：Base URL 无需包含 `/v1`、`/v1beta` 或具体的 API 端点路径，程序会自动处理。

---

### 📁 分组管理

分组用于将多个渠道聚合为一个统一的对外模型名称。

**核心概念：**

- **分组名称** 即程序对外暴露的模型名称
- 调用 API 时，将请求中的 `model` 参数设置为分组名称即可

> 💡 **示例**：创建分组名称为 `gpt-4o`，将多个供应商的 GPT-4o 渠道加入该分组，即可通过统一的 `model: gpt-4o` 访问所有渠道。

**分组 Relay 配置：**

- 会话粘合：同一 `X-Session-Id` 的请求固定路由到同一成员，上下文缓存命中率更高
- 冷却退避：成员连续失败进入冷却，指数退避避免持续击打故障渠道
- 后台探测：定时探测冷却中的成员，恢复后自动回流流量
- 分组引用：分组成员可引用其他分组，实现跨分组故障转移链
- 紧急兜底：全部成员不可用时对指定兜底成员节流放行

---

### ⚙️ 设置

系统全局配置项。

---

## 🔌 客户端接入

### OpenAI SDK

```python
from openai import OpenAI
import os

client = OpenAI(   
    base_url="http://127.0.0.1:8080/v1",   
    api_key="sk-NovaVeil-P48ROljwJmWBYVARjwQM8Nkiezlg7WOrXXOWDYY8TI5p9Mzg", 
)
completion = client.chat.completions.create(
    model="gpt-4o",  # 填写正确的分组名称
    messages = [
        {"role": "user", "content": "Hello"},
    ],
)
print(completion.choices[0].message.content)
```

### Claude Code

编辑 `~/.claude/settings.json`

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080",
    "ANTHROPIC_AUTH_TOKEN": "sk-NovaVeil-...",
    "API_TIMEOUT_MS": "3000000",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "ANTHROPIC_MODEL": "nova-sonnet-4-5",
    "ANTHROPIC_SMALL_FAST_MODEL": "nova-haiku-4-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "nova-sonnet-4-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "nova-sonnet-4-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "nova-haiku-4-5"
  }
}
```

### Codex

编辑 `~/.codex/config.toml`

```toml
model = "gpt-5.6-sol"
model_reasoning_effort = "xhigh"
model_provider = "novaveil"
preferred_auth_method = "apikey"

[model_providers.novaveil]
base_url = "http://127.0.0.1:8080/v1"
name = "novaveil"
supports_websockets = false
requires_openai_auth = true
wire_api = "responses"
experimental_bearer_token = "sk-NovaVeil-..."
```
编辑 `~/.codex/auth.json`

```json
{
  "OPENAI_API_KEY": ""
}
```


---

## 🤝 致谢

- 基于 [bestruirui/octopus](https://github.com/bestruirui/octopus) 二次开发，感谢原作者的优秀工作
- 🙏 [looplj/axonhub](https://github.com/looplj/axonhub) - 本项目的 LLM API 适配模块直接源自该仓库的实现

## 🔒 安全部署

- **首次启动**会把随机管理员密码写入仅属主可读写的 `data/initial-admin-password` 引导文件，登录后请立即改密
- 容器内默认监听 `0.0.0.0`，docker-compose 在宿主默认绑定 `127.0.0.1:8888`；如需公网访问请置于反向代理之后并启用 HTTPS
- 反代终止 TLS 时保留原始 `Host` 请求头，并设置环境/配置 `security.cookie_secure: true`
- 登录接口内置限速：15 分钟内失败 5 次将临时拒绝；失败计数持久化在数据库中，多副本部署共享同一份计数
- 备份导出（`/api/v1/setting/export`）包含渠道 Key 与 API Key **明文**（用于完整还原，导出文件头部 `note` 字段亦有提示）；用户密码不在导出范围内。请将备份文件视同生产凭据妥善保管
- 默认直连部署不信任任何代理头（`X-Forwarded-For` 不可伪造绕过限速）；若置于反向代理之后，需自行配置 gin 可信代理才能按真实客户端 IP 限速，参见 [gin SetTrustedProxies 说明](https://gin-gonic.com/docs/examples/trusted-proxies/)
- Docker 升级必须通过 `NOVAVEIL_IMAGE` 使用经过审查的版本或 digest；只读 rootfs 会有意阻止容器内替换二进制
- 镜像固定、HTTPS、目录权限、资源限制与更新校验见 [安全部署](docs/SECURE_DEPLOYMENT.md)
- `Build, test, and audit` 会对每个平台 Docker archive 单独做漏洞扫描，并通过 `dev-publish` environment 控制多架构 manifest 的发布。发布 job **绝不重新 build** 镜像，只 `docker load` 刚才已扫描的 archive，与 `IMAGE_IDS.tsv` 逐镜像核对 ID，然后以 `image@sha256:...` 不可变引用推送。任何 HIGH/CRITICAL 漏洞都会让该次构建失败。
- 首次发布需要一次运营操作：在 **仓库 Settings → Actions → General → Workflow permissions** 中勾选 `Read and write permissions`；在 kingsunb/novaveil-api 的 **Package settings** 中允许 workflow 写该包。否则 GitHub 会在发布步骤返回 `denied: permission_denied: write_package`，即使构建已通过。
- 应用导出、数据库完整备份与恢复演练见 [备份与恢复](docs/BACKUP_RESTORE.md)
- 无 Docker 的源码构建与运行见 [独立环境部署](docs/STANDALONE_DEPLOYMENT.md)
