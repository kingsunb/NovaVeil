<div align="center">

<img src="web-next/public/logo.svg" alt="NovaVeil Logo" width="120" height="120">

### NovaVeil

**A Simple, Beautiful, and Elegant LLM API Aggregation Service for Individuals**

 English | [简体中文](README_zh.md)

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react)](https://react.dev/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

</div>


## ✨ Features

- 🔀 **Multi-Channel Aggregation** - Connect multiple LLM provider channels with unified management
- 🔄 **Protocol Conversion** - Seamless conversion between OpenAI Chat / OpenAI Responses / Anthropic API formats
- 🛡️ **Three-State Circuit Breaker & Automatic Failover** - CLOSED / OPEN / HALF-OPEN state machine with non-blocking half-open probing; automatically switches to an available channel when an upstream fails
- 🔑 **Multi-Key per Channel** - Key round-robin, automatic cooldown rotation on 401/403, per-account proxy injection
- 🚦 **Channel-Level Rate Limiting** - Per-key RPM sliding window and channel-wide max concurrency
- 🚧 **Upstream Error Shielding** - Intercept all upstream errors and synthesize protocol termination frames to keep agent tasks running
- ⛑️ **Emergency Fallback** - Throttled last-resort routing when all members are unavailable
- 🔍 **Real-Time End-to-End Request Visualization** - Watch the complete request path and every attempt trace in the frontend
- 🎨 **Elegant UI** - Clean and beautiful web management panel
- 📦 **Lightweight Single-Binary Deployment** - Run as a single binary with no external runtime dependencies
- 🗄️ **Multi-Database Support** - Support for SQLite, MySQL, PostgreSQL


## 🚀 Quick Start

### 🐳 Docker

Use the hardened Compose template and pin an immutable release image:

```bash
wget https://raw.githubusercontent.com/kingsunb/NovaVeil/main/docker-compose.yml
sudo install -d -o 10001 -g 10001 -m 0700 /var/lib/novaveil
docker volume create --driver local \
  --opt type=none --opt o=bind --opt device=/var/lib/novaveil novaveil-data
export NOVAVEIL_IMAGE='ghcr.io/kingsunb/novaveil@sha256:<manifest-digest>'
docker compose pull
docker compose up -d
```

The image runs as fixed UID/GID `10001:10001`, uses a read-only root filesystem,
and writes only `/app/data` plus a bounded `/tmp` tmpfs. Compose binds
`127.0.0.1:8888` by default for a local HTTPS reverse proxy. To listen elsewhere,
set `NOVAVEIL_BIND_ADDRESS` explicitly and protect the port with HTTPS and a firewall.

> **Docker Hub migration:** Docker Hub image synchronization has been discontinued.
> `ghcr.io/kingsunb/novaveil` is the only supported container release source.
> Existing Docker Hub deployments must change only their image reference to the GHCR
> version/digest while retaining the same `/app/data` volume, then run
> `docker compose pull && docker compose up -d`.


### 📦 Download from Release

> ⚠️ No GitHub Release has been published yet (the repository currently has no tags or releases). Build the single binary from source below, or have an operator manually trigger the `build` workflow to run the full multi-arch archive/image audit. Pushes to `main` auto-publish `ghcr.io/kingsunb/novaveil` via the `docker-publish` workflow.

### 🛠️ Build from Source

**Requirements:**
- The Go version declared in `go.mod`
- Node.js 22.19+
- pnpm 11.21+

```bash
# Clone the repository
git clone https://github.com/kingsunb/NovaVeil.git
cd NovaVeil
# Daily loop: pull upstream, local production build, start :8080
git pull --ff-only
bash scripts/build.sh --local
bash scripts/run-local.sh
```

Full release archives (licenses + zip + multi-arch) still use `bash scripts/build.sh`.

The default build does not require Android NDK. Use `--targets all` for the non-Android
release matrix. Official releases also run `--include-android` with NDK r28c and publish
Android archives for `amd64`, `arm64`, `arm`, and `386`. Release archives and images include real Go and frontend
production dependency license reports; CI rejects empty/header-only reports.

> 📖 Standalone build & run (daily upstream pull, `--local` build, 8080 smoke test,
> toolchain, offline, release `build.sh`): [Standalone deployment](docs/STANDALONE_DEPLOYMENT.md).

**Development Mode**

```bash
cd web-next && pnpm install && pnpm run dev
## Open a new terminal, start the backend service
go run main.go start
## Access the frontend at
http://localhost:5174
```

### 🔐 Default Credentials

On first launch an `admin` account is created automatically and its random password is written to `data/initial-admin-password` with owner-only (`0600`) permissions. It is never printed to normal logs. Read that file before logging in: the first successful login deletes it. You must still change the password before any other operation is allowed.

> ⚠️ **Security Notice**: Read the bootstrap file only from the protected data volume, then change the password immediately.

### 📝 Configuration File

The configuration file is located at `data/config.json` by default and is automatically generated on first startup.

**Complete Configuration Example:**

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

**Configuration Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `server.host` | Listen address | `127.0.0.1` |
| `server.port` | Server port | `8080` |
| `database.type` | Database type | `sqlite` |
| `database.path` | Database DSN | `data/data.db` |
| `log.level` | Log level | `info` |
| `security.cookie_secure` | Mark the auth cookie `Secure` | `false` |
| `server.trusted_proxies` | Reverse proxies allowed to supply `X-Forwarded-For` | empty (trust none) |

> **Note:** a direct binary listens on `127.0.0.1:8080`. That is not the container listen address. The hardened Compose template sets `NOVAVEIL_SERVER_HOST=0.0.0.0` inside the container and publishes it as `127.0.0.1:8888` on the host. `scripts/run-local.sh` also defaults to `0.0.0.0` for LAN access. Set `server.host` explicitly to change it.

> **Proxies and cookies:** an empty `server.trusted_proxies` ignores `X-Forwarded-For`, so login rate limits see the immediate peer. Behind a reverse proxy, set that list to the proxy CIDR or IP — do not patch application code. `security.cookie_secure` defaults to false. The cookie is also marked `Secure` when the request itself is TLS, or when `X-Forwarded-Proto` is `https`. That header only makes the cookie stricter; it does not make `X-Forwarded-For` trusted. A proxy that terminates TLS presents plain HTTP to NovaVeil, so set `security.cookie_secure` to true there; leave it false for direct HTTP or the browser will not send the cookie.

**Databases:**

| Type | `database.type` | `database.path` Format |
|------|-----------------|------------------------|
| SQLite | `sqlite` | `data/data.db` |
| MySQL | `mysql` | `user:password@tcp(host:port)/dbname` |
| PostgreSQL | `postgres` | `postgresql://user:password@host:port/dbname?sslmode=disable` |

**MySQL Example:**

```json
{
  "database": {
    "type": "mysql",
    "path": "root:password@tcp(127.0.0.1:3306)/novaveil"
  }
}
```

**PostgreSQL Example:**

```json
{
  "database": {
    "type": "postgres",
    "path": "postgresql://user:password@localhost:5432/novaveil?sslmode=disable"
  }
}
```

> 💡 **Tip**: MySQL and PostgreSQL databases must be created manually; tables are created automatically.

**Environment Variables:**

Every option can be overridden via environment variables using the `NOVAVEIL_` prefix (path joined by `_`):

| Environment Variable | Option |
|----------------------|--------|
| `NOVAVEIL_SERVER_PORT` | `server.port` |
| `NOVAVEIL_SERVER_HOST` | `server.host` |
| `NOVAVEIL_DATABASE_TYPE` | `database.type` |
| `NOVAVEIL_DATABASE_PATH` | `database.path` |
| `NOVAVEIL_LOG_LEVEL` | `log.level` |
| `NOVAVEIL_SECURITY_COOKIE_SECURE` | `security.cookie_secure` |
| `NOVAVEIL_SERVER_TRUSTED_PROXIES` | `server.trusted_proxies` (comma-separated; replaces the file list when non-empty) |
| `NOVAVEIL_GITHUB_PAT` | GitHub PAT for update checks rate limit (optional) |


## 📖 Guides

### 📡 Channels

A channel is the basic unit for connecting to an LLM provider.

**Base URL:**

The program appends the API version and endpoint path based on the channel type — just fill in the service root:

| Channel Type | Appended Path | Base URL | Full Endpoint Example |
|--------------|---------------|----------|----------------------|
| OpenAI Chat | `/v1/chat/completions` | `https://api.openai.com` | `https://api.openai.com/v1/chat/completions` |
| OpenAI Responses | `/v1/responses` | `https://api.openai.com` | `https://api.openai.com/v1/responses` |
| Anthropic | `/v1/messages` | `https://api.anthropic.com` | `https://api.anthropic.com/v1/messages` |
| Gemini | `/v1beta/models/:model:generateContent` | `https://generativelanguage.googleapis.com` | `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent` |

> 💡 **Tip**: No need to include `/v1`, `/v1beta`, or endpoint paths in the Base URL.

---

### 📁 Groups

A group aggregates multiple channels under one externally exposed model name.

**Key concept:**

- The **group name** is the model name exposed by the service
- Set the request's `model` parameter to the group name

> 💡 **Example**: Create a group named `gpt-4o`, add GPT-4o channels from multiple providers, and access them all via `model: gpt-4o`.

**Group Relay Config:**

- Session Sticky: requests with the same `X-Session-Id` stick to the same member for better prompt-cache hits
- Cooldown Backoff: members entering cooldown after consecutive failures back off exponentially
- Background Probe: cooled-down members are probed periodically and rejoin automatically once healthy
- Group Reference: a group member can reference another group, forming cross-group failover chains
- Emergency Fallback: when all members are unavailable, a designated fallback member is throttled through

---

### ⚙️ Settings

Global settings.

---

## 🔌 Client Integration

### OpenAI SDK

```python
from openai import OpenAI
import os

client = OpenAI(   
    base_url="http://127.0.0.1:8080/v1",   
    api_key="sk-NovaVeil-...", 
)
completion = client.chat.completions.create(
    model="gpt-4o",  # group name
    messages = [
        {"role": "user", "content": "Hello"},
    ],
)
print(completion.choices[0].message.content)
```

### Claude Code

Edit `~/.claude/settings.json`

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

Edit `~/.codex/config.toml`

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

Edit `~/.codex/auth.json`

```json
{
  "OPENAI_API_KEY": ""
}
```


---

## 🤝 Acknowledgements

- Built on top of [bestruirui/octopus](https://github.com/bestruirui/octopus) — thanks to the original author for the great work
- 🙏 [looplj/axonhub](https://github.com/looplj/axonhub) - The LLM API adaptation module in this project is directly derived from this repository

## 🔒 Secure Deployment

- On **first launch** the random admin password is written to the owner-only `data/initial-admin-password` bootstrap file; change it immediately
- The server listens on `0.0.0.0` inside the container; docker-compose binds it to `127.0.0.1:8888` on the host by default. Put the service behind a reverse proxy with HTTPS for public access
- When terminating TLS at the proxy, preserve the original `Host` header and set `security.cookie_secure` to true (`NOVAVEIL_SECURITY_COOKIE_SECURE=true`). Direct HTTP should leave it false
- Login has built-in rate limiting: 5 failures within 15 minutes triggers a temporary block; counters are persisted in the database and shared across replicas
- Backup export (`/api/v1/setting/export`) contains channel keys and API keys in **plaintext** so a restore can call upstreams again. The live database still stores those keys as `nv1:` ciphertext. Proxy URLs, custom header values, and header template values are replaced with `****`. User passwords are not exported. Treat the file as production credentials
- Direct deployments trust no proxy headers (`X-Forwarded-For` cannot be spoofed to bypass rate limiting). Behind a reverse proxy, set `server.trusted_proxies` (or `NOVAVEIL_SERVER_TRUSTED_PROXIES`, comma-separated) to the proxy CIDR or IP. Do not edit application code for this
- Docker upgrades must use a reviewed version/digest through `NOVAVEIL_IMAGE`; the read-only root filesystem intentionally prevents in-container binary replacement
- See [Secure deployment](docs/SECURE_DEPLOYMENT.md) for image pinning, HTTPS, permissions, resource limits, and update verification
- See [Backup and restore](docs/BACKUP_RESTORE.md) for application exports, full database backups, and recovery testing
- See [Standalone deployment](docs/STANDALONE_DEPLOYMENT.md) for building and running from source without Docker
- `Build, test, and audit` scans every per-platform Docker archive and gates the multi-arch manifest via the `dev-publish` environment. The publish job **never rebuilds** images: it `docker load`s the scanned archive, verifies the image ID against `IMAGE_IDS.tsv`, and pushes the exact bytes under `image@sha256:...` references. Any HIGH/CRITICAL Trivy finding fails the run. A one-time operator action is required before the first publish: set the repository workflow default to `Read and write permissions`, and allow workflows to write to the `ghcr.io/kingsunb/novaveil` package. Without this, GitHub returns `denied: permission_denied: write_package` and the publish step aborts after the build succeeds.
