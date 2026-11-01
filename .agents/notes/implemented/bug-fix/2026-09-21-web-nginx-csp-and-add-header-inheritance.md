# Agent Note: web-next nginx 移除 script-src unsafe-inline 并重复安全头

Status: implemented

## 问题

审计 FE-03/F-M3 确认两层问题：`web-next/nginx.conf` 的 CSP 仍允许 `script-src 'unsafe-inline'`，脚本注入
只靠 `default-src 'self'` 兜底；`/healthz`、`/__flags/`、`/assets/` 与静态文件 location 由于各自带有
`add_header`，按 nginx 继承规则会丢弃 server 级全部安全头，等于这些路径无 CSP、无 X-Frame-Options。

## 决定

- server 级 CSP 改为 `script-src 'self'`，删除 `'unsafe-inline'`；`style-src 'self' 'unsafe-inline'`
  保留，仅用于 Vite/运行时注入的内联样式。
- `/healthz`、`/__flags/`、`/assets/`、静态文件 location 内显式重复五条安全头
  （X-Content-Type-Options、X-Frame-Options、Referrer-Policy、Permissions-Policy、Content-Security-Policy），
  避免 `add_header` 继承规则导致安全头丢失。nginx 无 header 变量/片段导入，选择内联重复。
- 2026-11-01 起取值与 Go 侧对齐：`X-Frame-Options` 为 `DENY`，CSP 追加
  `frame-ancestors 'none'`，每个区块再加 `Strict-Transport-Security: max-age=31536000`；
  `web-next/nginx.conf` 与 `web-next/router.nginx.conf` 各 5 处同步。

## 备选方案

- **用 `include security-headers.conf` 去重**：更 DRY，但需要容器镜像额外 COPY 文件并保证路径，改动面更大。
- **只在有 `add_header` 的 location 去重 CSP**：省行数，但 X-Frame-Options/Permissions-Policy 等头同样会丢，
  没有完整修复。
- **把 CSP 收紧到 `style-src 'self'`**：需审计 Vite 运行时样式是否全部外链，当前风险大于收益。

## 后果

- **收益**：所有 public 路径都携带一致的严格安全头，内联脚本被 CSP 拒绝。
- **代价与已知上限**：六条头在 nginx.conf 内重复多份，后续改安全头需同步改 5 处（两个配置文件共 10 处）；如容器镜像允许引入
  片段文件可再收敛。`router.nginx.conf` 的 `/healthz`、`/legacy`、`/assets/`、`/__flags/` 同样重复这六条头，见 [web mutation 缓存与导航防护](./2026-09-22-web-mutation-cache-and-nav-guards.md)。HSTS 在明文监听上被浏览器忽略，只有外层终止 TLS 且透传响应头时才生效。

## 验证

- `web-next/nginx.conf`：server 级与四个 location 均包含 `script-src 'self'` 且无 `'unsafe-inline'`；
  `X-Frame-Options` 为 `DENY`，CSP 含 `frame-ancestors 'none'`，每处都有 HSTS。
- 本文件为 envsubst 模板，未在本机 nginx 运行；语法经人工核对。
