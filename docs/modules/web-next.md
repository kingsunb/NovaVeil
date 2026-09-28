# 前端控制台（web-next）

React 19 + Vite 8 + TypeScript 6 的管理台，React Query 5 管服务端状态，Tailwind 4 + 自维护 shadcn/ui 原语做 UI，构建产物经 `static/out` 嵌入 Go 二进制（见 [build-ci.md](build-ci.md)）。

## 怎么做的

### 页面与结构

- 11 个页面：登录、总览（KPI + Token 趋势 / 热力图）、渠道、自定义模型、分组、模型评估、脱敏、API 密钥、日志（全链路实时可视化）、设置（12 分区）、对话。页面级 lazy chunk + 分类 skeleton + 路由滚动复位 + 导航预加载。
- `lib/api.ts` 是唯一 API 客户端（约 80 端点），统一 `/api/v1` 前缀与 `{code,message,data}` 信封；导出走 rawDownload 不经信封。
- 灰度基建 `lib/flags.ts`：构建期默认 + 运行时 runtime.json + localStorage 调试覆盖，匿名桶 sessionStorage 固定。

### 鉴权前端面

- JWT Cookie（`credentials: "include"`，前端不存 token）；401 三路统一广播（http 封装 / rawDownload / Chat 裸流）触发全局登出；登出五步：后端撤销 → cancelQueries → 清缓存 → 清 Chat localStorage → 状态更新，全程 generation 竞态守卫。
- 强制改密门挂在路由树之上整体替换页面，URL 直跳无法绕过。
- SSE 三流（log overview / group runtime / eval queue）手动 `es.close()` + 指数退避 1s→30s 重连，组件卸载 / 登出即关闭，无僵尸流；断连 30 秒节流探活状态接口。

### 凭据生命周期

- 明文凭据遵循「组件 state + generation 守卫 + 关闭即弃」：API Key 创建明文经 ref 旁路 mutation.data、对话框关闭即清；渠道 Key 不进 draft / React Query 缓存。
- **渠道代理是例外**：列表与编辑页直接明文显示（含 userinfo 口令），随列表进入 React Query 缓存——有意决策，见 [2026-09-24-show-channel-proxy-plaintext-in-admin-ui](../../.agents/notes/implemented/simplification/2026-09-24-show-channel-proxy-plaintext-in-admin-ui.md)。

### 测试体系

- Vitest 5 + happy-dom：575 个用例；覆盖率门禁覆盖 lib / ui / charts；Playwright E2E：侧栏导航 8 场景冒烟 + axe 无障碍扫描；size-limit 体积预算门禁。

### 渠道测试反馈

渠道列表、编辑器单模型/批量模型和逐密钥测试共用设置中的 `channel_test_message`，空值回退默认测试问题。编辑器以代次和模型请求票据隔离关闭、切换渠道、切换密钥之前的请求，旧结果和旧批次的收尾不能改写当前界面。关闭后未发出的批量项停止发送，已发出的请求继续由日志展示。成功显示耗时与回复，空文本显示明确提示，失败显示错误原因。新增提示集中在 `locales/zh-CN.ts`。

## 设计想法

- **401 统一广播而不是各页自处理**：过期会话必须从所有页面瞬间退出，任何"页面自己处理 401"的实现都会漏掉在途请求与 SSE 流。
- **密钥不进 Query 缓存**：React Query 缓存是持久化的模块级状态，密钥一旦进去就很难界定生命周期——用 ref / 组件 state 旁路让它随组件卸载自然消亡。
- **版本看门狗 + chunk 失效恢复**：单二进制部署下前端与后端永远同版本升级，旧页面持有过期 chunk 的场景真实存在，必须能自动恢复而不是白屏。

## 已知边界

- Chat（手写 SSE 解析）、Mask（脱敏配置）仍缺页面单测，Chat 也无 e2e——Chat SSE 解析器单测列为 ROADMAP 未来项。channel-editor 有针对测试结果与异步隔离的回归测试，不代表完整页面覆盖。
- 无完整 i18n 体系，多数文案硬编码中文；渠道测试的新提示使用 locale 常量。其余文案与 AGENTS.md「UI 文案走 locale」的约定仍有差距。
- SSE「连上即断」场景 onopen 会重置退避为固定 1s 重连。
- 巨型组件：channel-editor 2303 行、Settings 2108、Groups 1882、Logs 1679，回归保护靠手写用例密度。

## 深入阅读

- 前端美化后续计划与验收：[frontend-redesign/FOLLOW_UP.md](../frontend-redesign/FOLLOW_UP.md)
- 路由与交互性能规划：[frontend-redesign/ROUTE_TRANSITION_PERFORMANCE_PLAN.md](../frontend-redesign/ROUTE_TRANSITION_PERFORMANCE_PLAN.md)
- 验收基线：[STABILITY_UX_BASELINE.md](../STABILITY_UX_BASELINE.md)
