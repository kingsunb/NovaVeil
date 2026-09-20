/**
 * 路由级代码分割 loader —— 统一出口。
 *
 * 历史上有两个并行注册表：本文件的裸 import() loader 与 page-loaders 的
 * createPageLoader 缓存 loader，路径/页面名很容易漏改一边而漂移（审计
 * FE-07 / F-L2）。现在本文件不再自行维护 loader 清单，而是原样 re-export
 * page-loaders 的同名 loader：App 的 lazy()、Sidebar 的 preloadPage 与
 * CommandPalette 的预加载共享同一组 createPageLoader 实例（同一 Promise
 * 缓存 + 失败自动清 pending），不再存在第二份注册表。
 */
export {
  loadLoginPage,
  loadDashboardPage,
  loadChannelsPage,
  loadCustomModelsPage,
  loadGroupsPage,
  loadModelEvalPage,
  loadMaskPage,
  loadKeysPage,
  loadLogsPage,
  loadSettingsPage,
  loadChatPage,
  preloadPage,
} from "./page-loaders";
