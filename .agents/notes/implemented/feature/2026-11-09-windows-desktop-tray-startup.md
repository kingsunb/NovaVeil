# Agent Note: Windows 双击启动的桌面入口与托盘退出

Status: implemented

## 问题

Windows 发行包只有控制台 CLI：双击 `novaveil.exe` 走 Cobra 根命令 `cmd/root.go`，根命令没有 Run/RunE，只打印帮助后退出；用户必须开终端敲 `novaveil start`，再手动打开 `http://127.0.0.1:8080`。也没有常驻图标与可见的退出入口——关掉控制台窗口等于硬杀进程，停机钩子（在途请求、任务泵、评估、DB flush/close）全部被跳过。

## 决定

- 根命令新增 RunE：Windows 无参数启动进入桌面模式，其他平台仍 `cmd.Help()`。
- `cmd/start.go` 拆成 `prepareStart()`（banner/配置/日志级别）与 `startService()`（原有全部初始化 + `server.Start` + 注册停机钩子）；CLI `start` 在 `startService()` 后继续 `shutdown.Listen()`，行为不变。
- 桌面模式（`cmd/desktop_windows.go`）：`startService()` 成功后用 `fyne.io/systray` 驻留通知区，启动不自动打开浏览器；菜单「打开控制台」与左键单击托盘图标都重开浏览器，「退出」与 Ctrl+C 都调用 `shutdown.Shutdown()` 后 `systray.Quit()`，复用同一套幂等停机链（`internal/utils/shutdown/shutdown.go:108-110`），不另写一套退出逻辑；单击打开与图标透明圆角的细节见 [Windows 桌面模式改为单击托盘打开](2026-11-10-windows-desktop-tray-open-on-click.md)。
- 浏览器地址由 `desktopURL()`（`cmd/desktop_url.go`）从监听配置派生：空/`0.0.0.0`/`::`/`[::]` 回退 `127.0.0.1`，其余 host 原样；打开用既有 `golang.org/x/sys/windows.ShellExecute`，失败只告警不阻断服务。
- 保留控制台子系统（不传 `-H=windowsgui`）：双击后窗口持续显示日志，Windows ZIP 结构与 `start`/`version` 等显式子命令不变。
- 托盘图标是 `cmd/tray_icon.ico`（多尺寸 16–256，透明圆角），`go:embed` 进 Windows 构建，不是运行时资源；exe 文件图标由构建脚本从同一 ICO 生成 `.syso`，见 [Windows exe 图标资源打包](../process/2026-11-10-windows-exe-icon-resource.md)。
- `scripts/build.sh` 的 Go 许可证清单改为 linux+windows 依赖并集：`GOOS=windows go list -deps` 才会列出托盘库，原清单只跑 linux 会漏报 Windows 专属依赖。

## 备选方案

- **`-H=windowsgui` 隐藏控制台** — 最强论据是接近原生桌面应用；否掉因为会吞掉 CLI 标准输出，需要拆 CLI/GUI 两个二进制，双份产物与构建矩阵复杂化，且用户明确要保留命令行窗口。
- **`github.com/pkg/browser` 打开浏览器** — 最强论据是跨平台封装现成；否掉因为本项目只在 Windows 桌面模式开浏览器，`golang.org/x/sys/windows` 已是直接依赖，多引一个归档库不划算。
- **托盘库自研（Win32 NOTIFYICONDATA）** — 最强论据是零第三方依赖；否掉因为 Win32 消息循环/图标/菜单样板多、易错，违背成熟轮子优先。
- **托盘里再放“重启服务”等运维项** — 否掉因为 YAGNI：本次只要求打开控制台与退出，更多入口等真实需求再加。

## 后果

- 收益：Windows 双击即可用，托盘提供可见退出入口，且退出与 CLI 停机同链路（在途请求完成、任务/评估收尾、DB 关闭）；其他平台与显式 CLI 行为不变。
- 代价与已知上限：桌面模式固定读默认 `data/config.json`（`--config` 仍需走 `start`）；无单实例保护，端口占用时双击会立即失败退出；托盘不可用时（如无桌面会话）`systray.Run` 无法驻留，仅剩控制台；新增 `fyne.io/systray v1.12.2` 依赖（Windows 端纯 Go，`CGO_ENABLED=0` 可交叉编译）。
- 重访信号：需要自定义配置/端口从桌面模式直达时，把 `--config` 提升为根命令持久 flag；需要安装包/开机自启时再引入 installer 流程。

## 验证

- `go test ./cmd -run TestDesktopURL -v`
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o novaveil.exe .` 后在 Windows 双击：控制台出现、启动不自动打开浏览器、托盘图标出现；单击托盘图标打开控制台；托盘「退出」或 Ctrl+C 后进程结束且日志显示停机钩子执行。
- `pnpm verify-notes`
