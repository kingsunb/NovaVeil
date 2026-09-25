# 自更新（internal/update）

单二进制自更新：下载新版本归档、校验、替换、失败自动回滚。默认关闭。

## 怎么做的

- **开关语义**：默认关闭；`NOVAVEIL_DISABLE_SELF_UPDATE` 优先于 `NOVAVEIL_ENABLE_SELF_UPDATE`，两个都设时拒绝启用。
- **完整性校验**：下载归档后按 SHA256SUMS 清单逐文件校验，任何不匹配即中止。
- **回滚机制**：替换前保留旧二进制为 `.old` 并落 `.update-pending` 标记；新版本连续 3 次启动失败自动回滚旧二进制并 re-exec。
- **归档防护**：解压防路径穿越、跳过 symlink、文件权限固定，防止恶意归档覆盖越界路径。
- 更新源使用 GitHub Release；`NOVAVEIL_GITHUB_PAT` 可选用于绕过匿名速率限制。

## 设计想法

- **校验清单与二进制同源同信道**：SHA256SUMS 能挡住下载损坏与镜像篡改，挡不住发布源本身被攻破——完整性 ≠ 真实性。Sigstore 签名是明确未做的 remaining risk，文档如实记录而不是假装安全。
- **回滚自动化**：个人部署没有运维盯升级，"三次启动失败自动回退"把最坏情况收敛回已知可用版本。
- **默认关**：自更新是网关最高权限动作（替换自身二进制），默认关闭让"打开它"成为一次显式的信任决策。

## 已知边界

- 发布归档无签名，只防损坏不防投毒；生产部署建议用 Compose 的 `NOVAVEIL_IMAGE` digest 固定代替自更新。

## 深入阅读

- [SECURE_DEPLOYMENT.md](../SECURE_DEPLOYMENT.md) 的升级策略章节
