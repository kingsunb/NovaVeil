# Agent Note: Windows 控制台横幅不再输出裸 ANSI

Status: implemented

## 问题

Windows 便携版双击启动时，stdout 是真实控制台，`isatty` 判定为真；但传统 conhost 默认没有开启 VT 序列处理，横幅输出的 `\033[36m`、`\033[1m` 等被原样显示成 `[36m[1m`，看起来就是乱码。既有颜色判定只看 `NO_COLOR` 与 TTY（见 [审计低风险修复](2026-10-22-audit-8888-low-risk-fixes.md)），没有覆盖「是 TTY 但终端不解析 ANSI」这一档。

## 决定

`internal/conf/banner.go` 的颜色判定改为 `NO_COLOR` 为空、stdout 是 TTY，且 `ansiSupported(fd)` 为真。

- Windows 的 `ansiSupported` 用 `GetConsoleMode` 读取当前模式，再以 `SetConsoleMode` 打开 `ENABLE_VIRTUAL_TERMINAL_PROCESSING`（同时确保 `ENABLE_PROCESSED_OUTPUT`）；读取或写入失败返回 false，横幅降级为纯文本。
- 非 Windows 的 TTY 默认按支持 ANSI 处理。
- `NO_COLOR` 与「非 TTY」仍优先短路：既不看控制台模式，也不修改它。

## 备选方案

- **引入 `github.com/mattn/go-colorable` 翻译 ANSI**：能覆盖无法开 VT 的旧控制台，但需要新增依赖，并把所有输出改走包装 Writer；本项目只有启动横幅用色，为这一处引入全局 Writer 不划算。
- **用 `WT_SESSION`、`TERM` 等环境变量猜终端能力**：Windows Terminal、VS Code、ConEmu、重定向与 ConPTY 组合太多，猜测容易误关颜色或继续输出乱码，不如直接问控制台 API。
- **Windows 一律关闭颜色**：实现最简单，但 Windows Terminal 与已开启 VT 的 conhost 会无故失去配色，也没有解决能力探测这个根因。
- **手写 Win32 控制台 API 上色**：等于自研 `go-colorable`，违背成熟轮子优先。

## 后果

- 能开启 VT 的控制台（Windows 10+ 传统 conhost、Windows Terminal）恢复正常配色；无法开启 VT 的控制台降级为纯文本，不再出现 `[36m` 乱码。
- `SetConsoleMode` 会修改当前控制台输出模式，这是让 VT 生效的必要副作用；进程退出后模式随控制台会话恢复。
- 新增 `ansiSupported` 注入点与两个按平台编译的实现文件；契约测试覆盖「TTY 但不支持 ANSI」与 `NO_COLOR`/非 TTY 短路。
- 仍然只治理启动横幅；日志与其它输出不经过这套判定。
- 未运行 go test/build（仓库禁令）。

## 验证

仓库禁止 AI 执行构建与测试，以下命令由用户执行：

```sh
go test ./internal/conf -run 'TestBanner' -count=1
```

重新构建 Windows 便携版后双击 `novaveil.exe`：传统控制台应显示纯文本横幅或正常彩色，不再出现 `[36m`；`NO_COLOR=1` 时强制纯文本。`pnpm verify-notes` 校验笔记树与格式。
