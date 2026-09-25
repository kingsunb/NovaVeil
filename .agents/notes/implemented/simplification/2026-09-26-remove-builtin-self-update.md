# Agent Note: 删除内置自更新模块

Status: implemented

## 问题

2026-09 多代理审计把 `internal/update` 的供应链完整性列为 High（发现 U-1/U-2/U-3/U-4，另见同域 low 项）：自更新下载仅依赖 HTTPS 与同源 `SHA256SUMS`（TOFU——校验和与二进制同信道，挡不住发布源本身失陷）、无版本比较（`releases/latest` 回退即降级）、标记链路吞错（可误回滚或永不回滚）、二进制替换窗口无崩溃自愈。该模块是整个进程里唯一的远程代码执行入口。

NovaVeil 的实际部署形态是 Docker（`docker-compose.yml` 以 `NOVAVEIL_IMAGE` 指定镜像），三处 compose/smoke 配置全部显式 `NOVAVEIL_DISABLE_SELF_UPDATE=true`——自更新在生产路径上从未启用。审计建议的修复（签名校验、版本闸门、启动自愈）是在维护一条部署形态用不到的 RCE 入口。

## 决定

彻底删除内置自更新，升级统一走镜像（Docker）或人工替换二进制（独立部署）：

- 删除 `internal/update/` 整个包（`core.go`/`update.go`/三个测试文件）：下载、`SHA256SUMS` 校验、zip 解压限额、原子替换、`.update-pending` 标记、三次失败自动回滚、`restartExecutable` 全部移除。
- `cmd/start.go` 移除 `update.CheckPendingUpdate()`（启动最早期的回滚检查）与 `update.ClearUpdateMarker()`（监听成功后清标记）两处调用。
- `internal/server/handlers/update.go` 移除 `/api/v1/update` 路由组（`GET latest`、`POST update` 即触发替换重启的 `updateFunc`）及 `update` 包 import；文件保留并收拢为 `/api/v1/stats` 统计组（`now-version`/`build-info`/`token-trends`/`usage-detail`/`usage-heatmap`），前端版本看门狗仅依赖只读 `build-info`，不受影响。
- 移除环境变量 `NOVAVEIL_ENABLE_SELF_UPDATE`/`NOVAVEIL_DISABLE_SELF_UPDATE`/`NOVAVEIL_GITHUB_PAT`：`docker-compose.yml`、`docker-compose.local.yml`、`scripts/smoke-test-image.sh` 删除对应行，README 双语环境变量表删除 `NOVAVEIL_GITHUB_PAT`。
- 文档同步：删除 `docs/modules/update.md` 及 `docs/README.md` 模块表中的条目；`docs/SECURE_DEPLOYMENT.md` 与 `docs/STANDALONE_DEPLOYMENT.md` 的升级章节改为「自更新已整体移除，按镜像/校验后归档升级」。

## 备选方案

- **按审计建议补签名校验（minisign/cosign）+ 版本闸门 + 启动自愈**：完整性最强，但维持一条 Docker 部署用不到的 RCE 入口并引入签名密钥管理义务；已否决。
- **保留代码、维持默认关闭**：零改动，但审计发现的降级/回滚缺陷继续躺在树里，后续维护者还要背着 `internal/update` 的测试与契约走；已否决。
- **只保留回滚检查（CheckPendingUpdate）**：面向二进制升级的独立部署仍有价值，但失去上游（写标记方）后是死代码；已否决。

## 后果

- **收益**：删除进程内唯一远程代码执行入口（审计 High/medium ×4 + low ×2 一并消解）；少一个可执行文件自我替换的攻击面与一套标记/回滚状态机；`go vet`/`go test ./internal/...` 全绿验证无残留引用。
- **代价与已知上限**：独立（非 Docker）部署失去一键升级，须按 `docs/STANDALONE_DEPLOYMENT.md` 手动「重新构建 + 替换二进制 + 重启」（可用 `scripts/verify-release-archive.sh` 先校验归档）；未来若有非 Docker 大规模部署需求，须新开笔记重访（建议以带签名的发布校验重来，而非恢复 TOFU 下载器）。
- **边界**：前端版本看门狗（`/stats/build-info` 错位检测）与统计端点行为不变；`.agents/notes/` 历史笔记中关于自更新的事实描述按归档约定不改写。

## 验证

- `go build ./...`、`go vet ./...`、`go test ./internal/...` 全部通过；`grep -rn "internal/update\|SELF_UPDATE\|NOVAVEIL_GITHUB_PAT" --include='*.go' --include='*.yml' --include='*.sh'` 仅剩 docs/README 的历史性陈述与 CHANGELOG 历史条目（归档叙事，不改写）。
