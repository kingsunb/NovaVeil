# Agent Note: Windows exe 图标资源打包

Status: implemented

## 问题

Windows 发行包的 `novaveil.exe` 在资源管理器里显示默认空白图标。托盘图标 `cmd/tray_icon.ico` 已经通过 `go:embed` 嵌入并用于通知区，但 exe 文件图标属于 PE 资源（`RT_GROUP_ICON`），Go 的 `go:embed` 不会写进 PE 资源节；`scripts/build.sh` 也没有生成 `.syso`，所以文件图标一直缺失。

## 决定

- `scripts/build.sh` 在构建 Windows 目标前，用固定版本 `go-winres v0.3.3` 从 `cmd/tray_icon.ico` 生成 `rsrc_windows_<arch>.syso`，`go build` 会自动链接同目录下匹配当前 `GOOS/GOARCH` 的 `.syso`。
- 生成命令为 `go run github.com/tc-hib/go-winres@v0.3.3 simply --arch <arch> --manifest none --icon cmd/tray_icon.ico --out rsrc`；`--manifest none` 表示不额外注入 manifest，避免改变现有启动行为。
- `scripts/build.sh` 退出时用 trap 删除 `rsrc_windows_*.syso`，`.gitignore` 同步忽略该文件，避免把生成物提交进仓库。
- 图标源与托盘图标共用 `cmd/tray_icon.ico`（16/20/24/32/48/64/128/256 多尺寸），不新增第二份图标资产。

## 备选方案

- **提交预生成的 `.syso`**：构建不依赖 `go-winres` 和网络，但 `.syso` 是不可读二进制，图标更新容易过期，且一个 `.syso` 只对应一个架构；否掉，改为构建时生成。
- **`akavel/rsrc`**：同样能生成 `.syso`，但最后版本 v0.10.2（2021）维护活跃度低，且多尺寸 ICO 与架构参数要自己处理；否掉。
- **`windres` / `llvm-rc`**：系统资源编译器更通用，但要求构建机预装 binutils/LLVM，Windows 与 Linux 交叉构建路径不一致；否掉。
- **同时写入版本信息/manifest**：能让文件属性更完整，但本次只缺图标，版本信息已有 `-ldflags` 注入，额外资源属于 YAGNI；否掉。

## 后果

- 收益：`scripts/build.sh --targets windows/amd64` 产出的 `novaveil.exe` 自带与托盘一致的文件图标；不新增仓库二进制文件，图标更新只需改 `cmd/tray_icon.ico`。
- 代价：构建 Windows 目标时首次需要联网拉取 `go-winres v0.3.3`（之后走 Go module cache）；构建结束会清理 `.syso`，所以手动 `go build` 仍不会自动带图标，必须走 `scripts/build.sh`。`go-winres` 只在构建期使用，不进入最终二进制，也不新增运行时依赖。
- 重访信号：如果 CI 要求完全离线构建，改为在 CI 缓存中预置 `go-winres`，或改回提交预生成 `.syso`。

## 验证

- `bash scripts/build.sh --targets windows/amd64` 后检查 `build/bin/novaveil-windows-amd64` 的 PE 资源：`go run github.com/tc-hib/go-winres@v0.3.3 extract --dir build/winres-extract build/bin/novaveil-windows-amd64` 应能看到 `RT_GROUP_ICON`。
- 在 Windows 资源管理器中查看 `novaveil.exe`，图标应与托盘图标一致。
- `pnpm verify-notes`
