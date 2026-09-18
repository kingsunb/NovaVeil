# NovaVeil 前端页面切换与交互性能优化规划

> 目标：改善 `web-next` 页面切换时的空窗、布局跳变、滚动位置异常和高频重渲染问题。
> 范围：仅规划前端体验与运行时性能改造，不改变业务接口和视觉品牌方向。
> 现状依据：2026-09-16 对 `web-next/src` 的源码复核（原规划形成于 2026-09-15）。
> 验证边界：本次未安装依赖、构建、运行测试或启动服务；P0 三项目前已落地、待验收，其余据此区分“代码已有”“仍待实施”，均不代表性能或交互验收通过。

---

## 1. 背景与目标

源码可见的页面切换成本与待测风险如下，实际卡顿程度尚未通过浏览器测量：

1. React Router 卸载旧页面。
2. 新页面的动态 chunk 尚未加载完成，进入路由对应的分类 skeleton（已落地、待验收）。
3. 页面挂载后订阅各自的 Query；是否重新请求取决于缓存新鲜度等配置，并非每次挂载必然请求（全局 `staleTime` 为 30 秒）。
4. 部分页面（尤其 Logs）在进入后持续触发高频状态更新，造成整页重渲染。
5. 主内容区已实现 pathname 变化时即时滚动复位；真实浏览器的前进/后退及布局表现仍待验收。

目标是让用户感受到“页面立即响应、结构稳定、数据随后更新”，而不是等待新页面完成后突然重排。

### 1.1 目标指标

以下指标用于改造后的对比验收，具体基线数值在实现阶段通过浏览器性能面板记录：

- 导航点击后立即出现目标页面的稳定结构，不出现整块空白或错误形状的骨架屏。
- 已加载过的页面再次切换时，不因缓存数据缺失出现明显空窗。
- 路由切换后主滚动区域回到顶部，除非明确采用按路由保存滚动位置的策略。
- 高频导航页面的首次切换尽量在用户点击前完成 chunk 预加载。
- Logs 实时流更新不引起筛选器、工具栏和非相关区域的高频重渲染。
- 在 `prefers-reduced-motion` 下，页面仍然可用，且不依赖动画传达状态。
- 不增加新的运行时依赖，不改变后端 API 契约。

---

## 2. 问题清单与优先级

| 优先级 | 问题 | 主要位置 | 用户表现 | 处理方向 |
|---|---|---|---|---|
| P0 | 统一 Suspense fallback 与页面结构不匹配 | `web-next/src/App.tsx` | 切换时先显示错误形状，随后整页跳变 | ✅ 已落地（待验收）：稳定的路由 pending 容器与分类型 skeleton |
| P0 | 主内容区没有路由滚动复位 | `web-next/src/components/layout/AppShell.tsx` | 新页面从旧页面的滚动位置开始 | ✅ 已落地（待验收）：监听 pathname，切换时复位 `main` |
| P0 | 高频页面首次访问才加载 chunk | `web-next/src/App.tsx`、`Sidebar.tsx` | 点击导航后等待动态 import | ✅ 已落地（待验收）：导航 hover/focus 预加载，控制预加载范围 |
| P0 | 匿名灰度桶可能重新随机 | `App.tsx`、`lib/flags.ts` | 灰度状态在重渲染或状态变化时不稳定 | ✅ 已落地（待验收）：session/local 范围固定匿名 bucket |
| P1 | Logs SSE 更新导致整页重渲染 | `web-next/src/pages/Logs.tsx` | 实时日志多时页面操作卡顿 | 拆分组件、memo、合并更新 |
| P1 | Groups 筛选和 runtime 状态带动整页计算 | `web-next/src/pages/Groups.tsx` | 搜索、筛选、运行状态变化时响应变慢 | 拆分列表项、筛选 debounce、收窄更新范围 |
| P1 | 页面切换后的 Query loading 策略不一致 | 各页面 Query | 返回页面时旧数据消失，出现二次等待 | 优先展示缓存数据，区分 initial loading 与 refetch |
| P2 | Auth Context 更新传播范围过大 | `web-next/src/store/auth.tsx` | 状态刷新时壳层和页面一起重渲染 | 拆分 state/action context 或引入 selector |
| P2 | Dashboard 派生数据每次 render 重算 | `web-next/src/pages/Dashboard.tsx` | 数据更新时有额外计算 | 对 map/sort/slice 使用 `useMemo` |
| P2 | 玻璃 blur 合成成本 | `web-next/src/index.css` | 低端设备切换或滚动掉帧 | 保留品牌效果，减少常驻 blur，完善降级 |

---

### 2.1 源码状态补充（2026-09-16）

- **P0 性能三项目前已落地，待验收**：`App.tsx` 的 `LazyPage` 已按路由传入 `TableSkeleton`/`CardGridSkeleton`/`LogsSkeleton`/`SettingsSkeleton`，未登录分支改用 `LoginSkeleton`；`AppShell.tsx` 已绑定 pathname 与 main 滚动 ref 并在切换时复位；Sidebar（`preloadPage`）与 CommandPalette 已接入页面 loader 预加载，并配 `App.route-transition.test.tsx` 回归。
- **灰度匿名 bucket 已用 `sessionStorage` 固定**：`flags.ts` 的 `getAnonymousBucket()` 读取/写入 `sessionStorage`（仅接受规范整数，存储不可用时由模块内存兜底），同一浏览器会话内保持一致；粘性用户仍按 `userBucket(userId)` 哈希。依赖变化和重挂载仍需验证。
- **Groups 已有部分计算优化与缓存保护**：列表过滤/排序、渠道映射已使用 `useMemo`，自定义排序有乐观更新和失败回滚，编辑保存返回实体后直接回填 `['groups']`。仍未见搜索 debounce 或卡片 memo 隔离；性能改造需保留自定义顺序、成员名次与路由 priority 的不同语义。
- **Query 并非没有缓存策略**：`main.tsx` 已配置 30 秒 `staleTime`、4xx 不重试与禁用窗口聚焦刷新。渠道、自定义模型、分组保存已取消在途查询并回填实体；下一步是核验返回页面、后台失败和跨 query key 的展示策略，不是重做已存在的保存回填。
- **Logs、Dashboard、Auth 的结构性待办仍成立**：Logs 的 live 状态与 SSE 订阅仍在页面层，已有 `LiveTable` 及部分派生 `useMemo`，未见消息批处理；Dashboard 的模型 map/sort/slice 仍直接计算；Auth 虽有 memoized value/actions，仍是单一 context。收益必须经性能采样确认。

## 3. 分阶段实施方案

### 阶段一：切换稳定性基础（P0）

目标是先消除最明显的“空窗、跳变、错位”。

#### 3.1 路由级稳定 pending 结构

涉及：

- `web-next/src/App.tsx`
- `web-next/src/components/ui/skeleton.tsx`（如需复用或补充 skeleton）
- 可选新增 `web-next/src/components/layout/RoutePending.tsx`

方案：

1. 保留现有 `ErrorBoundary` 和按页面动态 import 的代码分割策略。
2. 复用 `skeleton.tsx` 中已有的分类骨架——`TableSkeleton`（列表页）、`CardGridSkeleton`（Groups）、`SettingsSkeleton`、`LogsSkeleton` 均已存在，当前只是 `LazyPage` 统一渲染了 Dashboard 形状的 `PageSkeleton`。本项主要是**接线**而非新建：让 `LazyPage` 接受骨架类型参数，各路由传入与目标页面匹配的骨架。
3. 修正 skeleton 与目标页面的对应关系：
   - Dashboard：`PageSkeleton`（现状正确）。
   - Channels / CustomModels / Keys / ModelEval / Mask 等列表页：`TableSkeleton`。
   - Groups：`CardGridSkeleton`。
   - Settings：`SettingsSkeleton`。
   - Logs：`LogsSkeleton`。
   - Login：当前未登录分支（`App.tsx` 未认证 `Suspense`）也用 `PageSkeleton`，与登录表单形状差异最大，改为轻量的居中卡片骨架。
4. 骨架只承担等待期间的占位，不新增大范围动画；继续遵守现有 reduced-motion 规则。
5. 对已加载数据的页面，后续优先让 Query 保留旧数据并在后台 refetch，避免页面先清空再加载。

验收：

- 从任一页面切换到任一目标页面时，主区宽度、高度和内边距保持稳定。
- 首次加载和网络较慢时，显示的 skeleton 与目标页面结构一致。
- 失败时由 ErrorBoundary 显示错误状态，不会卡在 skeleton。

#### 3.2 路由滚动复位

涉及：

- `web-next/src/components/layout/AppShell.tsx`
- `react-router-dom` 的 `useLocation`

方案：

1. 给 `main` 增加 `ref`。
2. 在 `pathname` 变化时将 `scrollTop` 复位到 0。
3. 初始版本使用即时复位，避免页面切换时出现缓慢滚动造成误解。
4. 如果后续发现列表页返回位置具有明显价值，再单独设计按 pathname 保存/恢复，不在本阶段混用两种行为。

验收：

- 从长列表底部切换到新页面，新页面从顶部开始。
- 浏览器前进/后退行为没有被全局滚动逻辑破坏。
- 移动端抽屉关闭和路由切换不出现 body 滚动锁残留。

#### 3.3 高频路由 chunk 预加载

涉及：

- `web-next/src/App.tsx`
- `web-next/src/components/layout/Sidebar.tsx`
- `web-next/src/components/layout/CommandPalette.tsx`

方案：

1. 将高频页面的动态 import 提取为具名 loader，例如 `loadDashboardPage`、`loadGroupsPage`。
2. `lazy(loader)` 继续作为实际渲染入口，保证现有错误边界和 Suspense 行为不变。
3. 侧栏导航项在 `onMouseEnter` 和 `onFocus` 时调用对应 loader；移动端点击前无法依赖 hover，因此保留点击后的正常加载路径。
4. Command Palette 选择项也复用同一组 loader，避免两套路由预加载逻辑。
5. 只预加载高频且体积可控的页面，不在首屏一次性加载全部页面。
6. 预加载函数要具备幂等性；动态 import 自身会缓存 Promise，不另建复杂缓存层。

验收：

- 鼠标悬停或键盘聚焦导航项后，目标 chunk 开始请求。
- 实际导航仍能正常工作，预加载失败时不阻断跳转。
- 首屏网络请求数量没有明显增加到影响登录和首屏渲染。

#### 3.4 固定匿名灰度 bucket

涉及：

- `web-next/src/lib/flags.ts`
- `web-next/src/App.tsx`

方案：

1. 已登录用户优先使用稳定的用户标识参与灰度计算；需要明确保留 `sticky-bucket: false` 时的非粘性语义，不能把所有登录用户都无条件固定。
2. 匿名用户在首次计算时生成随机 bucket，并保存到 `sessionStorage`；同一浏览器会话内保持一致。
3. 当 `sticky-bucket` 为 true 但用户未登录时，使用该匿名 bucket；当 `sticky-bucket` 为 false 时，保持每次计算随机的现有语义，或由产品明确决定是否改为会话固定，不能静默改变灰度策略。
4. 读取 `sessionStorage` 失败时使用模块级或组件级 ref 兜底，避免同一挂载周期内重新随机。
5. 不在 URL 中暴露灰度实现细节，也不改变服务端 flags 的字段含义。
6. 保持 flags 拉取失败时的既有默认行为。

> 注意：当前 `shouldUseNewWeb` 的实现是 `sticky-bucket && userId ? userId : Math.random()`，因此 `sticky-bucket` 关闭时即使是已登录用户也会随机。实现前需要先确认产品是否要修复为稳定用户桶，避免将“匿名桶稳定化”和“sticky-bucket 语义变更”混成同一改动。

验收：

- flags 加载完成、认证状态刷新或无关状态更新不会导致新旧前端来回切换。
- 清理会话存储后允许重新分桶，行为符合灰度预期。
- 已登录用户的分桶结果不受匿名 bucket 影响。

---

### 阶段二：高频更新页面降噪（P1）

目标是降低进入页面后的持续卡顿，尤其是实时日志和分组管理页面。

#### 3.5 拆分 Logs 页面实时更新路径

涉及：

- `web-next/src/pages/Logs.tsx`
- 可选新增 `web-next/src/components/logs/LiveLogTable.tsx`
- 可选新增 `web-next/src/components/logs/LogStatsBar.tsx`
- 可选新增 `web-next/src/components/logs/LogTraceDialog.tsx`

方案：

1. `LogsPage` 当前已经有 `LiveTable` 子组件；本项不是重复拆表，而是将现有 `LiveTable`、统计栏、追踪详情和错误日志区域的更新边界真正隔离。
2. 将 SSE 订阅和 live 状态管理集中在较小的容器中。
3. 对现有 `LiveTable`、统计栏、实时表格行、追踪详情弹窗分别使用稳定 props 和 `React.memo`；不依赖 `live` 的工具栏和错误日志区域不要接收 live props。
4. 表格行只接收自身需要的 request 数据；更新单条记录时避免无关行重复计算。
5. 对高频 SSE 消息设置轻量批处理窗口（建议 50–100ms），在一次 render 中合并多个消息；批处理不能改变最新记录优先顺序，也不能丢失最终状态。
6. `tracing` 详情只在打开弹窗或对应记录变化时更新，避免每个列表变化都重新驱动整页派生计算。
7. 保留现有连接状态、自动重连、停止全部和恢复接收行为。

验收：

- SSE 高频更新时，筛选器、按钮和非实时区域不随每条消息闪动。
- 实时列表顺序、重复 ID 替换、最多 200 条限制与现有行为一致。
- 打开追踪详情后仍能看到对应请求的最新状态。
- 切换到错误日志 Tab 后 SSE 能关闭，返回实时 Tab 后能恢复。

#### 3.6 优化 Groups 页面筛选与列表渲染

涉及：

- `web-next/src/pages/Groups.tsx`
- 相关分组卡片或列表子组件

方案：

1. 将单个分组卡片/行拆成稳定 props 的子组件并使用 `React.memo`。
2. 对文本搜索增加 150–250ms debounce；明确区分输入值和实际过滤值。
3. 保留已有列表排序/过滤与渠道映射的 `useMemo`；针对仍在卡片 render 内执行的成员排序和 runtime 派生计算测量后再优化，保证依赖数组完整，不重复包装已有计算。
4. 如果 runtime 更新频率高，只更新受影响的分组项，不让整个页面工具栏重新计算。
5. 不改变分组排序、priority 和成员编辑的现有语义。

验收：

- 连续输入搜索词时，输入框无明显延迟，列表不会每个字符产生多次无意义更新。
- runtime 状态更新仍能及时反映。
- 分组排序和编辑后的顺序与当前功能保持一致。

#### 3.7 统一 Query 的缓存显示策略

涉及：

- `web-next/src/main.tsx`
- 各页面的 `useQuery` 使用处

方案：

1. 区分 `isLoading`（无缓存的首次加载）与 `isFetching`（已有数据的后台刷新）。
2. 页面已有缓存数据时继续显示旧数据，仅在局部显示刷新状态。
3. 对适合的列表 Query 使用明确的 `placeholderData` 或保留上一次数据，避免筛选/路由切换时整块消失。
4. 不盲目提高全局 `staleTime`；按数据更新频率在页面或 query key 层级覆盖。
5. 保留现有 4xx 不重试和窗口聚焦不自动 refetch 的安全约束。

验收：

- 返回已访问页面时，缓存数据先展示，后台刷新不造成空白。
- 首次加载仍有明确 skeleton，错误状态和重试入口不被吞掉。
- 不因缓存策略展示明显过期且误导用户的关键状态。

---

### 阶段三：结构性优化与视觉微调（P2）

目标是在前两阶段稳定后，处理传播范围和低端设备成本。

#### 3.8 收窄 Auth Context 更新范围

涉及：

- `web-next/src/store/auth.tsx`
- `web-next/src/App.tsx`
- 依赖 `useAuth` 的布局和页面组件

建议顺序：

1. 先统计 `refreshStatus`、登录、登出和 bootstrapping 的实际更新频率。
2. 将稳定 action（login/logout/refresh）与易变状态拆成两个 context，减少 action 消费者被状态更新触发的重渲染。
3. 只有在仍有明显收益时，再引入 selector 模式；避免为了局部优化增加全局状态复杂度。
4. 登出清理 Query 缓存的安全语义保持不变。

#### 3.9 优化 Dashboard 派生计算

涉及：

- `web-next/src/pages/Dashboard.tsx`

将 tokens by model 的 map、sort、slice 等派生计算放入 `useMemo`，并检查数组引用是否在 Query 更新之外稳定。该项优先级低于 Logs 和 Groups，不应为了微小收益引入额外抽象。

#### 3.10 评估玻璃效果的合成成本

涉及：

- `web-next/src/index.css`
- 相关 layout class

方案：

1. 保留现有品牌玻璃风格，不整体移除 `backdrop-blur`。
2. 通过开发者工具对滚动和路由切换记录合成层开销。
3. 对常驻大面积 blur 提供低透明度或无 blur 降级类。
4. 与现有 `prefers-reduced-motion` 和透明度降级策略保持一致。

---

## 4. 建议的实施顺序与提交拆分

每个主题独立提交，便于回滚和定位体验变化：

1. `perf(frontend): stabilize route pending layout and scroll reset`
   - 路由 pending 容器
   - 分类型 skeleton
   - 路由滚动复位
2. `perf(frontend): preload frequently visited route chunks`
   - 具名 dynamic import loader
   - Sidebar/CommandPalette 预加载
3. `fix(frontend): persist anonymous rollout bucket`
   - 匿名灰度 bucket 稳定化
4. `perf(logs): isolate live stream updates`
   - Logs 组件拆分
   - SSE 批处理和 memo
5. `perf(groups): reduce filtering and list rerenders`
   - Groups 拆分、debounce、派生计算优化
6. `perf(frontend): preserve cached query content during refetch`
   - Query loading 状态统一
7. 后续单独评估 Auth Context、Dashboard 和 blur 合成优化

不得将 P2 的架构改动与 P0 的体验修复强行合并，避免难以判断收益来源。

---

## 5. 验收矩阵

遵循 [`docs/STABILITY_UX_BASELINE.md`](../STABILITY_UX_BASELINE.md)，至少覆盖以下组合：

### 5.1 页面与网络状态

- 1440px 桌面、768px 平板、390px 移动端。
- 首次进入页面、重复进入页面、浏览器前进/后退。
- 动态 chunk 慢、API 慢、API 失败、SSE 断开后重连。
- Dashboard、列表页、Groups、Logs、Settings 五类页面。

### 5.2 交互与视觉

- 切换后滚动位置正确。
- 导航项 hover、focus、点击预加载行为正确。
- 键盘 Tab/Enter/Escape 仍可操作。
- 浅色和深色主题下 skeleton 与真实页面无明显跳变。
- `prefers-reduced-motion` 开启时不依赖过渡动画。
- 无意外横向滚动，主内容区高度和侧栏状态稳定。

### 5.3 性能观察项

实现阶段记录以下对比数据，不预设脱离设备的绝对阈值：

- 路由点击到目标页面首个稳定结构出现的时间。
- 动态 chunk 请求开始时间，以及点击前是否已完成预加载。
- 路由切换期间的 React commit 次数和最长 commit 时间。
- Logs SSE 高频更新时每秒 commit 数量。
- 切换前后主线程长任务数量和持续时间。
- Query 重新进入页面时是否展示缓存数据。

---

## 6. 风险与不做事项

### 风险

- 预加载过多会增加首屏带宽和缓存压力，必须限制在高频路由。
- 过度 memo 可能造成依赖遗漏和陈旧数据，组件拆分后必须保留现有行为测试。
- SSE 批处理会增加极短的显示延迟，窗口不能过大，也不能改变最终状态。
- Query 缓存展示过期数据时，需要保留明确的刷新状态。
- 路由滚动复位与浏览器历史滚动恢复可能存在冲突，需要在真实导航流程中验证。
- 玻璃效果优化不能破坏现有主题和品牌层次。

### 本规划不包含

- 不更换 React Router、TanStack Query 或整体状态管理框架。
- 不新增后端接口，不改变 SSE 消息协议。
- 不用动画掩盖 chunk 加载或 API 请求延迟。
- 不一次性预加载所有页面。
- 不为低收益的微优化引入新的重型依赖。
- 不改变现有权限、认证、灰度和登出清缓存语义。

---

## 7. 完成定义

当以下条件全部满足时，可认为本轮优化完成：

- P0 四项已实现并通过验收矩阵。
- 高频路由首次切换不再出现明显空白和错误形状跳变。
- Logs 实时更新不会使页面整体操作明显掉帧。
- Query 缓存和错误状态行为经过回归确认。
- `prefers-reduced-motion`、键盘可用性、深浅色主题和移动端布局没有退化。
- 每个实施主题有独立变更记录，性能对比数据和残余问题写入对应 PR 或后续审计文档。
