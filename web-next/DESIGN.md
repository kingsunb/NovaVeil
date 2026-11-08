# NovaVeil 控制台重设计 · 设计稿 v0.1（原型阶段）

> 本文档是 `web-next/` 原型的设计依据。目标：在**不改动现有 `web/` 前端与后端**的前提下，
> 重新设计控制台的信息架构、视觉语言与交互模式，先交付可点击的高保真静态原型（纯 HTML/CSS/JS，零构建），
> 后续再分阶段迁移到 React 实现。

---

## 1. 设计调研结论（为什么这么设计）

调研对象：new-api (46k★)、one-api (36k★)、one-hub、AxonHub (5.1k★)、LiteLLM UI、shadcn/dashboard、Tremor。

关键共识：

1. **侧边栏布局已是行业标准** —— 4 个参考项目全部使用可折叠侧边栏。现有 NovaVeil 的顶部胶囊导航在资源增多后容量不足。
2. **生态已投票 shadcn/ui + Tailwind 方向**（AxonHub 基于 shadcn-admin），zinc 中性色 + 单一主色的克制风格显高级。
3. LLM 网关通用 UX 惯例必须遵循：
   - 渠道列表带**厂商标识**；
   - 密钥一律 `sk-…x4f2` 掩码 + 一键复制 + 创建后仅展示一次的对话框；
   - 模型用 chips 展示，超出 `+N` 折叠；表单中为可搜索多选；
   - 状态一律语义化 pill（emerald/amber/red/zinc + 圆点）；
   - 行内「测试」按钮 + 延迟结果反馈；
   - 日志从平面表格升级为 **行 → 右侧 Sheet 追踪面板**（请求体/响应/尝试时间线）——AxonHub/LiteLLM 共同趋势。
4. 现有版本缺失的**仪表盘页**应补上：后端 `GET /api/v1/update/now-version` 已返回
   总请求数 / 客户端 IP 数 / 24h 错误数 / Token 用量 Top10，数据现成，只差界面。

---

## 2. 信息架构（IA）

旧版 4 页：Channel / Group / Log / Setting（API Keys、统计、模型测试全部塞在 Settings 卡片流里）。

新版 6 页，按「运营 / 接入」两组组织：

```
运营 Operations
├── 总览 Dashboard      ← 新增（KPI + 趋势图 + 错误速览）
├── 渠道 Channels       ← 原 Channel 页
└── 分组 Groups         ← 原 Group 页

接入 Access
├── API 密钥 API Keys   ← 从 Settings 提升为一级页
├── 日志 Logs           ← 原 Log 页（实时 + 错误两个 Tab）
└── 设置 Settings       ← 收窄为纯配置（外观/账户/系统/保留策略/同步/备份/关于）
```

映射到的后端能力（原型字段均真实存在）：

| 页面 | 数据来源 |
|---|---|
| 总览 | `GET /api/v1/update/now-version`（version/client_ip_count/total_requests/error_count_24h/tokens_by_model）、`GET /api/v1/log/errors?limit=5` |
| 渠道 | `/api/v1/channel/*` 全套（list/create/update/enable/delete/export/fetch-model/test/sync） |
| 分组 | `/api/v1/group/*` + SSE `/api/v1/group/runtime/stream`（冷却/亲和倒计时徽章） |
| 密钥 | `/api/v1/apikey/*` |
| 日志 | SSE `/api/v1/log/overview/stream`、`GET /api/v1/log/errors`、`:id/request-body|response-body`、stop 系列 |
| 设置 | `/api/v1/setting/*`、`/api/v1/user/*`、`/api/v1/channel/sync` |

---

## 3. 布局蓝图

```
┌──────────┬────────────────────────────────────────────────────────────┐
│          │ 顶栏 56px:  页面标题+面包屑 · 版本徽标 · 主题切换           │
│ 侧边栏    ├────────────────────────────────────────────────────────────┤
│ 240px    │                                                            │
│ 可折叠至  │   内容区: 流式宽度, 24px 内边距, 不设 max-width             │
│ 56px 图标 │                                                            │
│ 导航栏    │   Dashboard : 4 KPI 卡 → 主图表(时间范围Tab) → 双列         │
│          │               [Token Top 榜 | 最近错误表]                   │
│ ─ 运营 ─ │   Channels  : 工具栏(搜索/筛选/排序/导出/+新建)              │
│  总览     │               → 高密度表格(厂商标/状态pill/模型chips)        │
│  渠道     │               → 行内测试按钮, 点击行 → 右侧 Sheet 编辑      │
│  分组     │   Groups    : 组卡片(成员拖拽序/运行时状态徽章)              │
│ ─ 接入 ─ │   API Keys  : 表格(掩码密钥/过期/模型chips) + 仅一次弹窗    │
│  密钥     │   Logs      : 顶部实时计数条 → Tab[实时流|错误]             │
│  日志     │               → 虚拟化表格 → 行点击追踪 Sheet              │
│  设置     │   Settings  : 左子导航 + 分区表单(分区独立保存)             │
│          │                                                            │
│ 底部:     │                                                            │
│ 主题·用户 │                                                            │
└──────────┴────────────────────────────────────────────────────────────┘
```

- 复杂资源的编辑（渠道、日志追踪）统一用**右侧 Sheet（抽屉）**而非居中大弹窗 —— 保持列表上下文可见。
- 需要强聚焦的低频操作（新建渠道、创建密钥、创建分组）仍用居中 Dialog。
- 顶栏不再提供全局命令面板入口与快捷键。

---

## 4. 设计令牌（Design Tokens)

### 4.1 色彩与材质（macOS 冷白磨砂 + Apple 蓝）

| 角色 | Light | Dark |
|---|---|---|
| 画布背景 | `#F2F4F7` 冷白灰 | `#161719` |
| 玻璃表面 | `rgba(255,255,255,.72)` | `rgba(38,39,42,.74)` |
| 弱表面(hover/表头) | `rgba(120,120,128,.08)` | `rgba(255,255,255,.075)` |
| 边框 | `rgba(60,60,67,.12)` | `rgba(255,255,255,.10)` |
| 文本主/次/弱 | `#1D1D1F` / `#636366` / `#98989D` | `#F5F5F7` / `#B5B5BA` / `#77777D` |
| 主色 Primary | `#007AFF` (hover `#006EE6`) | `#0A84FF` (hover `#409CFF`) |
| 成功/运行 | `#248A3D` + 低透明绿底 | `#30D158` + 低透明绿底 |
| 警告/冷却/转换 | `#B25000` + 低透明橙底 | `#FFD60A` + 低透明黄底 |
| 危险/失败 | `#D70015` + 低透明红底 | `#FF453A` + 低透明红底 |
| 图表系列 | `#60A5FA #34D399` | 同色提亮档 |

材质规则：侧栏、顶栏、卡片和浮层使用 `saturate + backdrop-filter` 形成真实磨砂层；用细描边、内高光和柔和阴影区分层级。Apple 蓝仅用于主要操作和图标选中态，状态色只表达状态，不做装饰；系统开启“减少透明度”时退化为实色表面。

### 4.2 字体

- 无衬线：Inter / system-ui 栈；等宽：JetBrains Mono / ui-monospace —— 密钥、URL、模型名、数字**必须等宽**。
- 字阶（后台高密度）：页面标题 20/28 semibold · 区块标题 14/20 semibold · 正文与表格 13/20 · 辅助 12/16。
- 数字列右对齐 + `tabular-nums`。
- 字重仅 400/500/600。

### 4.3 间距 / 密度 / 圆角

- 4px 栅格；卡片内边距 16px；区块间距 24px；网格 gap 12–16px。
- 表格行高 44px；圆角：卡片 8px、控件 7px、pill/chips 全圆。
- elevation 使用 1px 半透明描边、0.5px 内高光与低扩散柔和阴影；Dialog/Sheet 使用更强浮层阴影。

---

## 5. 组件规范

| 组件 | 规范 |
|---|---|
| 状态 Pill | 8px 圆点 + 文本；enabled=绿 / disabled=zinc / cooling=琥珀(带倒计时) / affinity=青 / failed=红 |
| 密钥展示 | mono 掩码 `sk-Nv…KD6S` + copy 按钮（点击变 ✓ 1.5s）；创建成功弹窗含警示条「仅此一次展示」 |
| 模型 Chips | 小圆角标签；auto 来源降透明度、manual 来源主色描边（沿用现有语义）；溢出 `+N` |
| 厂商标识 | 类型→字母圆徽：openai=墨绿 / responses=青 / anthropic=赭橙 / gemini=蓝 / volcengine=火红（原型用字标，正式版换品牌 SVG） |
| 中继路径 Badge | `透传`=emerald、`转换`=amber（对应 relay_mode passthrough/converted）|
| 表格操作 | hover 显露行内图标按钮；破坏性操作两段确认（点一下变红色 ✓ 再点执行）|
| 表单 | label 在上；复杂表单分区卡 + 锚点小节（渠道编辑：凭据→模型→限制→高级）；每个分区独立保存 |
| KPI 卡 | 数值大号 tabular-nums + 同比/说明一行小字 + 右上角迷你趋势 |
| 图表 Tooltip | 白底/黑底描边浮层，左标签右数值 |

---

## 6. 页面线框与要点

### 6.1 登录
全屏渐变网格背景，居中卡片：Logo、用户名、密码、「信任此设备(30天)」复选、登录按钮。
错误内联提示；锁定时显示 429 剩余秒数。

### 6.2 总览 Dashboard（新增）
```
[总请求 1024 ▲] [客户端 IP 42] [24h 错误 3] [Token In/Out]
[ 主图表卡: Token 用量趋势   | 时间范围 Tab: 24h/7d/30d ]
[ Token Top10 BarList      | 最近错误(5条, class pill, 链接去日志页) ]
```

### 6.3 渠道 Channels
工具栏：搜索 · 状态筛选(全部/启用/停用) · 标签筛选 · 排序 · 导出 · ＋新建。
表格列：状态(switch+pill) | 渠道名(厂商标+tags) | 类型 badge | Base URL(mono截断) | Keys(n 徽章) | 模型 chips(+N) | RPM/并发 | 最近测试 | 操作(测试/克隆/删除)。
行点击 → Sheet 编辑器，四个锚点分区：凭据(类型/BaseURL/Keys 多行+批量粘贴) / 模型(chips + 拉取模型 + 正则过滤) / 限制(RPM/并发/每模型限额表) / 高级(代理/自定义头/参数覆盖/自动同步)。

### 6.3.1 渠道模型目录（本次优化）

本次参考的是 DeepSeek Harness 的真实模型设置实现，而不是只借鉴颜色：

- 官方仓库：<https://github.com/deepseek-ai/deepseek-harness>
- 供应商设置：`packages/client/ui-settings-models/src/client/ModelsSection.tsx`
- 模型行编辑：`packages/client/ui-settings-models/src/client/ModelListEditor.tsx`
- 运行时模型选择：`packages/client/ui-model-selection/src/client/ModelSelect.tsx`
- 配置说明：<https://raw.githubusercontent.com/deepseek-ai/deepseek-harness/master/packages/client/ui-settings-models/README.md>

Harness 的可复用原则：

1. **供应商分组**：供应商是稳定的配置身份，显示名称与内部 ID 分离；模型目录挂在供应商下。
2. **模型用紧凑表格行展示**：不把每个模型做成大卡片；一行优先放 Model ID、显示名称、删除和展开入口。
3. **高级信息默认收起**：上下文窗口、最大输出等不干扰主列表，需要时再展开编辑。
4. **发现与保存分离**：Fetch available models 先展示候选模型和复选框，用户确认后才加入草稿。
5. **选择与管理分离**：运行时选择器保留供应商分组，配置页负责凭据、目录发现和模型管理。
6. **状态分层**：凭据已配置、路由可用、目录加载失败分别表达，不混成一个模糊的在线状态。

NovaVeil 原型在此基础上针对渠道场景增加：

- 渠道头部同时显示供应商、实际上游协议（如 `openai/chat_completions`）和 Base URL。
- 模型目录顶部显示总数、自动同步数、手动添加数。
- 按模型 ID/显示名称搜索、按 `auto/manual` 来源筛选、按名称/来源排序。
- 复选框批量选择、全选当前结果、批量移除；单模型删除仍保留行内操作。
- 模型行展示稳定 ID、显示名称、能力标签（文本/视觉/推理/工具等）、上下文窗口和输出上限。
- `+` 展开高级区：`model_limits.max_output` 与 `model_limits.thinking_level`，默认不占用列表空间。
- 「从上游拉取」进入候选模型对话框：候选项可搜索、显示能力和已添加状态，确认后以 `source=auto` 加入，不覆盖已有手动模型。

其中能力、上下文窗口和显示名称目前是原型的目录预览数据；NovaVeil 当前后端 `ChannelModel` 只持久化 `name/source`，
真实接入时应以 `/api/v1/channel/fetch-model` 返回的模型为准，`max_output/thinking_level` 则映射到现有 `model_limits`。
这样既保留 Harness 的信息层级，也不误把尚未存在于后端的数据字段当作已实现能力。
编辑 Dialog 两 Tab：成员(左侧选择器[渠道模型|组引用] + 右侧排序列表) / 转发配置(重试超时/粘滞/冷却探测/透传偏好 prefer_passthrough/应急成员)。

### 6.5 API 密钥
表格：名称 | 密钥掩码+copy | 支持模型 chips | 过期(永久=灰 pill) | 状态 | 操作。
创建 Dialog：名称 / 密钥留空自动生成 / 过期日期+时间+永不开关 / 支持模型(分组名多选) / 成功态仅展示一次。

### 6.6 日志 Logs
顶部计数条：●运行 n · 成功 n · 失败 n · 总计 n（实时）。
Tab 实时：时间 | 状态pill | 模型→渠道→目标模型 | 路径badge(透传/转换) | IP | Key | tokens(in/cached/out) | 耗时(运行中跳动)。
行点击 → 追踪 Sheet：左[分组|请求体 JSON树] 右[尝试时间线(seq/渠道/延迟/结果) + 响应体] + 停止按钮。
Tab 错误：class 筛选 + 条数 + 清空；卡片式 err_brief + 可展开 err_detail。

### 6.7 设置
左子导航：外观(主题/语言) · 账户(用户名/密码, 强改密横幅) · 系统(全局代理/CORS 列表) · 日志保留(错误天数/条数/会话归档开关) · 模型测试(选渠道→逐模型测试) · LLM 同步(间隔+手动同步+上次时间) · 备份(导出JSON/导入+结果表) · 关于(版本/构建/前后端一致性强刷)。

---

## 7. 微交互清单

复制按钮 →✓ 1.5s；KPI 数字滚动；骨架屏形状同最终内容；乐观开关失败回滚 toast；
健康但降级的节点呼吸灯；行操作 hover 显露；`prefers-reduced-motion` 全面尊重；主题切换无过渡动画防闪烁。

---

## 8. 落地路线图

| 阶段 | 内容 | 产出 |
|---|---|---|
| P0（本次） | 静态高保真原型，Mock 数据对齐真实 API 形状 | `web-next/`（零依赖，任意静态服务器可部署）|
| P1 | Vite+React 新工程复刻本设计系统（tokens→CSS vars→shadcn theme），接真实 API 只读 | `web-next/` 升级为可构建工程，与 `web/` 并行部署 |
| P2 | 写操作接入 + SSE 实时流 + 虚拟化长列表 | 功能对齐现有前端 |
| P3 | 灰度替换 `web/`，旧入口保留回滚开关 | 正式切换 |

> 原型所有 Mock 数据结构均取自后端 struct 的 json tag（RequestState/ErrorLog/Channel/Group/APIKey 等），
> P1 阶段可直接替换为真实 fetch，无需改视图层。
