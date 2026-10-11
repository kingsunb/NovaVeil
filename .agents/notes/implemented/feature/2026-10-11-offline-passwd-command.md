# Agent Note: 离线 passwd 子命令免登录改密

Status: implemented

## 问题

管理员密码的唯一改密路径是登录后调 `/api/v1/user/change-password`，该接口要求提供旧密码。管理员忘记密码且无法登录时没有自救路径——除非手算 bcrypt 哈希再裸 SQL 改库。密码列存的是 bcrypt 哈希（`model.User.HashPassword`），不经过 seal 加密（seal 只加密渠道 Key、API Key、自定义头等凭据字段），因此离线改密不需要 seal 密钥文件，只需能读到 DB。

## 决定

新增 `novaveil passwd <密码>` cobra 子命令（`cmd/passwd.go`），经 `rootCommand.AddCommand` 注册。流程：`conf.Load(cfgFile)` → `db.InitDB` → `db.GetDB().First(&user)`（取首个用户）→ `user.HashPassword()`（bcrypt）→ `Model(&user).Update("password", ...)` → `db.Close`。仅校验非空，不校验 8 位强度（见后果）。完成后输出「Restart the running server to apply the change」——运行中服务器把用户缓存在 `op.userCache`（`UserInit` 启动时从 DB 载入），passwd 直写 DB 不刷缓存，必须重启才生效。

不调 `seal.Configure`：密码是 bcrypt 哈希非 seal 密文，无需加密密钥。一次性初始密码文件的删除语义不变，见 [凭据加密与导出脱敏](../bug-fix/2026-09-21-credential-at-rest-encryption-and-export-redaction.md)。

## 备选方案

- **裸 SQL UPDATE 配预算的 bcrypt 哈希** — 最强论据是零代码改动；否掉因为要运维手算 bcrypt 哈希、找 DB 路径与表结构、易写错列名，且绕过 `model.User.HashPassword` 的哈希逻辑（将来换哈希算法会漏改）。
- **启动加 `--reset-password` flag** — 最强论据是复用启动链的 conf/db 初始化；否掉因为把改密混进服务器启动路径，且要求先停运行中服务器才能起；独立子命令可在服务器停机时单独跑，职责清晰。
- **加一个无需旧密码的未认证 API reset 端点** — 最强论据是免停机、远程可调；否掉因为它在公网暴露一个未认证改密面，安全风险远大于离线 CLI（CLI 只能从宿主文件系统触达，已是信任边界）。

## 后果

- **收益**：忘记密码有免登录、免 seal 密钥的自救路径；复用既有 `conf.Load`/`db.InitDB`/`model.User.HashPassword`，零新依赖。
- **代价与已知上限**：需重启运行中服务器才生效（`userCache` 不刷）；不强制 8 位强度校验（`UserChangePassword` 有，passwd 没有）——离线 CLI 的执行者是宿主运维，信任边界与登录态改密不同，故只拦空串；`First(&user)` 取首个用户，单管理员模型下即 admin。重访信号：多用户/RBAC 落地后，「首个用户」语义需改为按用户名定位。

## 验证

- `cmd/passwd.go` 注册在 `rootCommand`，`NovaVeil --help` 列出 `passwd` 子命令；`go build ./...`/`go vet ./...`/`go test ./...` 全绿。
- 本地 SQLite 库实测：`novaveil passwd 'TestPasswd#2026'` exit 0，DB 中 password 列经 `bcrypt.CompareHashAndPassword` 比对新密码 = true；重启服务器后 `POST /api/v1/user/login` 用新密码返回 200、错密码返回 401。
