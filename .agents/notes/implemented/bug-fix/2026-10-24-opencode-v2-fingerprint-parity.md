# Agent Note: OpenCode v2 免费档指纹对齐官方 2.1.4（平台感知 shell 描述 + 补 todowrite + 版本升级）

Status: implemented

## 问题

NovaVeil 的 v2 免费档（`/inference`）按 OpenCode 2.x 客户端指纹伪装出站请求。与官方源码（`anomalyco/opencode` tag v2.1.3 / v2.1.4）逐字段比对后，发现三处偏差：

1. **shell 工具描述不区分平台**。embed 的 `opencode_tools.json` 捕获的是 Unix 措辞（"Commands run on Linux using bash"），真实 2.x 客户端在 Windows 上发 "on Windows using powershell"。NovaVeil 在所有平台都输出 Unix 措辞，Windows 网关上 shell 工具的 description 与官方客户端逐字节不匹配，上游若按工具描述校验则 403。opencode2api 已在 `v2ShellDescription`（`upstream.go:1043`）修复此点。
2. **embed 工具表缺 `todowrite`**。v2.1.3 / v2.1.4 源码确认 `todowrite` 仍是注册工具（`tool/plugin/todo.ts`，`export const name = "todowrite"`），但 NovaVeil 与 opencode2api 的 `opencode_tools.json` 逐字节相同，都不含它。`todowrite` 在 2.x 是实验性 opt-in（`experimental.todo === true`），默认不发，但开启后客户端自带定义无法被 `opencodeToolMatches` 识别为已知工具，走占位降级。
3. **v2 版本串钉在 2.1.3**。最新 v2 tag 是 v2.1.4，其请求头集合与 UA 格式与 v2.1.3 完全一致（源码逐行确认），UA 版本串滞后。

不做会怎样：第 1 点在 Windows 部署上每个带 shell 工具的免费档请求都指纹不匹配；第 2 点在用户开启 experimental.todo 时 todowrite 工具无法逐字节校验/替换；第 3 点是保真度滞后，不影响功能。

## 决定

### 平台感知 shell 描述（`internal/relay/opencode_free.go`）

新增 `opencodeToolDescription(name, description string) string`：当 `name == "shell"` 且 `runtime.GOOS == "windows"` 时，将描述中的 "on Linux using bash" 替换为 "on Windows using powershell"，其余情况原样返回。

该函数在两处调用：
- `opencodeToolMatches`：比对时用平台调整后的描述与客户端工具比较，Windows 客户端发的 powershell 措辞能匹配通过，避免不必要的工具替换。
- `opencodeFreeTool`：生成工具定义时用平台调整后的描述，确保注入/替换的 shell 工具描述与同平台官方客户端一致。

### 补 todowrite 到 embed 工具表（`internal/relay/opencode_tools.json`）

从 v2.1.3 源码（`tool/plugin/todo.ts` 的 Input schema + `tool/todowrite.txt` 的描述）提取真实定义，加入 `opencode_tools.json`。parameters 为 `todos` 数组，每项含 `content`（string）与 `status`（enum: pending / in_progress / completed / cancelled）。插入位置在 `subagent` 与 `webfetch` 之间，保持字母序。

### v2 版本串升级到 2.1.4

`opencodeV2ClientVersion` 从 `"2.1.3"` 改为 `"2.1.4"`。同步更新 `builtin.go` 中四处内置渠道 CustomHeader 的 UA 字面量（三处 `cli` + 一处 `desktop`）与 `opencode_free_test.go` 的 UA 断言。

## 备选方案

- **只在 `opencodeFreeTool` 做平台调整，不改 `opencodeToolMatches`** — 改动更小；否掉是因为 Windows 客户端发的 powershell 描述与 embed 的 Linux 描述比对不通过，触发不必要的工具替换（先删后插），虽然最终输出正确但多一次 marshal 往返，且若 marshal 与客户端原始 JSON 有细微差异（如键序）反而引入新偏差。
- **按平台加载不同的 embed json** — 最彻底；否掉是因为只有 shell 一个工具的描述随平台变化，为单字段维护两份 json 不合算（YAGNI）。
- **不补 todowrite（它是 experimental opt-in）** — 当前默认不发，功能无影响；否掉是因为 embed json 的职责是收录所有已知工具定义以支持逐字节校验/替换，缺 todowrite 是表的不完整，且用户开启 experimental.todo 后客户端定义走占位降级与已知工具走真实定义的行为不一致。
- **版本串保持 2.1.3** — v2.1.3 与 v2.1.4 头集合/UA 格式一致，功能无影响；否掉是因为 2.1.4 是最新 v2 tag，保真度应跟最新，且 v2.1.3 没有对应 release（只有 tag），用 2.1.4 更不容易被按 release 存在性校验挑出。

## 后果

- **收益**：Windows 网关上 shell 工具描述与官方 2.x 客户端逐字节一致；embed 工具表覆盖 v2.x 全部 14 个注册工具（含 experimental 的 todowrite）；v2 UA 版本串对齐最新 tag。
- **代价与已知上限**：新增一个 `opencodeToolDescription` 函数与 `runtime` 导入；`opencodeToolMatches` 多一次函数调用（仅 shell + Windows 有实际开销）。todowrite 的 parameters 从源码 Schema 推导而非捕获真实请求，若 AI SDK 的 Schema-to-JSON-Schema 转换有额外注解字段则可能与真实客户端存在细微差异——重访信号：用户开启 experimental.todo 后上游对 todowrite 工具 403。
- 重访信号：opencode 发布 v2.1.5+ 且改变头集合/工具集/UA 格式。

## 验证

- `opencodeToolDescription("shell", "...on Linux using bash...")` 在 `runtime.GOOS == "windows"` 时返回含 "on Windows using powershell" 的描述；在 Linux/macOS 原样返回。
- `opencodeToolDescription("edit", "...")` 无论平台均原样返回。
- `python3 -c "import json; print(sorted(json.load(open('internal/relay/opencode_tools.json')).keys()))"` 输出含 `todowrite`，共 14 个键。
- `grep -rn '2\.1\.3' internal/` 无残留（排除 `error_log_mask.go` 与 `state.go` 中 design 章节引用）。
- `grep -n 'opencodeV2ClientVersion' internal/relay/opencode_free.go` 值为 `"2.1.4"`。
- `go test ./internal/relay/ -run TestApplyOpencodeFreeLaneHeaders` 通过（UA 断言已更新为 2.1.4）。
