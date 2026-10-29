# Agent Note: v2 免费档自定义工具限 33 个，溢出转文本注入

Status: implemented

## 问题

dsh 客户端一次对话带 52 个自定义工具，经网关转发到 OpenCode 免费档后必然失败：表现为 `HTTP 503 暂无可用渠道`，或连接建立后长时间无响应（挂住）。官方 opencode 客户端同样对话只带 40 个工具，一切正常。

2026-10-30 用外部实例做了完整对照矩阵（每次请求独立 `msg_` ID、独立 session，串行发送）：

| 工具构成 | 总数 | 结果 |
| --- | --- | --- |
| 纯官方 | 40 | 成功 |
| 官方 + dsh 10 / 15 / 20 / 30 / 31 / 33 / 34 | 50–74 | 全部成功 |
| 官方 + dsh 35（含 `subagent_fork`） | 75 | 失败 |
| 官方 40 + dsh 34 + `team_task_create`（跳过 `subagent_fork`） | 75 | 成功 |
| 官方 + dsh 40 / 46 | 80 / 86 | 失败 |
| 官方 40 + 剔除 `subagent_fork` 的其余 45 个 | 85 | 失败 |
| **纯 dsh（无官方工具）** | **52** | **失败** |

两个结论：

1. **官方工具集必须在场**。纯 dsh 只有 52 个却失败，官方 + dsh 30 有 70 个却成功——上游校验的是官方形态而非总数。
2. **自定义工具数量上限在 30 附近**。同数量（75）下换掉 `subagent_fork` 能过，说明 75 那次是数量踩线；但 85（已剔除 `subagent_fork`）仍失败，说明 30 个自定义工具是安全上限。

## 决定

`internal/relay/opencode_tool_alias.go` 的 `ensureOfficialToolset` 改为「白名单优先 + 名额上限 33」：

```go
const ocToolAliasMaxTools = 33
var ocToolAliasWhitelist = map[string]bool{ /* 33 个 dsh 工具名 */ }
```

- 白名单内的自定义工具先占位，若白名单未占满上限，剩余名额按客户端顺序用其他工具补足；
- 33 个之外的工具收集到 `overflow`，由新增的 `appendExtraToolsNote` 渲染成 `<additional_tools>` 文本块，追加到首条 system 消息末尾（含 name / description / parameters 原文）；
- 客户端没有白名单工具时（如其他第三方客户端），退化为「按顺序取前 33 个」，其余转文本；
- 幂等短路条件补上 `len(overflow) == 0`，否则「全部工具都溢出」时会被误判为「无需改写」而原样放行。

白名单按下表选取（46 个 dsh 工具中优先保留 33 个）：

| 组 | 工具 |
| --- | --- |
| 文件与执行（7） | `pwsh`、`read`、`write`、`edit`、`glob`、`grep`、`read_image` |
| 上下文与压缩（6） | `acp_cache`、`acp_read_original_image`、`acp_status`、`compress`、`decompress`、`search_context` |
| 子代理（5） | `subagent`、`subagent_fork`、`send_message`、`list_agents`、`interrupt_agent` |
| 后台作业（3） | `job_list`、`job_output`、`job_kill` |
| 工作流（1） | `workflow` |
| 目标与任务（4） | `create_goal`、`get_goal`、`update_goal`、`todo_write` |
| 联网（2） | `web_fetch`、`web_search` |
| 交互与其他（5） | `ask_user_question`、`exit_plan_mode`、`present`、`skill`、`working_directory` |

注：`subagent_fork` 在单变量实验中曾触发失败，但产品决策保留在名单内，若后续复现失败再单独剔除。

## 备选方案

- **把溢出工具直接丢弃**：模型完全不知道这些能力存在，dsh 的 `subagent`、`team_task_*`、`schedule_*` 等会静默失效，用户无法察觉。
- **注入到 user 消息而非 system**：会污染对话语义，且部分客户端会校验 user 消息数量；system 末尾追加对模型可见、对上游形态影响最小。
- **只保留交集 6 个工具**：请求最轻，但 dsh 连执行命令的工具都没有（它的 `pwsh` 在溢出组里），等于废掉 dsh。
- **在客户端侧减工具**：治不了其他第三方客户端，网关仍会对未整改的客户端透传。
- **不加限制、指望上游放宽**：当前 75 起必失败，不可控。

## 后果

- **作用面**：`ensureOfficialToolset` 由 v1 与 v2 两档共同调用（`shapeOpencodeFreeBody` 的两条分支），各自取 `templates[lane].Tools` 作为官方全集，因此 v1（`/zen`）与 v2（`/inference`）都受本规则约束。
- **能力损失**：溢出的工具（dsh 的 16 个，含 `subagent_fork`、`team_task_*`、`schedule_*`、`workflow` 等）**不在 `tools` 数组内，模型无法发起结构化 `tool_calls`**。文本注入只是让模型知道这些能力存在，若它在正文里输出 `{"tool": "...", "arguments": {...}}`，需要客户端自行解析执行，网关不做转换。
- **响应侧**：溢出工具没有别名映射（未改名的工具本来也不在别名表里），不影响 `rewriteToolReferences`。
- **副作用**：system 消息变长（溢出工具的 schema 原文），prompt tokens 相应增加；工具定义越长越明显。

## 验证

```sh
go build ./...
go test ./internal/relay/ -run 'TestOpencode|TestToolset' -v
```

部署后以 dsh 客户端发一次对话，检查出站报文：

```sh
docker logs novaveil --since 2m 2>&1 | grep -A 30 'outbound headers'
```

或直接看抓包：

```sh
python3 -c "
import json
for l in open('/root/capture.log'):
    d=json.loads(l)
    if d.get('dir')=='request' and 'chat/completions' in (d.get('path_client') or ''):
        b=json.loads(d['body'])
        print('tools =', len(b.get('tools') or []))
        print('system 含 additional_tools:', 'additional_tools' in json.dumps(b['messages'][0]))
"
```

预期：`tools` 总数 = 官方全集 + 30，system 消息末尾出现 `<additional_tools>` 块。

## 追加：体检模板补齐 29 个 chrome-devtools

上节的白名单逻辑依赖「官方全集」在场，但体检模板 `opencode_probe_v1.json` / `opencode_probe_v2.json` 原先只有 11 / 12 个工具，缺 29 个 chrome-devtools 浏览器工具。dsh 客户端本身不带这些工具，网关也就补不出来，导致对齐后的工具集始终不完整。

实测对照（外部实例，串行、独立 ID）：

| system | 工具构成 | 结果 |
| --- | --- | --- |
| 官方模板（体检） | 11 个官方 | 成功 |
| 官方模板 | dsh 全部 46 个 | 失败 |
| dsh 自己的 | 40 个官方 + 30 个 dsh | 成功 |
| dsh 自己的 | 11 个官方（体检定义）+ 30 个 dsh | 失败 |

规律：**纯官方工具时 11 个就够；一旦混入自定义工具，官方必须凑齐完整 40 个**。这也解释了为什么体检能过而 dsh 不能——体检没有自定义工具。

处理：把 29 个 chrome-devtools 定义追加进两个模板（v1 11→40，v2 12→41），原有工具顺序与定义不动，只做尾部追加。定义来源为官方客户端抓包，已确认不含本地路径、URL 等环境痕迹。

副作用：体检 body 增大约 22 KB，prompt tokens 相应增加；若体检对体积敏感需关注。

## 追加：多客户端白名单与溢出精简渲染

抓包实测补入四个第三方客户端后，工具集风格差异极大，白名单从「只服务 dsh」扩为按客户端分组维护，共 106 条：

| 客户端 | 工具数 | 命名风格 | 白名单命中 |
| --- | --- | --- | --- |
| dsh | 46 | 全小写 | 33 |
| LiveAgent | 26 | 首字母大写 | 26 |
| Codex | 28 | 全小写 + 双下划线命名空间 | 28 |
| Claude Code | 26 | 首字母大写 | 26 |
| Pi | 107 | 小写核心 + 大量 `mcp_*` | 17 |

每个请求只携带单一客户端的工具集，跨组重名（`read`、`Agent`、`AskUserQuestion`、`SendMessage`）对匹配无影响；同名工具在改名 `clint_` 阶段由 `taken` 集合追加下划线去重。

Pi 是首个工具数破百的客户端（107 个，body 170KB），其中 `mcp_ida_*` 约 70 个、`mcp_playwright_*` 约 25 个。白名单只收 17 个核心工具，按顺序补足到 33 后仍有 74 个溢出。若按原方案渲染完整 schema，system 会膨胀 100KB 以上，因此 `appendExtraToolsNote` 改为精简渲染：

- 描述截断到 80 字符（常量 `extraToolDescLimit`）
- 参数只输出属性名列表，不输出 schema 原文

74 个工具的文本块从约 100KB 降到约 12KB，system 总量约 182KB，仍在可控范围。

需要注意：溢出的工具不在 `tools` 数组内，模型无法发起结构化 `tool_calls`，只能按 `<additional_tools>` 里的约定输出 JSON 文本，由客户端解析。Pi 的 `mcp_playwright_*` 全部落在此列。
