# Agent Note: 初始管理员密码恢复与离线改密语义对齐

Status: implemented

## 问题

初始管理员只有一条一次性密码文件作为首次登录凭据，但旧实现有三个缺口：

1. `UserInit` 把任何 `First` 错误都当作“用户不存在”去创建新管理员；数据库读失败时会误建用户，且 `Create` 成功后才写密码文件，文件写入失败会留下 `must_change_password=true` 但没有密码文件的管理员，管理端永久无法登录。
2. 已有管理员仍处于首次改密状态、但一次性密码文件丢失时，`UserInit` 直接返回成功，不重建文件，也没有其他恢复路径。
3. 离线 `passwd` 子命令只更新密码列：不校验最小长度、不轮换 JWT 密钥、不清 `must_change_password`、不删一次性密码文件；改密后旧会话仍有效，首次登录状态也可能残留。

另外，`AuthJWTSecretGet` 在任意读取错误时都会生成并覆盖新密钥，数据库故障会被误判为“密钥不存在”，导致所有已签发 token 意外失效。

## 决定

- `UserInit` 用 `errors.Is(err, gorm.ErrRecordNotFound)` 只处理真正的新库；其他数据库错误原样返回，启动失败而不是误建用户。
- 新建管理员时先 `Create`，再用同目录临时文件 + `Rename` 落盘一次性密码文件；`Rename` 前先删除同路径旧文件（忽略 `ErrNotExist`），兼容 Windows 不能覆盖既有目标；写入失败删除刚创建的用户并清空缓存，避免留下无法登录的半成品。
- 已有管理员仍为首次改密状态且文件缺失时，生成新的 16 位随机密码，更新数据库 hash 后原子写文件；文件写入失败回滚数据库 hash。成功恢复后保持 `must_change_password=true`，旧密码失效。
- 新增 `ValidateNewPassword`（去空白后非空、至少 8 个字符），在线改密与离线重置共用。
- 新增 `UserResetPassword`，离线改密复用在线改密的语义：bcrypt hash、清除 `must_change_password`、在事务内轮换 JWT 密钥；提交成功后更新用户与设置缓存、删除一次性密码文件。CLI 输出文案和退出码不变。
- `settingGetInternal` 的“未找到”改为哨兵 `errSettingNotFound`，`loadSettingFromDB` 只把 `gorm.ErrRecordNotFound` 映射为它；`AuthJWTSecretGet` 只在该哨兵或空值时生成新密钥，其他读取错误直接返回。
- `cmd/passwd.go` 在加载配置、初始化数据库后调用 `seal.Configure(dataDir/novaveil-encryption.key)`，因为 JWT 密钥轮换需要与运行中服务相同的 seal 密钥；随后调用 `op.UserResetPassword`。若未配置 `security.encryption_key` 且密钥文件不存在，CLI 直接报错而不是让 `seal.Configure` 静默生成新密钥，避免旧 `nv1:` 密文被新密钥永久锁死。

## 备选方案

- **把用户创建和文件写入包进一个数据库事务**：文件系统不在事务里，提交后写文件失败仍会留下用户；提交前写文件又可能文件存在而事务回滚。最终采用“先建用户、写文件失败补偿删除”的可回滚顺序。
- **文件缺失时直接清空数据库重建管理员**：会丢失用户表 ID 与其他潜在用户数据；改为只重置密码并保留账号。
- **离线改密继续只写 password 列**：无法清首次改密状态，也不会让旧 JWT 失效；本次改为复用 `UserResetPassword`。
- **所有设置读取错误都生成新 JWT 密钥**：会把临时数据库故障扩大为全站会话失效；改为只对“记录不存在”生成。
- **CLI 自行实现 bcrypt/事务/文件删除**：会与在线改密逻辑分叉；统一收敛到 `op` 层。

## 后果

- 数据库读失败时启动直接失败，不再静默创建第二个管理员。
- 初始密码文件丢失可在重启时自动恢复，但会生成新密码并使旧密码失效；管理员仍需首次登录后改密。
- 离线改密现在要求至少 8 个字符，会轮换 JWT 密钥（所有旧会话失效），并清除首次改密标记、删除一次性密码文件；需要能读取与运行中服务相同的 data 目录和 seal 密钥文件。密钥文件缺失时 CLI 直接失败，不会生成新密钥覆盖旧密文；首次部署应先启动一次服务生成密钥，或显式配置 `security.encryption_key`。
- 设置读取的数据库错误不再触发 JWT 密钥轮换；缓存命中路径不受影响。
- 已知上限：`UserInit` 的补偿删除若同时失败，仍会留下无密码文件的用户，只能记录错误并依赖人工介入；`passwd` 仍只操作首个用户，单管理员模型不变。

## 验证

- `internal/op/user_test.go`：
  - `TestUserInitRegeneratesMissingPasswordFile`：文件缺失时重建、旧密码失效、新密码可验证、二次启动不重复重置。
  - `TestUserInitReplacesExistingPasswordFile`：目录内残留旧密码文件时仍能初始化并替换为新文件。
  - `TestUserInitRollsBackUserWhenPasswordFileWriteFails`：最终路径不可写时用户行回滚为 0。
  - `TestUserInitPropagatesDatabaseErrors`：查询注入错误原样返回，不新增用户。
  - `TestUserResetPasswordRotatesSecretAndClearsFlag`：新密码生效、旧密码失效、JWT 密钥变化、首次改密标记清除、一次性文件删除。
  - `TestUserResetPasswordRejectsShortPassword`：短密码不改变 hash、JWT 密钥和文件。
- 用户执行：`go test ./internal/op -count=1`，`go vet ./...`，`pnpm verify-notes`。
- 关联：[离线 passwd 子命令](../feature/2026-10-11-offline-passwd-command.md)（本笔记部分取代其“不调 seal、仅校验非空、不轮换 JWT、不删初始文件”的决定）、[凭据加密与导出脱敏](./2026-09-21-credential-at-rest-encryption-and-export-redaction.md)。
