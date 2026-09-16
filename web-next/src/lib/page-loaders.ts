/** 缓存同一页面在途/成功的导入；失败不留缓存，导航仍交给 lazy 的错误边界处理。 */
export function createPageLoader<T>(importPage: () => Promise<T>) {
  let pending: Promise<T> | undefined;
  const load = () => {
    if (!pending) {
      pending = Promise.resolve().then(importPage).catch((error: unknown) => {
        pending = undefined;
        throw error;
      });
    }
    return pending;
  };

  return Object.assign(load, {
    // 意图预加载是尽力而为，不触发错误 UI，也不阻断后续导航。
    preload: () => load().then(() => undefined, () => undefined),
  });
}

export const loadLoginPage = createPageLoader(() => import("@/pages/Login"));
export const loadDashboardPage = createPageLoader(() => import("@/pages/Dashboard"));
export const loadChannelsPage = createPageLoader(() => import("@/pages/Channels"));
export const loadCustomModelsPage = createPageLoader(() => import("@/pages/CustomModels"));
export const loadGroupsPage = createPageLoader(() => import("@/pages/Groups"));
export const loadModelEvalPage = createPageLoader(() => import("@/pages/ModelEval"));
export const loadMaskPage = createPageLoader(() => import("@/pages/Mask"));
export const loadKeysPage = createPageLoader(() => import("@/pages/Keys"));
export const loadLogsPage = createPageLoader(() => import("@/pages/Logs"));
export const loadSettingsPage = createPageLoader(() => import("@/pages/Settings"));
export const loadChatPage = createPageLoader(() => import("@/pages/Chat"));

// 仅包含现有生产导航页面；注册不会触发导入，也不预取任何业务数据。
const navigationLoaders = new Map([
  ["/dashboard", loadDashboardPage.preload],
  ["/channels", loadChannelsPage.preload],
  ["/custom-models", loadCustomModelsPage.preload],
  ["/groups", loadGroupsPage.preload],
  ["/model-eval", loadModelEvalPage.preload],
  ["/mask", loadMaskPage.preload],
  ["/keys", loadKeysPage.preload],
  ["/logs", loadLogsPage.preload],
  ["/settings", loadSettingsPage.preload],
  ["/chat", loadChatPage.preload],
]);

export function preloadPage(pathname: string): Promise<void> {
  return navigationLoaders.get(pathname)?.() ?? Promise.resolve();
}
