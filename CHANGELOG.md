# NovaVeil 变更日志

> 本文档记录面向用户的可见变更。开发期内部重构见 git log。

## [Unreleased]

### 安全

- 渠道导出、密钥明文、整库导出改为 POST，并禁止缓存；GET 不再返回凭据
- 管理写操作在带登录 cookie 时必须提供 Origin/Referer
- 登出轮换 JWT 密钥；登录有效期上限改为 24 小时
- 自更新改为显式开启（`NOVAVEIL_ENABLE_SELF_UPDATE=true`）
- 管理端对话走 cookie 会话，不再把 API Key 明文拉进浏览器
- 增加 CSP；二进制默认监听 `127.0.0.1`

### 新增

- **请求脱敏（REQ-015）**：网关层请求打码 + 流式还原，内建 MAC/USCC/PHONE/EMAIL 等规则与自定义词；默认全关，分层开关（全局 / 分组 / 单规则）
- **日志命中明细**：日志详情展示命中规则标签、占位符与有界命中原文片段（上限 128 条 / 32KB，超限置 `mask_matches_truncated` 截断标记）
- **完全渠道透传开关**：渠道编辑器「完全渠道透传」开关，任意协议原样转发（不跳过路由 / failover / Key 轮询）
- **三态熔断与会话粘合路由**：CLOSED / OPEN / HALF-OPEN + 非阻塞半开探测，按 `X-Session-Id` 会话粘合渠道
- **模型评估四视图**：当前 / 排序 / 队列 / 历史
- **Usage 总览与热力图**：用量总览、详细指标与 TokenArena 风格热力图
- **渠道批量添加密钥、分组手动模式当前成员**
- **前端路由性能**：分类骨架屏、路由滚动复位、导航 chunk 预加载、Sidebar 预加载
- **日志详情耗时与 token 速率指标**

### 修复

- 脱敏会话映射按会话 TTL 跨请求保留；无会话键不再共享一张永不回收的表
- 脱敏配置 JSON 损坏时回退关闭，不再拒绝全部中转
- 渠道地址后端校验 http/https
- 仪表盘非永久档错误数改用量分桶，不再被错误日志条数上限扭曲
- 设置页调试变量名更正为 `NOVAVEIL_DEBUG`
- 空流合成终止事件检测与 `finish_reason` 白名单扩展
- opencode 兼容头、ZCode/Anthropic 协议兼容修复
- 版本号与 Channels 审计相关修复

## [0.2.0] — 2026-08-30

### 重大变更

- **前端重构**：`web/`（Vite+React+TS，v0.1.x）替换为 `web-next/`（同技术栈，全新设计系统）
  - 设计依据：[`web-next/DESIGN.md`](web-next/DESIGN.md)
  - 6 个核心页：Dashboard（新增）/ Channels / Groups / Keys（从设置拆分）/ Logs / Settings
  - 主题：Apple 蓝 + Zinc 中性色 + 玻璃材质；亮 / 暗双主题
  - 路由：侧边栏可折叠、⌘K 命令面板、移动端侧栏抽屉
  - 详情：[`web-next/README.md`](web-next/README.md)

### 新增

- **灰度替换基础设施**（P3）：
  - 运行时特性开关 `web-next/src/lib/flags.ts`：new-web / rollout-percent / sticky-bucket / ab-mode
  - `/__flags/default.json` + `/__flags/runtime.json` 运维可挂载覆盖
  - nginx A/B 路由网关 `web-next/router.nginx.conf`（web-router 服务）

- **完整测试与质量门禁**（P4）：
  - 136 单元测试（vitest）+ 15 端到端（Playwright）+ 5 a11y（axe-core）
  - 覆盖率达 **90.18%**；bundle 134.94 KB gzip（<200 KB 预算）
  - 前端 CI：`.github/workflows/web-next-ci.yaml`（lint / typecheck / test / size / E2E / a11y）

- **设计令牌深色模式达标**：
  - text-primary → 拆分为 `bg-primary`（按钮/图标）+ `text-primary-text`（文字，#0051D5 / 暗色浅蓝）
  - 全部 5 个核心页在 light + dark 下通过 axe-core WCAG AA 4.5:1 扫描

### 变更

- **Dashboard 全新**：4 KPI 卡 + SVG 趋势图（24h/7d/30d）+ 模型 Top + 最近错误
- **Channels Sheet 编辑器**：4 Tab（凭据 / 模型 / 限制 / 高级），含「从上游拉取模型」「测试连通」「行内启用切换」
- **Groups 成员编辑**：基本信息 / 成员双 Tab；支持上移 / 下移 / 删除 / 渠道模型新增 / 跨组引用
- **Keys 管理**：掩码 + 复制 + 过期日期 + 支持模型范围 + 「仅此一次」明文展示
- **Logs 实时流**：
  - EventSource 包装（SSE 指数退避重连）
  - **虚拟化表**（200 条不卡顿，`@tanstack/react-virtual`）
  - 追踪 Sheet（时间线 / 请求体 / 响应体）
  - 「中止当前轮次」「终止请求」按钮
- **Settings 8 区**：外观 / 账户 / 系统 / 日志保留 / 模型测试 / LLM 同步 / 备份 / 关于
  - 主题切换（light / dark / system）实时应用
  - 改密 / 改名 / 强改密横幅
  - 主题即时应用
  - JSON 导入校验（≤1MB + 必须 plain object + KV 字符串）
  - LLM 同步「立即同步全部」

### 修复

- 鉴权重定向链丢失原 URL：在已登录路由表补 `<Route path="/login" element={<Navigate to="/dashboard" replace />} />`
- useMemo 做副作用：改为 useEffect
- useState(() => setV(...)) 反模式：改 useEffect 同步
- SSE race condition：追踪 Sheet 加 `cancelled` cleanup flag
- Race condition：抽 `clearFlagsCache` 供测试 / 运维强制刷新
- 装饰图标 a11y 噪音：批量补 `aria-hidden`（34 处）
- 路由重复 Navigate：links 内部全部用 `<a>` + 装饰 `aria-hidden`
- ESLint 缺位：装 v9 flat config + react-hooks/exhaustive-deps + react-refresh

### 移除

- **`web/` 目录**（v0.1.x 的旧前端）：v0.2.0 退役

### 内部

- 后端 API 类型与前端 `src/lib/types.ts` 字段 1:1 对齐；新增 21 个 endpoint 封装
- 引入 ESLint v9 flat config（typescript-eslint 8 + react-hooks 5 + react-refresh）
- 引入 Playwright E2E（@axe-core/playwright 用于 a11y）
- 引入 size-limit 做 bundle 预算
- vitest coverage threshold：不达标 CI 失败（lines ≥ 88, functions ≥ 80, branches ≥ 85, statements ≥ 88）

### 安全

- 引入 ErrorBoundary（每个页面单独包裹，单页崩溃不影响其他页 + 顶栏）
- 全局 fetch 拦截：401 / 5xx 走 toast 报错而非裸 throw
- 灰度开关：cookie JWT 不变；前端不存 token

### 部署

- 新增 `web-next/Dockerfile`（多阶段：node:22 → nginx:1.27，非 root + read-only rootfs + /healthz）
- `docker-compose.yml` 新增 `web-next` + `web-router` 服务（profile=web-next）
- nginx 配置：CSP / Gzip / 静态长缓存 / SSE 缓冲关闭 / 24h 超时
- TLS 终止：仍走外部 nginx（见 [`docs/SECURE_DEPLOYMENT.md`](docs/SECURE_DEPLOYMENT.md)）

### 文档

- [`DESIGN.md`](web-next/DESIGN.md)：设计调研 + IA + 令牌 + 线框

## [0.1.x] — 2024

- 初始发布：4 段路由（Channel / Group / Log / Setting）
- Vite + React + TypeScript + shadcn/ui

---

## 版本约定

- **MAJOR**：破坏性 API / 数据结构变更
- **MINOR**：新功能 + 向后兼容
- **PATCH**：bug 修复 / 文档

## 发布节奏

- 每个 MINOR 版本至少 7 天观察期
- PATCH 可热修（改 `__flags.runtime.json`）
