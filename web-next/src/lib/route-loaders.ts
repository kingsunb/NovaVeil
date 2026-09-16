/**
 * 路由级代码分割 loader —— 具名动态 import，供 `lazy()` 与导航预加载复用。
 *
 * 设计要点：
 *  - 动态 `import()` 自身会缓存 Promise，因此这些 loader 天然幂等：重复调用
 *    只触发一次网络请求，后续调用返回同一个已 resolve 的 Promise，无需另建
 *    缓存层（ROUTE_TRANSITION_PERFORMANCE_PLAN §3.3）。
 *  - 仅在导航项 hover/focus 时**逐个**调用，不在首屏一次性加载全部页面，
 *    避免增加首屏带宽压力。
 *  - 把 loader 从 App.tsx 抽到独立模块，是为了打破 App → AppShell → Sidebar
 *    → App 的循环 import（Sidebar/CommandPalette 需要复用同一组 loader）。
 *
 * 每个页面是独立 chunk，首屏只加载 AppShell + Login，其余按需加载。
 */
export function loadLoginPage() {
  return import("@/pages/Login");
}
export function loadDashboardPage() {
  return import("@/pages/Dashboard");
}
export function loadChannelsPage() {
  return import("@/pages/Channels");
}
export function loadCustomModelsPage() {
  return import("@/pages/CustomModels");
}
export function loadGroupsPage() {
  return import("@/pages/Groups");
}
export function loadModelEvalPage() {
  return import("@/pages/ModelEval");
}
export function loadMaskPage() {
  return import("@/pages/Mask");
}
export function loadKeysPage() {
  return import("@/pages/Keys");
}
export function loadLogsPage() {
  return import("@/pages/Logs");
}
export function loadSettingsPage() {
  return import("@/pages/Settings");
}
export function loadChatPage() {
  return import("@/pages/Chat");
}
