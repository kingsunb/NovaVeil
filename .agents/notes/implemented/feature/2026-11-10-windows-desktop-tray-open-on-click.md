# Agent Note: Windows 桌面模式改为单击托盘打开

Status: implemented

## 问题

Windows 发行包的 `novaveil.exe` 双击进入桌面模式后，`cmd/desktop_windows.go` 在托盘 `onReady` 末尾调用 `openConsole(target)`，每次启动都会自动弹出浏览器。用户希望启动后先安静驻留托盘，由用户单击托盘图标再打开控制台；同时 `cmd/tray_icon.ico` 的圆角外是不透明黑底，和网页 favicon 的透明圆角不一致，托盘与 exe 文件图标都带黑边。

## 决定

- 桌面模式启动不再自动打开浏览器；托盘 `onReady` 只设置图标、tooltip 与菜单，并调用 `systray.SetOnTapped(openConsole)`，左键单击托盘图标打开控制台。
- 左键单击即时打开；`openConsole` 对 500ms 内的重复调用去重，双击托盘不会打开两个标签页（不实现双击语义）。
- 右键菜单保留「打开控制台」与「退出」；菜单项与单击回调复用同一 `openConsole(target)`，不新增第二套打开逻辑。
- 保留控制台子系统（与 [Windows 双击启动的桌面入口与托盘退出](2026-11-09-windows-desktop-tray-startup.md) 一致）；用户选择保留启动日志与 CLI 输出，本轮不隐藏控制台、不切 `-H=windowsgui`。
- `cmd/tray_icon.ico` 改为按 `web-next/public/favicon.svg` 的透明圆角样式重新渲染：16/20/24/32/48/64/128/256 多尺寸 RGBA，圆角外透明；exe 文件图标继续由 `scripts/build.sh` 从同一 ICO 生成 `.syso`，见 [Windows exe 图标资源打包](../process/2026-11-10-windows-exe-icon-resource.md)。

## 备选方案

- **保留启动自动打开浏览器**：用户明确希望启动后不弹窗、只驻留托盘；否掉。
- **双击托盘图标打开**：systray v1.12.2 的 Windows 实现只处理 `WM_LBUTTONUP`/`WM_RBUTTONUP`，只提供 `SetOnTapped`/`SetOnSecondaryTapped`；双击要自己做约 300ms 的两次单击判定，单击会延迟，且用户已选择单击打开；否掉。当前只做 500ms 去重，不实现双击语义。
- **`-H=windowsgui` 或启动时隐藏控制台**：用户选择保留控制台；`-H=windowsgui` 会吞掉 CLI 标准输出并需要拆分 GUI/CLI 两个二进制，隐藏控制台仍可能闪窗；否掉。
- **把托盘图标改成满幅无边框**：用户选择网页 favicon 的透明圆角样式；否掉。

## 后果

- 收益：启动不再自动弹浏览器；左键单击托盘图标即可打开；托盘与 exe 文件图标圆角外透明，和网页 favicon 一致；CLI 行为不变。
- 代价与边界：左键单击不再弹出托盘菜单，退出/重开入口移到右键菜单；双击托盘会按一次打开（500ms 去重），不会开两个标签页；托盘初始化失败时（例如无交互桌面会话）仍只有控制台，需手动访问 `http://127.0.0.1:8080`；用户选择保留控制台，双击 exe 仍会看到控制台窗口；`cmd/tray_icon.ico` 是提交的栅格资产，后续改 `favicon.svg` 需要重新渲染并替换该文件。
- 重访信号：需要双击语义，或要求双击完全不显示控制台时，再评估自绘双击判定或 GUI 子系统方案；需要从 `favicon.svg` 自动重建 ICO 时，补一个生成脚本。

## 验证

- `go test ./cmd -run TestDesktopURL -v`
- `bash scripts/build.sh --targets windows/amd64` 后启动 `build/bin/novaveil-windows-amd64`：托盘出现 NovaVeil 图标，启动不再自动打开浏览器，左键单击图标打开控制台，双击不会打开两个标签页，右键菜单可退出。
- `go run github.com/tc-hib/go-winres@v0.3.3 extract --dir build/winres-extract build/bin/novaveil-windows-amd64` 检查 exe 资源图标与 `cmd/tray_icon.ico` 一致。
- `pnpm verify-notes`
