# Agent Note: 自定义头、头模板、JWT 与代理列静态加密，列表掩码渠道代理

Status: implemented

## 问题

[凭据字段静态加密](2026-09-21-credential-at-rest-encryption-and-export-redaction.md) 盖住了渠道 Key、多 Key、渠道代理和 API Key，但库里还有能当凭据用的明文：`channels.custom_header[].header_value`、`settings.header_templates`、`settings.auth_jwt_secret`、`proxy_url`、`proxy_pool`。后三项只是从备份里整行拿掉，文件被拖走时列值仍可读。`channel_proxy` 已经落库加密，列表接口却把带 userinfo 的代理 URL 放进 JSON。数据库导入会把备份里的精确 `****` 再加密写回，掩码变成假凭据。渠道代理如果套用 BaseURL 的出口拒绝，指向本机或内网代理的合法部署会被当成 SSRF 挡掉。

## 决定

- 仍用 `internal/seal` 的 `nv1:` AES-GCM。`sealChannelForDB` / `openChannelForCache` 以及内置渠道的 `sealBuiltinChannelForDB` 加密、解密每个非空 `header_value`。`settingAtRest` 覆盖 `header_templates`、`auth_jwt_secret`、`proxy_url`、`proxy_pool`：写入前 `Seal` 整段设置值，读入缓存前 `Open`。空串不加密。没有 `nv1:` 前缀的存量行继续按明文读，下一次写入才变密文。
- 数据库备份继续把渠道 Key、多 Key、`channel_proxy`、API Key 打成精确 `****`。自定义头和头模板保留头名与模板结构，非空头值同样打成 `****`。`auth_jwt_secret`、`proxy_url`、`proxy_pool` 仍整行不出现在备份里。
- 导入预检拒绝渠道 Key、多 Key、`channel_proxy` 上的精确 `****`，事务在加密落库之前失败。文本渠道导入仍拒绝任何含 `****` 的密钥行。列表回传的代理掩码在更新时视为「保留已存代理」，不把哨兵写进库。
- 渠道列表、创建和更新响应里的 `channel_proxy` 非空时只给 `****`，不带 userinfo。明文走 `POST /api/v1/channel/proxy/:id`，与密钥揭示一样仅管理员会话、不缓存、打审计日志。（后续决定反转：管理台直接显示完整代理，移除列表掩码与揭示接口，见 [管理台直接显示渠道代理明文](../simplification/2026-09-24-show-channel-proxy-plaintext-in-admin-ui.md)。）
- `channel_proxy` 不走 BaseURL 的出口校验。`127.0.0.1` 和私网地址作为代理是合法的。BaseURL 仍然拒绝私网、环回和保留地址。部署说明写在 `docs/SECURE_DEPLOYMENT.md`。

## 备选方案

- **把头模板和代理池拆成逐字段密文**：结构在库里仍可读，但和「整列一个设置值」的现有读写不一致，导入导出还要单独版本化 JSON。整段 `Seal`/`Open` 足够，头模板只在备份导出时解开再打码。
- **列表沿用日志里的 `MaskProxySecret`（留下用户名和主机）**：主机不再是凭据，但 userinfo 的用户名仍然是明文，和「列表不回代理明文」不符。列表用与密钥相同的精确 `****`，需要地址时走揭示接口。
- **让 `channel_proxy` 复用 BaseURL 的私网拒绝**：能少写一段文档，但会把指向本机或内网正向代理的正常部署判成 SSRF。代理地址和上游 BaseURL 不是同一类目标。

## 后果

- **收益**：上述列在静态库里不再是明文；列表 JSON 不再带出代理口令；导入不会把 `****` 密封成渠道密钥或代理。数据库备份里的渠道 Key 与 API Key 后来改为明文，见 [数据库备份导出明文 Key](2026-09-22-backup-export-plaintext-keys.md)。代理、自定义头和头模板在备份里仍打码。
- **代价与已知上限**：和 [凭据字段静态加密](2026-09-21-credential-at-rest-encryption-and-export-redaction.md) 一样，存量明文要等下一次写入才加密。管理台若把列表里的 `****` 原样提交，更新会保留旧代理而不是报错；真正改代理必须提交新地址，或先调用揭示接口。导入脱敏备份会把头模板里的 `****` 当作新的模板值 upsert（结构还在，秘密值没有），渠道 Key 和代理则整单拒绝。未 `Configure` 的进程仍会写出重启后解不开的密文。

## 验证

- `go test ./internal/op/ ./internal/builtin/ ./internal/server/handlers/ ./internal/seal/` 覆盖落库密文、存量明文、备份打码、导入拒绝 `****`、内网代理可写、列表掩码。
