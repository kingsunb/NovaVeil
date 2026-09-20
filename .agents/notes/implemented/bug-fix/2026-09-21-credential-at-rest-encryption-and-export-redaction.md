# Agent Note: 凭据字段静态加密、导出脱敏、API Key 长度与初始密码文件清理

Status: implemented

## 问题

2026-09-20 后续审计确认凭据生命周期有四处缺陷：

1. **SEC-03**：渠道导出接口全文返回全部明文上游密钥。
2. **SEC-04**：API Key / 渠道 Key / 渠道代理凭据在 SQLite 中明文落库，备份导出只滤 `auth_jwt_secret`。
3. **SEC-05**：自定义 API Key 没有最小长度/熵校验，弱 Key 可被直接采用。
4. **SEC-08**：首次初始化生成的管理员密码文件在首次登录/改密前长期留在磁盘。

## 决定

- **新增 `internal/seal` 字段级加密包**：AES-256-GCM 原语。密文带 `nv1:` 版本前缀，无前缀的存量行按明文兼容读取；新写入恒为密文。密钥来源：`security.encryption_key` 配置（任意字符串派生 32 字节）或数据目录下 `novaveil-encryption.key`（0600，不存在则生成）；未 Configure 的测试/初始化路径回退进程内随机密钥。
- **敏感字段写入前统一加密、读入缓存前统一解密**：`internal/op/channel.go`（ChannelCreate/ChannelUpdate/channelRefreshCache/导入）、`internal/op/apikey.go`（APIKeyCreate/APIKeyUpdate/apiKeyRefreshCache）、`internal/op/crypto.go` 负责渠道副本加密与解密。进程内缓存与 API 返回继续使用明文，转发路径不受影响。
- **SEC-03**：`exportChannel` 只返回 `****` + 末四位的密钥掩码，不再返回明文；导出文件用于跨实例核对数量与尾号。
- **SEC-04**：`DBExportAll` 导出前把 `channels.key`、`channels.channel_proxy`、`channels.keys[].key`、`api_keys.api_key` 全部替换为 `****`；settings 过滤扩展到 `auth_jwt_secret`、`proxy_url`、`proxy_pool`。导入路径统一先 `seal.Open` 解明文预检、再 `seal.Seal` 落库。
- **SEC-05**：自定义 API Key 在 `op.APIKeyCreate`/`op.APIKeyUpdate` 中要求至少 16 个字符（`utf8.RuneCountInString`），不足返回 `ErrAPIKeyValidation`。
- **SEC-08**：`UserConsumeInitialPasswordFile` 在首次成功登录后、`UserChangePassword` 在改密提交后都删除一次性初始密码文件并清空路径变量；文件删除失败返回独立 sentinel，不影响登录/改密结果。

## 备选方案

- **全库 SQLCipher/整库存加密**：安全性最高，但迁移和备份格式变更大，本轮按字段级最小闭合实现；重访信号为 SQLite 文件被整体拖库。
- **导出接口干脆删除渠道导出**：影响现有工具迁移流程；选择保留掩码格式以维持可核对性。
- **仅对 Key 做不可逆哈希后导出**：无法开销核对场景，选择掩码最末四位。

## 后果

- **收益**：库内静态凭据不再明文；备份文件再无可用凭据；弱 Key 被拒绝；初始密码文件不再长期留存。
- **代价与已知上限**：字段级加密只加密了列值，表结构/数据量仍可见；存量行按明文读，下一次写入才会加密（迁移按读时兼容）。`seal.Configure` 在启动早期调用，未 Configure 的进程写库会产生重启后无法解密的密文——单元测试不落库不受影响。

## 验证

- `internal/op/user_test.go`：初始密码文件首次登录/改密后删除，删除失败回调有覆盖。
- `internal/op/authsecret_test.go`、`internal/op/backup_export_test.go`、`internal/server/handlers/channel_test.go` 等覆盖导出掩码与导入解密。
- 全量 `go build ./...`、`go test ./...`、`go vet ./...` 通过。