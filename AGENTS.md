# AGENTS.md

NovaVeil 是一个 LLM API 网关（Go 后端 + web-next 前端），提供渠道路由、Key 轮询、故障转移、请求脱敏、用量计费与评估。改代码前先读 [docs/README.md](docs/README.md) 了解现有文档；改架构或跨文件契约前读 [.agents/notes/README.md](.agents/notes/README.md)。

## AI 规范

本节是本仓库对话、编码、文档与提交信息的规范单一信息源（Single Source of Truth，即：唯一权威来源），适用范围为全部目录。规则未覆盖的情形，按第 1 节「禁止妄动」与第 3 节「直击根因」处理。

### 1. 绝对负向禁令（Never Rules）

1. **禁止造假**：严禁掩盖任何错误，严禁绕过报错提示，严禁使用默认值伪造成功。
2. **禁止妄动**：对技术方案或未知字段不确定时，严禁擅自做决定或脑补推测，必须主动提问。
3. **禁止附带重构**：遵循最小可行变更原则，严禁修改与当前任务无关的文件，严禁改动无关代码的缩进与排版。
4. **禁止表情符号**：全局严禁使用任何 Unicode Emoji（即：系统表情符号）；界面图标必须采用成熟的矢量图标库。
5. **禁止死循环排错**：遇到同一错误连续 2 次修复未果，必须立即熔断停手，向用户说明情况并请求协助。

### 2. 沟通与交互契约（Communication & Explanations）

- **金字塔表达**：所有回答与方案必须先用简体中文人话概括核心结论或当前状态，再展开具体技术细节。
- **自解释与零摩擦**：文档与解释必须通俗易懂，禁止出现「不看代码就不知道的魔法数字与字符串」；除通用互联网术语（如 API、Token、JSON）外，生僻术语首次出现必须紧跟括号中文注释，例如：RFC 8628（即：设备授权流规范）。
- **直奔结论**：已经拍板定案的决策，只客观陈述「选了什么」与接口契约，不再反复啰嗦论证「为什么否定了其他方案」。

### 3. 编码与实现铁律（Coding Standards）

- **YAGNI**（You Aren't Gonna Need It，即：不要为你现在不需要的功能写代码）：过早优化是万恶之源。设计架构与编写代码时不得提前抽象；保持函数职责单一、命名清晰、消除坏味道（高内聚低耦合）、编写自动化测试，让今天的代码极易被理解和修改，让未来重构的成本降到极低。
- **克制注释**：尽量不写注释，代码依靠良好的命名与强类型契约自解释；严禁复述代码表象逻辑。必须写注释时，除非另有说明一律使用简体中文，单段注释小于等于一行（严格控制在 20 字以内），仅用于解释反直觉的业务特殊逻辑。
- **成熟轮子优先链**：严禁重复造已存在的成熟轮子。选型优先级自高到低为：已存在且维护良好的成熟库 > 在现有成熟库之上叠加轻量逻辑 > 参考知名开源项目 > 彻底无法满足时方可自研。
- **直击根因**：严禁「创可贴式」打补丁（如随意吞异常、加假判断）；修复缺陷必须针对根因治理。
- **行为与契约测试优先**：严禁追求盲目的行覆盖率指标，拒绝测试无逻辑的透传与样板代码。测试必须聚焦核心领域状态流转、核心算法、边界条件（Boundary Cases，即：临界值与异常输入）与破坏性输入；优先编写可验证业务行为的黑盒契约测试，而非堆砌脆弱的白盒 Mock（即：模拟外部依赖的测试替身）。
- **边界解析与快速失败**（Fail-Fast，即：发现问题立即中止而不带病继续）：外部不可信输入（网络请求、文件、环境变量、第三方 RPC（即：跨进程远程调用））在越过系统边界的第一时间必须完成严格的模式校验与清洗；内部系统模块之间保持强契约互信，严禁层层做冗余的「防君子」校验；发现不可逆的数据损坏或破坏契约行为，必须坚决立刻报错中止，绝不向下游传播污染。

### 4. 规划、验证与审查流程（Process & Guardrails）

- **动刀前规划**：跨模块修改或复杂功能开发前，必须先梳理出实施清单（受影响的文件路径及变更概述），待确认后再编码。
- **写前先读**：写入文件前，必须先调用读取工具阅读该文件，待确认后再编码。
- **对抗性自审**：方案设计前后，必须主动切换到「挑刺者/攻击者」视角，对方案的并发竞争、安全边界与降级容灾进行对抗性自查。
- **交付必须见证据**：任务声明完成前，必须确保通过静态类型检查与编译，且必须提供 1~3 条人类可直接执行的验证命令（如测试用例命令或 API 调用指令），用实际结果证明交付质量。

### 5. Windows Shell 路由（Shell Routing）

- **工具选择**：在 Windows 上执行命令时，注册表、服务、系统管理、路径处理等原生命令使用 `powershell` 工具；简单构建、`cargo`、`git`、`npm` 等使用 `shell command`。
- **POSIX 语义**：需要 heredoc（即：脚本内嵌多行文本的写法）、`$VAR`、`&&`、管道、`grep`/`sed`/`awk` 等 POSIX 语法时，不要直接写进 cmd.exe；先把命令写成 `.sh` 文件，再用 `shell command` 调用 Git Bash，例如 `"C:\Program Files\Git\bin\bash.exe" script.sh`（按实际安装位置探测）。
- **禁止混用**：同一段命令里不要混用 PowerShell 与 bash 语法，一次只选一种 shell 语义。

### 6. 依赖选型（Dependency Selection）

- **依赖统一**：底层库在没有安全问题的前提下追求依赖统一，上层库追随稳定版最新版本。

### 本仓库执行口径

- **编译、测试、运行一律由用户执行**：AI 不得在本仓库执行 `go build`、`go test`、`go vet`、`pnpm build/test/typecheck/lint`、`scripts/run-local.sh` 或任何启动服务的动作，审计只做静态阅读与检索。第 4 节「交付必须见证据」的落地方式为：交付时给出 1~3 条可直接复制执行的验证命令，由用户执行并回报结果。
- **注释以第 3 节「克制注释」为准**：新增代码的注释单段不超过一行 20 字；既有的大段契约注释不因本规范主动重写（避免附带重构）。

## 仓库结构

```
main.go                 入口
internal/               后端核心（Go）
  server/  relay/  model/  op/  db/  conf/  client/
  eval/  task/  keylimit/  helper/  utils/  update/  testutil/
cmd/                    CLI 入口
web-next/               前端控制台（React + Vite + pnpm）
scripts/                构建/发布脚本与 agent-notes 门禁（scripts/agent-notes/）
docs/                   文档（部署、功能、路由、脱敏设计、审计）
.agents/                Agent 工作流与笔记
  notes/                工程决策记录（见 .agents/notes/README.md）
  skills/               Agent 技能（write-notes-like-deepseek）
static/  data/  logs/   运行时资源
```

## 常用命令

```sh
go build ./...          # 编译
go test ./...           # 后端单测
go vet ./...            # 静态检查
cd web-next && pnpm dev      # 前端开发
cd web-next && pnpm build    # 前端构建
cd web-next && pnpm typecheck && pnpm lint   # 前端类型检查 + lint
pnpm verify-notes       # Agent Note 门禁（树结构 + 格式 + 归档封印）
bash scripts/run-local.sh    # 本地运行
```

## 约定

- **非平凡改动必须同 PR 带一篇 Agent Note**：改了行为/架构/跨文件契约/流程工具链/测试策略/落盘或网络或配置格式，或其他维护者日后可能重访的决定。纯机械/局部编辑（排版、错别字、无歧义重命名、样式、依赖补丁、打标、CRUD）免除。详见 [.agents/notes/README.md](.agents/notes/README.md#何时写)。
- **事实与因果分离**：`docs/` 写「当前系统怎么运转」（事实，现在时）；`.agents/notes/` 写「当初为什么这么定、否过什么」（因果）。不混写。
- **笔记就地同步，非必要不新建**：重构/改名/改默认值直接更新现有笔记的事实，不追加变更流水账；决定翻转则新开一篇并互链，禁止把 `## 决定` 改写成反面。
- **禁止全局 INDEX.md**：笔记按 lifecycle/class 文件夹浏览或全仓搜索，不维护总索引（多分支并行会冲突）。
- **脱敏功能出厂默认全关，不可退化**：三层开关任一为 false 即跳过；关闭时跳过正则与还原，但仍先读取并解析一次配置，仅省去引擎开销。见 [脱敏设计](docs/脱敏开发/README.md)。
- **完全渠道透传不跳过路由**：透传指任意协议原样转发，不是绕过分组路由/failover/Key 轮询。见 [docs/CHANNEL_PASSTHROUGH.md](docs/CHANNEL_PASSTHROUGH.md)。
- **会话粘合按 `X-Session-Id`**：多轮上下文一致性靠会话粘合渠道 + 三态熔断 + 非阻塞半开探测。见 [docs/DEVELOPMENT_routing.md](docs/DEVELOPMENT_routing.md)。
- **OpenCode 官方指纹版本三处同步，升级先核对**：`internal/client/bun.go` 的 `bunRuntimeVersion`、`internal/client/bun_dial.go` 的 `bunClientHello` 指纹与注释、`internal/model/opencode_protocol.go` 的 `opencodeV1BunVersion` 必须同版本；改任何一处版本号前，先在真实官方客户端环境重抓 ClientHello 逐字段核对（核对口径见 [指纹笔记](.agents/notes/implemented/bug-fix/2026-10-24-bun-clienthello-sigalgs-update.md)）。
- **Go 代码**：ESM 无关（Go）；后端错误显式处理，空 `catch`/`if err` 要命名错误与原因；公开函数加注释契约。
- **前端**：UI 文案走 locale；改前端行为同步更新 web-next 测试。

## Agent Note 门禁

```sh
pnpm verify-notes       # 本地校验，CI 同步运行
```

写新笔记用模板 [.agents/skills/write-notes-like-deepseek/templates/](.agents/skills/write-notes-like-deepseek/templates/)；格式规则见 [.agents/notes/README.md](.agents/notes/README.md#文件格式)。归档用 `pnpm archive-agent-note`，归档即冻结。

## 编辑本文件

`CLAUDE.md` 软链接到 `AGENTS.md`，改真实文件。每条规则自包含，详细理由链到 docs 或 notes。
