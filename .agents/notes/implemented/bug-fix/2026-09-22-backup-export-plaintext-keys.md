# Agent Note: 数据库备份导出明文渠道 Key 与 API Key

Status: implemented

## 问题

库里的渠道 Key 和 API Key 以 `nv1:` 落盘。整库 JSON 备份再把它们打成 `****` 之后，文件既不能在另一台机器上还原调用，也比数据库本身更没用：拿到数据库的人解不开密文，拿到备份的人只看到哨兵。设置页已经按「备份含明文 Key」警告管理员。

## 决定

`DBExportAll` 在写出备份前用 `seal.Open` 解开渠道旧式 Key、多 Key 和 API Key。没有 `nv1:` 前缀的存量明文保持原样。库内列继续加密，列表接口继续只返回掩码。

代理地址、自定义头值和请求头模板值在备份里仍是精确 `****`。`auth_jwt_secret`、`proxy_url`、`proxy_pool` 仍不出现。用户表不导出。导入仍拒绝渠道 Key、API Key 和 `channel_proxy` 上的精确 `****`。

字段加密和列表掩码见 [自定义头与代理列加密](2026-09-22-seal-header-proxy-and-mask-list.md)。

## 备选方案

- **备份里保留 nv1: 密文，另附 encryption key**：还原必须同时带走密钥文件，和「一份 JSON 就能迁走调用凭据」不是同一件事。Key 在备份中用明文。
- **代理和自定义头也改成明文**：还原更完整，但用户这次只要求 Key。这两类仍打码。

## 后果

- **收益**：导入备份后渠道 Key 和 API Key 可以直接用来调用，不必对照数据库再抄一遍。
- **代价与已知上限**：备份文件等于上游凭据，落在管理员磁盘上。代理、自定义头和头模板仍要在导入后重填。加密密钥文件丢了不影响这份 JSON 里的 Key，但也不再能从旧库密文里读出它们。

## 验证

`internal/op/backup_export_test.go` 断言导出的渠道 Key 与 API Key 等于写入前的明文，且用户密码不在文件里。`internal/op/seal_at_rest_test.go` 断言密封后的渠道 Key 在备份里是明文，代理、自定义头、头模板和 `nv1:` 前缀不出现。
