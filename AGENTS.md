# AGENTS.md

NovaVeil 是一个 LLM API 网关（Go 后端 + web-next 前端），提供渠道路由、Key 轮询、故障转移、请求脱敏、用量计费与评估。改代码前先读 [docs/README.md](docs/README.md) 了解现有文档；改架构或跨文件契约前读 [.agents/notes/README.md](.agents/notes/README.md)。

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
- **脱敏功能出厂默认全关，不可退化**：三层开关任一为 false 即跳过，关闭时热路径零开销。见 [脱敏设计](docs/脱敏开发/README.md)。
- **完全渠道透传不跳过路由**：透传指任意协议原样转发，不是绕过分组路由/failover/Key 轮询。见 [docs/CHANNEL_PASSTHROUGH.md](docs/CHANNEL_PASSTHROUGH.md)。
- **会话粘合按 `X-Session-Id`**：多轮上下文一致性靠会话粘合渠道 + 三态熔断 + 非阻塞半开探测。见 [docs/DEVELOPMENT_routing.md](docs/DEVELOPMENT_routing.md)。
- **Go 代码**：ESM 无关（Go）；后端错误显式处理，空 `catch`/`if err` 要命名错误与原因；公开函数加注释契约。
- **前端**：UI 文案走 locale；改前端行为同步更新 web-next 测试。

## Agent Note 门禁

```sh
pnpm verify-notes       # 本地校验，CI 同步运行
```

写新笔记用模板 [.agents/skills/write-notes-like-deepseek/templates/](.agents/skills/write-notes-like-deepseek/templates/)；格式规则见 [.agents/notes/README.md](.agents/notes/README.md#文件格式)。归档用 `pnpm archive-agent-note`，归档即冻结。

## 编辑本文件

`CLAUDE.md` 软链接到 `AGENTS.md`，改真实文件。每条规则自包含，详细理由链到 docs 或 notes。
