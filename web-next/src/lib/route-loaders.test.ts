import { describe, expect, it } from "vitest";
import * as pageLoaders from "./page-loaders";
import * as routeLoaders from "./route-loaders";

const LOADER_NAMES = [
  "loadLoginPage",
  "loadDashboardPage",
  "loadChannelsPage",
  "loadCustomModelsPage",
  "loadGroupsPage",
  "loadModelEvalPage",
  "loadMaskPage",
  "loadKeysPage",
  "loadLogsPage",
  "loadSettingsPage",
  "loadChatPage",
] as const;

describe("route-loaders 注册表来源", () => {
  it("所有页面 loader 都与 page-loaders 同引用，不存在第二份注册表", () => {
    for (const name of LOADER_NAMES) {
      expect(routeLoaders[name], name).toBe(pageLoaders[name]);
    }
  });

  it("预加载入口也来自 page-loaders，供 Sidebar 复用同一缓存层", () => {
    expect(routeLoaders.preloadPage).toBe(pageLoaders.preloadPage);
  });
});
